package accounts

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// UpdateProfile changes the user's name, icon, avatar or several. A new
// name is claimed first; the change and its outbox row are one transaction
// on the user's shard; the old claim is released last. An interruption
// leaves at worst a stale claim, which the next claimant takes over.
func (s *Service) UpdateProfile(ctx context.Context, userID ids.UUID, req *pb.UpdateProfileRequest) (*pb.User, error) {
	if req.Name == nil && req.Icon == nil && req.Avatar == nil {
		user, found, err := s.Profile(ctx, userID)
		if err == nil && !found {
			err = fmt.Errorf("user %s missing", userID)
		}
		return user, err
	}
	if req.Name != nil {
		if err := checkName(req.GetName()); err != nil {
			return nil, err
		}
	}
	if req.Icon != nil && !validIcon(req.GetIcon()) {
		return nil, apierror.InvalidArgument("unknown icon %q", req.GetIcon())
	}
	if req.Avatar != nil {
		if err := s.checkAvatar(ctx, userID, req.GetAvatar()); err != nil {
			return nil, err
		}
	}
	current, found, err := s.Profile(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("user %s missing", userID)
	}
	if current.GetDeleted() {
		return nil, apierror.Unauthenticated("the account is deleted")
	}
	if req.Name != nil && req.GetName() != current.GetName() {
		if err := s.claim(ctx, req.GetName(), userID); err != nil {
			return nil, err
		}
	}
	oldName, user, err := s.storeProfile(ctx, userID, req)
	if err != nil {
		return nil, err
	}
	if user.GetName() != oldName {
		// The change is committed; a claim left behind is stale and is
		// taken over by the next claimant.
		if err := s.release(ctx, userID, oldName, user.GetName()); err != nil {
			s.log.Warn("releasing the old name failed", "user", userID, "err", err)
		}
	}
	return user, nil
}

// checkAvatar accepts an empty avatar, for none, or a blob the user has
// uploaded as an avatar.
func (s *Service) checkAvatar(ctx context.Context, userID ids.UUID, avatar []byte) error {
	if len(avatar) == 0 {
		return nil
	}
	blobID, ok := ids.Parse(avatar)
	if !ok {
		return apierror.InvalidArgument("avatar must be empty or 16 bytes")
	}
	if err := avatarUpload(ctx, s.db, userID, blobID, false); err != nil {
		return err
	}
	uploaded, err := s.blobs.Exists(ctx, blobID)
	if err != nil {
		return err
	}
	if !uploaded {
		return apierror.FailedPrecondition(pb.ErrorReason_ERROR_REASON_NOT_UPLOADED, "not uploaded")
	}
	return nil
}

// avatarUpload checks that the user made an avatar upload slot for the
// blob, optionally locking it against the sweeper.
func avatarUpload(ctx context.Context, q vtdb.RowQuerier, userID, blobID ids.UUID, lock bool) error {
	query := `SELECT 1 FROM upload WHERE user_id = ? AND blob_id = ? AND purpose = 'avatar'`
	if lock {
		query += ` FOR UPDATE`
	}
	var one int
	err := q.QueryRowContext(ctx, query, userID, blobID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return apierror.FailedPrecondition(pb.ErrorReason_ERROR_REASON_NOT_YOUR_UPLOAD, "not an avatar upload of yours")
	}
	return err
}

// storeProfile applies the change and writes its outbox row in one
// transaction, returning the name before and the profile after.
func (s *Service) storeProfile(ctx context.Context, userID ids.UUID, req *pb.UpdateProfileRequest) (string, *pb.User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()

	user := &pb.User{UserId: userID.Bytes()}
	o := &outbox{tx: tx, userID: userID, now: vtdb.Now()}
	var deletedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		SELECT name, icon, avatar, deleted_at, last_outbox_id FROM user WHERE user_id = ? FOR UPDATE`,
		userID).Scan(&user.Name, &user.Icon, &user.Avatar, &deletedAt, &o.lastID); err != nil {
		return "", nil, err
	}
	if deletedAt.Valid {
		return "", nil, apierror.Unauthenticated("the account is deleted")
	}
	oldName := user.Name
	if req.Name != nil {
		user.Name = req.GetName()
	}
	if req.Icon != nil {
		user.Icon = req.GetIcon()
	}
	if req.Avatar != nil {
		if err := swapAvatar(ctx, tx, userID, user.Avatar, req.GetAvatar()); err != nil {
			return "", nil, err
		}
		user.Avatar = req.GetAvatar()
	}
	var avatar any
	if len(user.Avatar) > 0 {
		avatar = user.Avatar
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE user SET name = ?, icon = ?, avatar = ? WHERE user_id = ?`,
		user.Name, user.Icon, avatar, userID); err != nil {
		return "", nil, err
	}
	if err := o.add(ctx, "profile", &pb.Event{Body: &pb.Event_Profile{Profile: &pb.ProfileEvent{User: user}}}); err != nil {
		return "", nil, err
	}
	if err := o.save(ctx); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	return oldName, user, nil
}

// swapAvatar marks the new avatar's upload used and the old one's unused,
// for the sweeper to delete.
func swapAvatar(ctx context.Context, tx *sql.Tx, userID ids.UUID, old, avatar []byte) error {
	if len(avatar) > 0 {
		blobID, _ := ids.Parse(avatar)
		if err := avatarUpload(ctx, tx, userID, blobID, true); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE upload SET used = TRUE WHERE user_id = ? AND blob_id = ?`, userID, blobID); err != nil {
			return err
		}
	}
	if len(old) > 0 && !bytes.Equal(old, avatar) {
		if _, err := tx.ExecContext(ctx, `
			UPDATE upload SET used = FALSE WHERE user_id = ? AND blob_id = ?`, userID, old); err != nil {
			return err
		}
	}
	return nil
}

// release gives up the claim on the old name after a rename. Names that
// differ only in case share one claim, which stays as it is: claims compare
// ignoring case, and Vitess cannot change a vindex column in place.
func (s *Service) release(ctx context.Context, userID ids.UUID, oldName, newName string) error {
	var same bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT ? = ? COLLATE utf8mb4_0900_as_ci`, oldName, newName).Scan(&same); err != nil {
		return err
	}
	if same {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM user_name WHERE name = ? AND user_id = ?`, oldName, userID)
	return err
}
