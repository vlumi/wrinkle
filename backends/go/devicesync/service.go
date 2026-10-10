// Package devicesync is the sync service: sign-in and devices, the user's
// event log, and the conversation list, against the user keyspace.
package devicesync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/conversation"
	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	maxNameLength       = 64
	defaultSyncLimit    = 100
	maxSyncLimit        = 1000
	maxConversationList = 200
)

type Service struct {
	db            *sql.DB
	conversations *conversation.Service
	now           func() time.Time
}

func New(db *sql.DB, conversations *conversation.Service) *Service {
	return &Service{db: db, conversations: conversations, now: func() time.Time { return time.Now().UTC() }}
}

func validName(name string) bool {
	return name != "" && utf8.ValidString(name) && utf8.RuneCountInString(name) <= maxNameLength
}

// Connect signs the user in by name, creating the user on first use, and
// registers the device.
func (s *Service) Connect(ctx context.Context, req *pb.ConnectRequest) (*pb.ConnectResponse, error) {
	if !validName(req.GetUserName()) {
		return nil, apierror.InvalidArgument("user_name must be 1 to %d characters", maxNameLength)
	}
	if n := req.GetDeviceName(); n != "" && !validName(n) {
		return nil, apierror.InvalidArgument("device_name must be at most %d characters", maxNameLength)
	}
	userID := ids.User(req.GetUserName())
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO user (user_id, name, log_head, created_at) VALUES (?, ?, 0, ?)
		ON DUPLICATE KEY UPDATE name = name`,
		userID, req.GetUserName(), now); err != nil {
		return nil, err
	}
	deviceID := req.GetDeviceId()
	if deviceID == 0 {
		deviceID = ids.Random()
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO device (user_id, device_id, name, acked_position, created_at, last_seen_at)
		VALUES (?, ?, ?, 0, ?, ?)
		ON DUPLICATE KEY UPDATE last_seen_at = ?`,
		userID, deviceID, req.GetDeviceName(), now, now, now); err != nil {
		return nil, err
	}
	resp := &pb.ConnectResponse{
		User:     &pb.User{UserId: userID, Name: req.GetUserName()},
		DeviceId: deviceID,
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT acked_position FROM device WHERE user_id = ? AND device_id = ?`,
		userID, deviceID).Scan(&resp.DeviceCursor); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT log_head FROM user WHERE user_id = ?`, userID).Scan(&resp.LogHead); err != nil {
		return nil, err
	}
	return resp, nil
}

// Ack records how far a device has processed the log.
func (s *Service) Ack(ctx context.Context, userID, deviceID, cursor uint64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE device SET acked_position = GREATEST(acked_position, ?), last_seen_at = ?
		WHERE user_id = ? AND device_id = ?`,
		cursor, s.now(), userID, deviceID)
	return err
}

// Sync returns events after the cursor, oldest first.
func (s *Service) Sync(ctx context.Context, userID, cursor uint64, limit uint32) (*pb.SyncResponse, error) {
	if limit == 0 {
		limit = defaultSyncLimit
	}
	limit = min(limit, maxSyncLimit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT payload FROM user_event WHERE user_id = ? AND position > ?
		ORDER BY position LIMIT ?`,
		userID, cursor, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &pb.SyncResponse{Cursor: cursor}
	for rows.Next() {
		if len(resp.Events) == int(limit) {
			resp.HasMore = true
			break
		}
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		event := &pb.Event{}
		if err := proto.Unmarshal(payload, event); err != nil {
			return nil, fmt.Errorf("unmarshal event for user %d: %w", userID, err)
		}
		resp.Events = append(resp.Events, event)
		resp.Cursor = event.GetCursor()
	}
	return resp, rows.Err()
}

// Open finds or creates the conversation with the named peer.
func (s *Service) Open(ctx context.Context, userID uint64, peerName string) (*pb.Conversation, error) {
	if !validName(peerName) {
		return nil, apierror.InvalidArgument("peer_user_name must be 1 to %d characters", maxNameLength)
	}
	peerID := ids.User(peerName)
	if peerID == userID {
		return nil, apierror.InvalidArgument("a conversation needs two different users")
	}
	var storedName string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM user WHERE user_id = ?`, peerID).Scan(&storedName)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && storedName != peerName) {
		return nil, apierror.NotFound("no user named %q", peerName)
	}
	if err != nil {
		return nil, err
	}
	userA, userB := ids.Participants(userID, peerID)
	conversationID := ids.Conversation(userA, userB)
	if err := s.conversations.Ensure(ctx, conversationID, userA, userB); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT IGNORE INTO user_conversation
			(user_id, conversation_id, peer_user_id, peer_name, last_seq, read_seq, peer_read_seq, updated_at)
		VALUES (?, ?, ?, ?, 0, 0, 0, ?)`,
		userID, conversationID, peerID, peerName, s.now()); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, conversationColumns+`
		FROM user_conversation WHERE user_id = ? AND conversation_id = ?`, userID, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list, err := scanConversations(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("conversation %d missing after insert", conversationID)
	}
	return list[0], nil
}

// ListConversations returns the user's conversations, most recently active first.
func (s *Service) ListConversations(ctx context.Context, userID uint64) ([]*pb.Conversation, error) {
	rows, err := s.db.QueryContext(ctx, conversationColumns+`
		FROM user_conversation WHERE user_id = ? ORDER BY updated_at DESC LIMIT ?`,
		userID, maxConversationList)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanConversations(rows)
}

const conversationColumns = `
	SELECT conversation_id, peer_user_id, peer_name, last_seq,
		last_sender_user_id, last_client_message_id, last_body, last_sent_at,
		read_seq, peer_read_seq`

func scanConversations(rows *sql.Rows) ([]*pb.Conversation, error) {
	var list []*pb.Conversation
	for rows.Next() {
		c := &pb.Conversation{Peer: &pb.User{}}
		var (
			sender   sql.NullInt64
			clientID sql.NullString
			body     sql.NullString
			sentAt   sql.NullTime
		)
		if err := rows.Scan(&c.ConversationId, &c.Peer.UserId, &c.Peer.Name, &c.LastSeq,
			&sender, &clientID, &body, &sentAt, &c.ReadSeq, &c.PeerReadSeq); err != nil {
			return nil, err
		}
		if c.LastSeq > 0 && sender.Valid {
			c.LastMessage = &pb.Message{
				ConversationId:  c.ConversationId,
				Seq:             c.LastSeq,
				SenderUserId:    uint64(sender.Int64),
				ClientMessageId: clientID.String,
				Text:            body.String,
				SentAt:          timestamppb.New(sentAt.Time),
			}
		}
		list = append(list, c)
	}
	return list, rows.Err()
}
