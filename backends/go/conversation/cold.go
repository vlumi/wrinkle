package conversation

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"time"

	"github.com/tsuna/gohbase"
	"github.com/tsuna/gohbase/hrpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const coldFamily = "m"

var coldTable = []byte("message")

// coldMessage is a message row as the cold tier keeps it: the columns of
// the message table and the message's reactions.
type coldMessage struct {
	seq      uint64
	sender   ids.UUID
	clientID string
	body     []byte
	sentAt   time.Time
	outboxID uint64
	blobIDs  []byte
	// A serialized Message with only its reactions, oldest first; empty
	// for none.
	reactions []byte
}

// coldKey is the row key: the conversation id, then the seq big-endian, so
// a conversation's history is one range in seq order.
func coldKey(conversationID ids.UUID, seq uint64) []byte {
	return binary.BigEndian.AppendUint64(slices.Clone(conversationID[:]), seq)
}

func (m coldMessage) cells() map[string]map[string][]byte {
	return map[string]map[string][]byte{coldFamily: {
		"sender":    m.sender[:],
		"client_id": []byte(m.clientID),
		"body":      m.body,
		"sent_at":   binary.BigEndian.AppendUint64(nil, uint64(m.sentAt.UnixMicro())),
		"outbox_id": binary.BigEndian.AppendUint64(nil, m.outboxID),
		"blob_ids":  m.blobIDs,
		"reactions": m.reactions,
	}}
}

func coldMessageOf(cells []*hrpc.Cell) (coldMessage, error) {
	var m coldMessage
	for _, c := range cells {
		switch string(c.Qualifier) {
		case "sender":
			if len(c.Value) != 16 {
				return m, fmt.Errorf("cold message sender of %d bytes", len(c.Value))
			}
			copy(m.sender[:], c.Value)
		case "client_id":
			m.clientID = string(c.Value)
		case "body":
			m.body = c.Value
		case "sent_at":
			if len(c.Value) != 8 {
				return m, fmt.Errorf("cold message sent_at of %d bytes", len(c.Value))
			}
			m.sentAt = time.UnixMicro(int64(binary.BigEndian.Uint64(c.Value))).UTC()
		case "outbox_id":
			if len(c.Value) != 8 {
				return m, fmt.Errorf("cold message outbox_id of %d bytes", len(c.Value))
			}
			m.outboxID = binary.BigEndian.Uint64(c.Value)
		case "blob_ids":
			m.blobIDs = c.Value
		case "reactions":
			m.reactions = c.Value
		}
	}
	if len(cells) > 0 && len(cells[0].Row) == 24 {
		m.seq = binary.BigEndian.Uint64(cells[0].Row[16:])
	}
	return m, nil
}

// message is the message as GetMessages returns it, with its reactions
// unless it is deleted or a notice.
func (m coldMessage) message(conversationID ids.UUID) (*pb.Message, error) {
	msg := &pb.Message{
		ConversationId:  conversationID.Bytes(),
		Seq:             m.seq,
		SenderUserId:    m.sender.Bytes(),
		ClientMessageId: m.clientID,
		SentAt:          timestamppb.New(m.sentAt),
		BlobIds:         decodeBlobIDs(m.blobIDs),
	}
	if err := decodeBody(m.body, msg); err != nil {
		return nil, err
	}
	if msg.GetDeletedAt() == nil && msg.GetNotice() == nil {
		reactions, err := m.reactionList()
		if err != nil {
			return nil, err
		}
		msg.Reactions = reactions
	}
	return msg, nil
}

func (m coldMessage) reactionList() ([]*pb.Reaction, error) {
	holder := &pb.Message{}
	if err := proto.Unmarshal(m.reactions, holder); err != nil {
		return nil, fmt.Errorf("unmarshal cold reactions: %w", err)
	}
	return holder.GetReactions(), nil
}

func encodeReactions(reactions []*pb.Reaction) ([]byte, error) {
	if len(reactions) == 0 {
		return nil, nil
	}
	b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(&pb.Message{Reactions: reactions})
	if err != nil {
		return nil, fmt.Errorf("marshal cold reactions: %w", err)
	}
	return b, nil
}

