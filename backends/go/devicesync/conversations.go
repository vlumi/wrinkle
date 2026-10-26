package devicesync

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/accounts"
	"github.com/vlumi/wrinkle/backends/go/conversation"
	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/page"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	defaultConversationPage = 100
	maxConversationPage     = 200
	// A page token: the last entry's updated_at in Unix microseconds, then
	// its conversation id.
	pageTokenBytes = 8 + 16
)

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
		return nil, apierror.InvalidArgument("your own notes are opened with OpenNotes")
	}
	if err := s.contacts.CheckMessaging(ctx, userID, peerID); err != nil {
		return nil, err
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
		userID, conversationID, peerID, peer.GetName(), peer.GetIcon(), vtdb.NullBytes(peer.GetAvatar()), peer.GetDeleted(),
		vtdb.Now()); err != nil {
		return nil, err
	}
	return s.listed(ctx, userID, conversationID)
}

// OpenNotes finds or creates the user's notes.
func (s *Service) OpenNotes(ctx context.Context, userID ids.UUID) (*pb.Conversation, error) {
	conversationID, err := s.conversations.OpenNotes(ctx, userID)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT IGNORE INTO user_conversation
			(user_id, conversation_id, kind, member_count, member, last_seq, read_seq, peer_read_seq, updated_at)
		VALUES (?, ?, 'notes', 1, TRUE, 0, 0, 0, ?)`,
		userID, conversationID, vtdb.Now()); err != nil {
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

// ListConversations returns a page of the user's conversations, most
// recently active first, and the token of the next page, nil after the
// last.
func (s *Service) ListConversations(ctx context.Context, userID ids.UUID, token []byte, limit uint32) ([]*pb.Conversation, []byte, error) {
	if limit == 0 {
		limit = defaultConversationPage
	}
	limit = min(limit, maxConversationPage)
	query, args := conversationColumns+`
		FROM user_conversation WHERE user_id = ?`, []any{userID}
	switch len(token) {
	case 0:
	case pageTokenBytes:
		after := time.UnixMicro(int64(binary.BigEndian.Uint64(token))).UTC()
		afterID, _ := ids.Parse(token[8:])
		query += ` AND (updated_at, conversation_id) < (?, ?)`
		args = append(args, after, afterID)
	default:
		return nil, nil, apierror.InvalidArgument("page_token is not one this server gave")
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY updated_at DESC, conversation_id DESC LIMIT ?`,
		append(args, limit+1)...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	list, err := scanConversations(rows)
	if err != nil {
		return nil, nil, err
	}
	more := len(list) > int(limit)
	list = list[:min(len(list), int(limit))]
	size := 0
	for i, c := range list {
		size += proto.Size(c)
		if i > 0 && size > page.MaxBytes {
			list, more = list[:i], true
			break
		}
	}
	if !more {
		return list, nil, nil
	}
	last, _ := ids.Parse(list[len(list)-1].GetConversationId())
	var updatedAt time.Time
	if err := s.db.QueryRowContext(ctx, `
		SELECT updated_at FROM user_conversation WHERE user_id = ? AND conversation_id = ?`,
		userID, last).Scan(&updatedAt); err != nil {
		return nil, nil, err
	}
	next := binary.BigEndian.AppendUint64(nil, uint64(updatedAt.UnixMicro()))
	return list, append(next, last[:]...), nil
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
			c.LastMessage.BlobIds = content.BlobIds
		}
		list = append(list, c)
	}
	return list, rows.Err()
}
