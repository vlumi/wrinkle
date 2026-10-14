package contacts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// The states of a side as stored; contact_peer also keeps none once the
// other's side ended.
const (
	Request = "request"
	Block   = "block"
	None    = "none"
)

// Side is one user's row toward another: its state, empty when there is no
// row, and when it last changed.
type Side struct {
	State     string
	UpdatedAt time.Time
}

// View is how a user sees another, from the user's own side and the copy
// of the other's. A block by the other is never revealed.
func View(own, peer string) pb.ContactState {
	switch {
	case own == Block:
		return pb.ContactState_CONTACT_STATE_BLOCKED
	case own == Request && peer == Request:
		return pb.ContactState_CONTACT_STATE_CONTACT
	case own == Request:
		return pb.ContactState_CONTACT_STATE_OUTGOING
	case own == "" && peer == Request:
		return pb.ContactState_CONTACT_STATE_INCOMING
	}
	return pb.ContactState_CONTACT_STATE_UNSPECIFIED
}

// Contact is the other user as the user sees them, changed when either row
// last changed.
func Contact(other *pb.User, own, peer Side) *pb.Contact {
	c := &pb.Contact{User: other, State: View(own.State, peer.State)}
	at := own.UpdatedAt
	if peer.UpdatedAt.After(at) {
		at = peer.UpdatedAt
	}
	if !at.IsZero() {
		c.UpdatedAt = timestamppb.New(at)
	}
	return c
}

// PayloadSide is the side an outbox row's state names: the user's own side
// after the change.
func PayloadSide(state pb.ContactState) string {
	switch state {
	case pb.ContactState_CONTACT_STATE_OUTGOING:
		return Request
	case pb.ContactState_CONTACT_STATE_BLOCKED:
		return Block
	}
	return None
}

func payloadState(side string) pb.ContactState {
	switch side {
	case Request:
		return pb.ContactState_CONTACT_STATE_OUTGOING
	case Block:
		return pb.ContactState_CONTACT_STATE_BLOCKED
	}
	return pb.ContactState_CONTACT_STATE_UNSPECIFIED
}

// Sides reads the user's own side toward the other and the copy of the
// other's, both on the user's shard.
func Sides(ctx context.Context, q vtdb.RowQuerier, userID, otherID ids.UUID) (own, peer Side, err error) {
	if own, err = readSide(ctx, q, `
		SELECT state, updated_at FROM contact WHERE user_id = ? AND other_user_id = ?`, userID, otherID); err != nil {
		return own, peer, err
	}
	peer, err = readSide(ctx, q, `
		SELECT state, updated_at FROM contact_peer WHERE user_id = ? AND other_user_id = ?`, userID, otherID)
	return own, peer, err
}

func readSide(ctx context.Context, q vtdb.RowQuerier, query string, userID, otherID ids.UUID) (Side, error) {
	var s Side
	err := q.QueryRowContext(ctx, query, userID, otherID).Scan(&s.State, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Side{}, nil
	}
	return s, err
}

// Blocks reports whether the user blocked the other, from the user's own
// row.
func Blocks(ctx context.Context, q vtdb.RowQuerier, userID, otherID ids.UUID) (bool, error) {
	own, err := readSide(ctx, q, `
		SELECT state, updated_at FROM contact WHERE user_id = ? AND other_user_id = ?`, userID, otherID)
	return own.State == Block, err
}

// CopyPeer stores the other user's side, as of their outbox row, in the
// user's contact_peer, unless a later row of theirs is already there, and
// reports whether the stored copy is this row's.
func CopyPeer(ctx context.Context, db *sql.DB, userID, otherID ids.UUID, side string, outboxID uint64, at time.Time) (bool, error) {
	if _, err := db.ExecContext(ctx, `
		INSERT INTO contact_peer (user_id, other_user_id, state, source_outbox_id, updated_at) VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			state = IF(VALUES(source_outbox_id) > source_outbox_id, VALUES(state), state),
			updated_at = IF(VALUES(source_outbox_id) > source_outbox_id, VALUES(updated_at), updated_at),
			source_outbox_id = GREATEST(source_outbox_id, VALUES(source_outbox_id))`,
		userID, otherID, side, outboxID, at); err != nil {
		return false, err
	}
	var stored uint64
	err := db.QueryRowContext(ctx, `
		SELECT source_outbox_id FROM contact_peer WHERE user_id = ? AND other_user_id = ?`,
		userID, otherID).Scan(&stored)
	return stored == outboxID, err
}

// EndRequest ends the user's request toward the other, as RemoveContact by
// the user would, when there is one: the fan-out calls it once the other's
// side ended or became a block. A deleted user is left as they are.
func EndRequest(ctx context.Context, db *sql.DB, userID, otherID ids.UUID) error {
	own, err := readSide(ctx, db, `
		SELECT state, updated_at FROM contact WHERE user_id = ? AND other_user_id = ?`, userID, otherID)
	if err != nil || own.State != Request {
		return err
	}
	err = change(ctx, db, userID, otherID, func(own string) (string, bool) {
		return "", own == Request
	})
	if apierror.Code(err) == pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED {
		return nil
	}
	return err
}

// change runs an edit of the user's side toward the other in one
// transaction under the lock on the user row, which every change of the
// user's sides takes. The edit gets the side as it is and returns the side
// after (empty for none) and whether to write it, with the change in the
// user's outbox.
func change(ctx context.Context, db *sql.DB, userID, otherID ids.UUID, edit func(own string) (string, bool)) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var lastID uint64
	var deletedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT last_outbox_id, deleted_at FROM user WHERE user_id = ? FOR UPDATE`,
		userID).Scan(&lastID, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) || err == nil && deletedAt.Valid {
		return apierror.Unauthenticated("the account is deleted")
	}
	if err != nil {
		return err
	}
	var own string
	err = tx.QueryRowContext(ctx, `
		SELECT state FROM contact WHERE user_id = ? AND other_user_id = ? FOR UPDATE`,
		userID, otherID).Scan(&own)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	after, write := edit(own)
	if !write {
		return tx.Commit()
	}
	now := vtdb.Now()
	if after == "" {
		_, err = tx.ExecContext(ctx, `
			DELETE FROM contact WHERE user_id = ? AND other_user_id = ?`, userID, otherID)
	} else if after != own {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO contact (user_id, other_user_id, state, updated_at) VALUES (?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE state = VALUES(state), updated_at = VALUES(updated_at)`,
			userID, otherID, after, now)
	}
	if err != nil {
		return err
	}
	payload, err := proto.Marshal(&pb.Event{
		OccurredAt: timestamppb.New(now),
		Body: &pb.Event_Contact{Contact: &pb.ContactEvent{Contact: &pb.Contact{
			User: &pb.User{UserId: otherID.Bytes()}, State: payloadState(after),
		}}},
	})
	if err != nil {
		return err
	}
	lastID++
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_outbox (user_id, outbox_id, kind, payload, created_at) VALUES (?, ?, 'contact', ?, ?)`,
		userID, lastID, payload, now); err != nil {
		return err
	}
	// As with the other outboxes: the binlog keeps the insert.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM user_outbox WHERE user_id = ? AND outbox_id = ?`, userID, lastID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE user SET last_outbox_id = ? WHERE user_id = ?`, lastID, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// placeholders is "?, ?, ..." for n values.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