// Cold is the cold tier of the history, in HBase. Every write puts a whole
// row, so a write can be repeated.
type Cold struct {
	client gohbase.Client
}

// NewCold connects through the ZooKeeper quorum, lazily.
func NewCold(quorum string) *Cold {
	return &Cold{client: gohbase.NewClient(quorum)}
}

func (c *Cold) Close() { c.client.Close() }

// Put writes the conversation's messages.
func (c *Cold) Put(ctx context.Context, conversationID ids.UUID, messages []coldMessage) error {
	batch := make([]hrpc.Call, 0, len(messages))
	for _, m := range messages {
		put, err := hrpc.NewPut(ctx, coldTable, coldKey(conversationID, m.seq), m.cells())
		if err != nil {
			return err
		}
		batch = append(batch, put)
	}
	res, ok := c.client.SendBatch(ctx, batch)
	if ok {
		return nil
	}
	for _, r := range res {
		if r.Error != nil {
			return unavailable("put", r.Error)
		}
	}
	return unavailable("put", errors.New("failed"))
}

// Get reads the message at seq, if the cold tier has it.
func (c *Cold) Get(ctx context.Context, conversationID ids.UUID, seq uint64) (coldMessage, bool, error) {
	defer observeRead("message", time.Now())
	get, err := hrpc.NewGet(ctx, coldTable, coldKey(conversationID, seq), hrpc.Families(map[string][]string{coldFamily: nil}))
	if err != nil {
		return coldMessage{}, false, err
	}
	res, err := c.client.Get(get)
	if err != nil {
		return coldMessage{}, false, unavailable("get", err)
	}
	if len(res.Cells) == 0 {
		return coldMessage{}, false, nil
	}
	m, err := coldMessageOf(res.Cells)
	return m, err == nil, err
}

// Scan reads up to limit messages with seq from lo to hi, newest first.
func (c *Cold) Scan(ctx context.Context, conversationID ids.UUID, lo, hi uint64, limit int) ([]coldMessage, error) {
	if lo > hi || limit <= 0 {
		return nil, nil
	}
	defer observeRead("range", time.Now())
	// Reversed, the range starts at its higher key, inclusive, and stops
	// before its lower one.
	stop := coldKey(conversationID, lo-1)
	if lo == 0 {
		stop = slices.Clone(conversationID[:])
	}
	scan, err := hrpc.NewScanRange(ctx, coldTable, coldKey(conversationID, hi), stop,
		hrpc.Reversed(), hrpc.NumberOfRows(uint32(limit)), hrpc.Families(map[string][]string{coldFamily: nil}))
	if err != nil {
		return nil, err
	}
	scanner := c.client.Scan(scan)
	defer func() { _ = scanner.Close() }()
	var messages []coldMessage
	for len(messages) < limit {
		res, err := scanner.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, unavailable("scan", err)
		}
		m, err := coldMessageOf(res.Cells)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, nil
}

// unavailable logs a failure of the cold tier and tells the device to try
// again later, without the details.
func unavailable(op string, err error) error {
	slog.Warn("cold tier failed", "op", op, "err", err)
	return apierror.Unavailable("history in the cold tier is unavailable; try again")
}

func observeRead(kind string, start time.Time) {
	metrics.ColdReadDuration.WithLabelValues(kind).Observe(time.Since(start).Seconds())
}

// change writes a changed cold row inside the transaction that tells of
// the change, which holds the conversation's row; undo puts the old row
// back, on its own deadline, unless the transaction committed.
func (c *Cold) change(ctx context.Context, conversationID ids.UUID, old, changed coldMessage) (undo func(committed *bool), err error) {
	if err := c.Put(ctx, conversationID, []coldMessage{changed}); err != nil {
		return nil, err
	}
	return func(committed *bool) {
		if *committed {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
		defer cancel()
		if err := c.Put(ctx, conversationID, []coldMessage{old}); err != nil {
			// The row keeps a change no event told of, until the next one.
			slog.Error("putting back a cold row failed", "conversation", conversationID, "seq", old.seq, "err", err)
		}
	}, nil
}
