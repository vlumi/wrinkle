// Package contacts is the contact service: each user's own side toward
// other users, a request or a block, changed only through the user's
// outbox, and the checks the other services make against it, against the
// user keyspace. The fan-out copies each side to the other user's
// contact_peer and ends the other's request when a side ends.
package contacts

import (
	"cmp"
	"context"
	"database/sql"
	"slices"

	"github.com/vlumi/wrinkle/backends/go/accounts"
	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// maxPerState bounds each state's part of the list.
const maxPerState = 1000

type Service struct {
	db       *sql.DB
	accounts *accounts.Service
}

func New(db *sql.DB, accounts *accounts.Service) *Service {
	return &Service{db: db, accounts: accounts}
}

func parseUserID(b []byte) (ids.UUID, error) {
	id, ok := ids.Parse(b)
	if !ok {
		return id, apierror.InvalidArgument("user_id must be 16 bytes")
	}
	return id, nil
}

// The queries of the list, one per state, each on the user's shard: the
// user's own rows with the copies of the other side, and the copies of
// requests the user has no row for.
const (
	ownColumns = `
		SELECT c.other_user_id, c.state, c.updated_at, p.state, p.updated_at
		FROM contact c LEFT JOIN contact_peer p ON p.user_id = c.user_id AND p.other_user_id = c.other_user_id`
	listContacts = ownColumns + `
		WHERE c.user_id = ? AND c.state = 'request' AND p.state = 'request'
		ORDER BY GREATEST(c.updated_at, p.updated_at) DESC LIMIT ?`
	listOutgoing = ownColumns + `
		WHERE c.user_id = ? AND c.state = 'request' AND (p.state IS NULL OR p.state <> 'request')
		ORDER BY GREATEST(c.updated_at, COALESCE(p.updated_at, c.updated_at)) DESC LIMIT ?`
	listBlocked = ownColumns + `
		WHERE c.user_id = ? AND c.state = 'block'
		ORDER BY GREATEST(c.updated_at, COALESCE(p.updated_at, c.updated_at)) DESC LIMIT ?`
	listIncoming = `
		SELECT p.other_user_id, c.state, c.updated_at, p.state, p.updated_at
		FROM contact_peer p LEFT JOIN contact c ON c.user_id = p.user_id AND c.other_user_id = p.other_user_id
		WHERE p.user_id = ? AND p.state = 'request' AND c.user_id IS NULL
		ORDER BY p.updated_at DESC LIMIT ?`
)

type listed struct {
	otherID   ids.UUID
	own, peer Side
}

// List returns the users the user has a state with, newest change first,
// leaving out deleted accounts.
func (s *Service) List(ctx context.Context, userID ids.UUID) ([]*pb.Contact, error) {
	var all []listed
	for _, query := range []string{listContacts, listOutgoing, listBlocked, listIncoming} {
		rows, err := s.db.QueryContext(ctx, query, userID, maxPerState)
		if err != nil {
			return nil, err
		}
		part, err := vtdb.Collect(rows, func(rows *sql.Rows) (l listed, err error) {
			var ownState, peerState sql.NullString
			var ownAt, peerAt sql.NullTime
			err = rows.Scan(&l.otherID, &ownState, &ownAt, &peerState, &peerAt)
			l.own = Side{ownState.String, ownAt.Time}
			l.peer = Side{peerState.String, peerAt.Time}
			return l, err
		})
		if err != nil {
			return nil, err
		}
		all = append(all, part...)
	}
	others := make([]ids.UUID, len(all))
	for i, l := range all {
		others[i] = l.otherID
	}
	profiles, err := s.accounts.Profiles(ctx, others)
	if err != nil {
		return nil, err
	}
	list := make([]*pb.Contact, 0, len(all))
	for _, l := range all {
		if user := profiles[l.otherID]; user.GetName() != "" && !user.GetDeleted() {
			list = append(list, Contact(user, l.own, l.peer))
		}
	}
	slices.SortStableFunc(list, func(a, b *pb.Contact) int {
		return cmp.Compare(b.GetUpdatedAt().AsTime().UnixMicro(), a.GetUpdatedAt().AsTime().UnixMicro())
	})
	return list, nil
}

// Add asks a user to be a contact, or accepts their request.
func (s *Service) Add(ctx context.Context, userID ids.UUID, req *pb.AddContactRequest) (*pb.Contact, error) {
	var otherID ids.UUID
	switch u := req.GetUser().(type) {
	case *pb.AddContactRequest_Name:
		var found bool
		var err error
		if accounts.ValidName(u.Name) {
			if otherID, found, err = s.accounts.Resolve(ctx, u.Name); err != nil {
				return nil, err
			}
		}
		if !found {
			return nil, apierror.NotFound("no such user")
		}
	case *pb.AddContactRequest_UserId:
		var err error
		if otherID, err = parseUserID(u.UserId); err != nil {
			return nil, err
		}
	default:
		return nil, apierror.InvalidArgument("name or user_id is required")
	}
	if otherID == userID {
		return nil, apierror.InvalidArgument("you can't add yourself")
	}
	other, err := s.existing(ctx, otherID)
	if err != nil {
		return nil, err
	}
	own, _, err := Sides(ctx, s.db, userID, otherID)
	if err != nil {
		return nil, err
	}
	if own.State == Block {
		return nil, unblockFirst()
	}
	blocked, err := Blocks(ctx, s.db, otherID, userID)
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, apierror.PermissionDenied("can't add this user")
	}
	if own.State != Request {
		var refused bool
		err := change(ctx, s.db, userID, otherID, func(own string) (string, bool) {
			refused = own == Block
			return Request, own == ""
		})
		if err != nil {
			return nil, err
		}
		if refused {
			return nil, unblockFirst()
		}
	}
	own, peer, err := Sides(ctx, s.db, userID, otherID)
	if err != nil {
		return nil, err
	}
	return Contact(other, own, peer), nil
}

