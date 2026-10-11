package devicesync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const maxConversationList = 200

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
