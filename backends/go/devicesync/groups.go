package devicesync

import (
	"context"
	"database/sql"

	"github.com/vlumi/wrinkle/backends/go/accounts"
	"github.com/vlumi/wrinkle/backends/go/conversation"
	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	// maxMemberNames bounds the names one request may invite.
	maxMemberNames = conversation.MaxMembers - 1
	// leavePage is how many of a deleted user's groups are read at a time.
	leavePage = 100
)

// CreateGroup creates a group of the user and the named members. The
// user's list has it at once; the members' lists get it from the fan-out.
func (s *Service) CreateGroup(ctx context.Context, userID ids.UUID, req *pb.CreateGroupRequest) (*pb.Conversation, error) {
	if err := conversation.CheckTopic(req.GetTopic()); err != nil {
		return nil, err
	}
	if err := conversation.CheckClientGroupID(req.GetClientGroupId()); err != nil {
		return nil, err
	}
	invitees, names, err := s.resolveMembers(ctx, userID, req.GetMemberNames())
	if err != nil {
		return nil, err
	}
	// Only a new group is checked: a retry returns the group as created.
	exists, err := s.conversations.GroupExists(ctx, userID, req.GetClientGroupId())
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := s.checkContacts(userID, names)(ctx, invitees); err != nil {
			return nil, err
		}
	}
	c, err := s.conversations.CreateGroup(ctx, userID, req.GetTopic(), req.GetClientGroupId(), invitees)
	if err != nil {
		return nil, err
	}
	conversationID, _ := ids.Parse(c.GetConversationId())
	if _, err := s.db.ExecContext(ctx, `
		INSERT IGNORE INTO user_conversation
			(user_id, conversation_id, kind, topic, member_count, member, admin, last_seq, read_seq, peer_read_seq, updated_at)
		VALUES (?, ?, 'group', ?, ?, TRUE, TRUE, 0, 0, 0, ?)`,
		userID, conversationID, c.GetTopic(), c.GetMemberCount(), vtdb.Now()); err != nil {
		return nil, err
	}
	return c, nil
}

// AddMembers adds the named users to a group.
func (s *Service) AddMembers(ctx context.Context, userID ids.UUID, req *pb.AddMembersRequest) error {
	conversationID, ok := ids.Parse(req.GetConversationId())
	if !ok {
		return apierror.InvalidArgument("conversation_id must be 16 bytes")
	}
	users, names, err := s.resolveMembers(ctx, userID, req.GetMemberNames())
	if err != nil {
		return err
	}
	return s.conversations.AddMembers(ctx, userID, conversationID, users, s.checkContacts(userID, names))
}

// resolveMembers finds the named users, leaving out the requesting user
// and repeats, with the name each was first given by.
func (s *Service) resolveMembers(ctx context.Context, userID ids.UUID, names []string) ([]ids.UUID, map[ids.UUID]string, error) {
	if len(names) > maxMemberNames {
		return nil, nil, apierror.InvalidArgument("at most %d member names", maxMemberNames)
	}
	for _, name := range names {
		if !accounts.ValidName(name) {
			return nil, nil, apierror.InvalidArgument("member name %q is not a valid name", name)
		}
	}
	given := map[ids.UUID]string{userID: ""}
	var users []ids.UUID
	for _, name := range names {
		id, found, err := s.accounts.Resolve(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			return nil, nil, apierror.NotFound("no user named %q", name)
		}
		if _, seen := given[id]; !seen {
			given[id] = name
			users = append(users, id)
		}
	}
	return users, given, nil
}

// checkContacts returns a check that every user is the requesting user's
// contact; the first who is not fails, by the name given.
func (s *Service) checkContacts(userID ids.UUID, names map[ids.UUID]string) func(context.Context, []ids.UUID) error {
	return func(ctx context.Context, users []ids.UUID) error {
		contacts, err := s.contacts.Contacts(ctx, userID, users)
		if err != nil {
			return err
		}
		for _, id := range users {
			if !contacts[id] {
				return apierror.FailedPrecondition(pb.ErrorReason_ERROR_REASON_NOT_A_CONTACT, "not a contact: %s", names[id])
			}
		}
		return nil
	}
}

// LeaveGroups takes the user out of every group the list says they are
// in, as LeaveGroup does, for a deleted account.
func (s *Service) LeaveGroups(ctx context.Context, userID ids.UUID) error {
	var after ids.UUID
	for {
		rows, err := s.db.QueryContext(ctx, `
			SELECT conversation_id FROM user_conversation
			WHERE user_id = ? AND kind = 'group' AND member AND conversation_id > ?
			ORDER BY conversation_id LIMIT ?`,
			userID, after, leavePage)
		if err != nil {
			return err
		}
		page, err := vtdb.Collect(rows, func(rows *sql.Rows) (id ids.UUID, err error) { return id, rows.Scan(&id) })
		if err != nil {
			return err
		}
		for _, id := range page {
			// A group left already, the list not showing it yet, is fine.
			if err := s.conversations.Leave(ctx, userID, id); err != nil {
				switch apierror.Code(err) {
				case pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED, pb.ErrorCode_ERROR_CODE_NOT_FOUND:
				default:
					return err
				}
			}
		}
		if len(page) < leavePage {
			return nil
		}
		after = page[len(page)-1]
	}
}
