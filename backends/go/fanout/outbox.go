package fanout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Outbox rows arrive as Debezium emits them after unwrapping: binary
// columns as base64 strings, unsigned bigints as numbers or decimal strings.

// rawID is a BINARY(16) column.
type rawID []byte

func (r rawID) uuid(column string) (ids.UUID, error) {
	id, ok := ids.Parse(r)
	if !ok {
		return id, fmt.Errorf("%s of %d bytes", column, len(r))
	}
	return id, nil
}

// rawUint is a BIGINT UNSIGNED column.
type rawUint uint64

func (u *rawUint) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(b, `"`)
	n, err := strconv.ParseUint(string(b), 10, 64)
	if err != nil {
		return fmt.Errorf("unsigned bigint %s: %w", b, err)
	}
	*u = rawUint(n)
	return nil
}

// outboxRow is a row of a conversation's outbox.
type outboxRow struct {
	conversationID ids.UUID
	outboxID       uint64
	recipients     []ids.UUID
	// The conversation as of the change.
	conversationKind string
	topic            string
	memberCount      uint32
	kind             string
	// The outbox row of the message a deletion is about, 0 for others.
	targetOutboxID uint64
	// A deletion's sender and the uploads of the message's attachments.
	releasedBy      ids.UUID
	releasedUploads []ids.UUID
	event           *pb.Event
}

func parseOutboxRow(value []byte) (*outboxRow, error) {
	var r struct {
		ConversationID   rawID    `json:"conversation_id"`
		OutboxID         rawUint  `json:"outbox_id"`
		Recipients       []byte   `json:"recipients"`
		ConversationKind string   `json:"conversation_kind"`
		Topic            string   `json:"topic"`
		MemberCount      rawUint  `json:"member_count"`
		Kind             string   `json:"kind"`
		TargetOutboxID   *rawUint `json:"target_outbox_id"`
		ReleasedUploads  []byte   `json:"released_uploads"`
		Payload          []byte   `json:"payload"`
	}
	if err := json.Unmarshal(value, &r); err != nil {
		return nil, err
	}
	o := &outboxRow{
		outboxID:         uint64(r.OutboxID),
		conversationKind: r.ConversationKind,
		topic:            r.Topic,
		memberCount:      uint32(r.MemberCount),
		kind:             r.Kind,
	}
	if r.TargetOutboxID != nil {
		o.targetOutboxID = uint64(*r.TargetOutboxID)
	}
	var err error
	if o.conversationID, err = r.ConversationID.uuid("conversation_id"); err != nil {
		return nil, err
	}
	if len(r.Recipients)%16 != 0 {
		return nil, fmt.Errorf("recipients of %d bytes", len(r.Recipients))
	}
	for i := 0; i < len(r.Recipients); i += 16 {
		id, _ := ids.Parse(r.Recipients[i : i+16])
		o.recipients = append(o.recipients, id)
	}
	if n := len(r.ReleasedUploads); n > 0 {
		if n%16 != 0 || n < 32 {
			return nil, fmt.Errorf("released_uploads of %d bytes", n)
		}
		o.releasedBy, _ = ids.Parse(r.ReleasedUploads[:16])
		for i := 16; i < n; i += 16 {
			id, _ := ids.Parse(r.ReleasedUploads[i : i+16])
			o.releasedUploads = append(o.releasedUploads, id)
		}
	}
	if o.event, err = parseEvent(r.Payload); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *outboxRow) direct() bool { return o.conversationKind == "direct" }

// peerOf returns the other recipient of a direct conversation's row, which
// always has both participants.
func (o *outboxRow) peerOf(userID ids.UUID) ids.UUID {
	for _, id := range o.recipients {
		if id != userID {
			return id
		}
	}
	return ids.UUID{}
}

// userOutboxRow is a row of a user's outbox: a profile or device change.
type userOutboxRow struct {
	userID   ids.UUID
	outboxID uint64
	kind     string
	event    *pb.Event
}

func parseUserOutboxRow(value []byte) (*userOutboxRow, error) {
	var r struct {
		UserID   rawID   `json:"user_id"`
		OutboxID rawUint `json:"outbox_id"`
		Kind     string  `json:"kind"`
		Payload  []byte  `json:"payload"`
	}
	if err := json.Unmarshal(value, &r); err != nil {
		return nil, err
	}
	o := &userOutboxRow{outboxID: uint64(r.OutboxID), kind: r.Kind}
	var err error
	if o.userID, err = r.UserID.uuid("user_id"); err != nil {
		return nil, err
	}
	if o.event, err = parseEvent(r.Payload); err != nil {
		return nil, err
	}
	return o, nil
}

func parseEvent(payload []byte) (*pb.Event, error) {
	event := &pb.Event{}
	if err := proto.Unmarshal(payload, event); err != nil {
		return nil, fmt.Errorf("outbox payload: %w", err)
	}
	return event, nil
}