func unblockFirst() error {
	return apierror.FailedPrecondition(pb.ErrorReason_ERROR_REASON_BLOCKED_BY_YOU, "unblock this user first")
}

// existing returns the profile of a user who exists and is not deleted.
func (s *Service) existing(ctx context.Context, userID ids.UUID) (*pb.User, error) {
	user, found, err := s.accounts.Profile(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !found || user.GetDeleted() {
		return nil, apierror.NotFound("no such user")
	}
	return user, nil
}

// Remove ends any contact or request with the user, for both: the user's
// own request goes, and the outbox row is written even without one, so
// that the fan-out ends the other's request. A block stays. An unknown
// user, or oneself, changes nothing.
func (s *Service) Remove(ctx context.Context, userID ids.UUID, other []byte) error {
	otherID, err := parseUserID(other)
	if err != nil || otherID == userID {
		return err
	}
	if _, found, err := s.accounts.Profile(ctx, otherID); err != nil || !found {
		return err
	}
	return change(ctx, s.db, userID, otherID, func(own string) (string, bool) {
		if own == Request {
			return "", true
		}
		return own, true
	})
}

// Block makes the user's side a block; the fan-out ends both requests.
func (s *Service) Block(ctx context.Context, userID ids.UUID, other []byte) error {
	otherID, err := parseUserID(other)
	if err != nil {
		return err
	}
	if otherID == userID {
		return apierror.InvalidArgument("you can't block yourself")
	}
	if _, err := s.existing(ctx, otherID); err != nil {
		return err
	}
	return change(ctx, s.db, userID, otherID, func(own string) (string, bool) {
		return Block, own != Block
	})
}

// Unblock lifts the user's block; no request comes back.
func (s *Service) Unblock(ctx context.Context, userID ids.UUID, other []byte) error {
	otherID, err := parseUserID(other)
	if err != nil {
		return err
	}
	return change(ctx, s.db, userID, otherID, func(own string) (string, bool) {
		return "", own == Block
	})
}

// CheckMessaging refuses a direct conversation, or a message or reaction
// in one, between users when either blocked the other: the user's own
// block first, and the other's without saying more.
func (s *Service) CheckMessaging(ctx context.Context, userID, peerID ids.UUID) error {
	blocked, err := Blocks(ctx, s.db, userID, peerID)
	if err != nil {
		return err
	}
	if blocked {
		return apierror.FailedPrecondition(pb.ErrorReason_ERROR_REASON_BLOCKED_BY_YOU, "you blocked this user")
	}
	if blocked, err = Blocks(ctx, s.db, peerID, userID); err != nil {
		return err
	}
	if blocked {
		return apierror.PermissionDenied("can't message this user")
	}
	return nil
}

// Contacts returns which of the others are the user's contacts: both
// sides a request, each read from its owner's row.
func (s *Service) Contacts(ctx context.Context, userID ids.UUID, others []ids.UUID) (map[ids.UUID]bool, error) {
	contacts := map[ids.UUID]bool{}
	if len(others) == 0 {
		return contacts, nil
	}
	args := make([]any, 0, len(others)+1)
	args = append(args, userID)
	for _, id := range others {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT other_user_id FROM contact
		WHERE user_id = ? AND state = 'request' AND other_user_id IN (`+placeholders(len(others))+`)`, args...)
	if err != nil {
		return nil, err
	}
	asked, err := vtdb.Collect(rows, func(rows *sql.Rows) (id ids.UUID, err error) { return id, rows.Scan(&id) })
	if err != nil || len(asked) == 0 {
		return contacts, err
	}
	args = args[:0]
	for _, id := range asked {
		args = append(args, id)
	}
	args = append(args, userID)
	rows, err = s.db.QueryContext(ctx, `
		SELECT user_id FROM contact
		WHERE user_id IN (`+placeholders(len(asked))+`) AND other_user_id = ? AND state = 'request'`, args...)
	if err != nil {
		return nil, err
	}
	both, err := vtdb.Collect(rows, func(rows *sql.Rows) (id ids.UUID, err error) { return id, rows.Scan(&id) })
	for _, id := range both {
		contacts[id] = true
	}
	return contacts, err
}
