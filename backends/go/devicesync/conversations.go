package devicesync

import (
	"context"
	"database/sql"
	"fmt"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/accounts"
	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const maxConversationList = 200

// Open finds or creates the conversation with the named peer.
func (s *Service) Open(ctx context.Context, userID ids.UUID, peerName string) (*pb.Conversation, error) {
	if !accounts.ValidName(peerName) {
		return nil, apierror.InvalidArgument("peer_name is not a valid name")
	}
	peerID, found, err := s.accounts.Resolve(ctx, peerName)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, apierror.NotFound("no user named %q", peerName)
	}
	if peerID == userID {
		return nil, apierror.InvalidArgument("a conversation needs two different users")
	}
	peer, found, err := s.accounts.Profile(ctx, peerID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, apierror.NotFound("no user named %q", peerName)
	}
	userA, userB := ids.Participants(userID, peerID)
	conversationID, err := s.conversations.Open(ctx, userA, userB)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT IGNORE INTO user_conversation
			(user_id, conversation_id, peer_user_id, peer_name, peer_icon, peer_avatar, peer_deleted,
			 last_seq, read_seq, peer_read_seq, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, 0, ?)`,
		userID, conversationID, peerID, peer.GetName(), peer.GetIcon(), nullBytes(peer.GetAvatar()), peer.GetDeleted(),
		s.now()); err != nil {
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
		return nil, fmt.Errorf("conversation %s missing after insert", conversationID)
	}
	return list[0], nil
}

// ListConversations returns the user's conversations, most recently active first.
func (s *Service) ListConversations(ctx context.Context, userID ids.UUID) ([]*pb.Conversation, error) {
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
	SELECT conversation_id, peer_user_id, peer_name, peer_icon, peer_avatar, peer_deleted, last_seq,
		last_sender_user_id, last_client_message_id, last_body, last_sent_at,
		read_seq, peer_read_seq`

func scanConversations(rows *sql.Rows) ([]*pb.Conversation, error) {
	var list []*pb.Conversation
	for rows.Next() {
		c := &pb.Conversation{Peer: &pb.User{}}
		var (
			conversationID, peerID ids.UUID
			sender                 []byte
			clientID               sql.NullString
			body                   sql.NullString
			sentAt                 sql.NullTime
		)
		if err := rows.Scan(&conversationID, &peerID, &c.Peer.Name, &c.Peer.Icon, &c.Peer.Avatar, &c.Peer.Deleted, &c.LastSeq,
			&sender, &clientID, &body, &sentAt, &c.ReadSeq, &c.PeerReadSeq); err != nil {
			return nil, err
		}
		c.ConversationId = conversationID.Bytes()
		c.Peer.UserId = peerID.Bytes()
		if c.LastSeq > 0 && sender != nil {
			c.LastMessage = &pb.Message{
				ConversationId:  c.ConversationId,
				Seq:             c.LastSeq,
				SenderUserId:    sender,
				ClientMessageId: clientID.String,
				Text:            body.String,
				SentAt:          timestamppb.New(sentAt.Time),
			}
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// nullBytes stores an empty id as NULL.
func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
