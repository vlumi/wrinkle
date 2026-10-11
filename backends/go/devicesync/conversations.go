package devicesync

import (
	"context"
	"database/sql"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/accounts"
	"github.com/vlumi/wrinkle/backends/go/conversation"
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
			(user_id, conversation_id, kind, peer_user_id, peer_name, peer_icon, peer_avatar, peer_deleted,
			 member, last_seq, read_seq, peer_read_seq, updated_at)
		VALUES (?, ?, 'direct', ?, ?, ?, ?, ?, TRUE, 0, 0, 0, ?)`,
		userID, conversationID, peerID, peer.GetName(), peer.GetIcon(), nullBytes(peer.GetAvatar()), peer.GetDeleted(),
		s.now()); err != nil {
		return nil, err
	}
	return s.listed(ctx, userID, conversationID)
}

// listed returns the user's list entry of the conversation.
func (s *Service) listed(ctx context.Context, userID, conversationID ids.UUID) (*pb.Conversation, error) {
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
	SELECT conversation_id, kind, peer_user_id, peer_name, peer_icon, peer_avatar, peer_deleted,
		topic, member_count, member, admin, last_seq,
		last_sender_user_id, last_client_message_id, last_body, last_sent_at,
		read_seq, peer_read_seq`

func scanConversations(rows *sql.Rows) ([]*pb.Conversation, error) {
	var list []*pb.Conversation
	for rows.Next() {
		c := &pb.Conversation{}
		peer := &pb.User{}
		var (
			conversationID ids.UUID
			kind           string
			peerID, sender []byte
			clientID       sql.NullString
			body           []byte
			sentAt         sql.NullTime
		)
		if err := rows.Scan(&conversationID, &kind, &peerID, &peer.Name, &peer.Icon, &peer.Avatar, &peer.Deleted,
			&c.Topic, &c.MemberCount, &c.Member, &c.Admin, &c.LastSeq,
			&sender, &clientID, &body, &sentAt, &c.ReadSeq, &c.PeerReadSeq); err != nil {
			return nil, err
		}
		c.ConversationId = conversationID.Bytes()
		c.Kind = conversation.Kind(kind)
		if peerID != nil {
			peer.UserId = peerID
			c.Peer = peer
		}
		if c.LastSeq > 0 && sender != nil {
			c.LastMessage = &pb.Message{
				ConversationId:  c.ConversationId,
				Seq:             c.LastSeq,
				SenderUserId:    sender,
				ClientMessageId: clientID.String,
				SentAt:          timestamppb.New(sentAt.Time),
			}
			content := &pb.Message{}
			if err := proto.Unmarshal(body, content); err != nil {
				return nil, fmt.Errorf("last message of conversation %s: %w", conversationID, err)
			}
			c.LastMessage.Content = content.Content
			c.LastMessage.DeletedAt = content.DeletedAt
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
