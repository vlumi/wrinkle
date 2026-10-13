package accounts

import (
	"context"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// DeleteAccount checks the password, has leaveGroups take the user out of
// their groups, then empties the user row, keeping it as a tombstone, signs
// out every device and writes a ProfileEvent of the deleted user, all in
// one transaction. Fan-out tells the partners, closes
// the connections and removes the user's log, list and devices. The name
// claim goes last; one left behind is stale, as the row no longer has the
// name.
func (s *Service) DeleteAccount(ctx context.Context, userID ids.UUID, req *pb.DeleteAccountRequest, leaveGroups func(context.Context) error) error {
	verified, err := s.verifyCurrent(ctx, userID, req.GetPassword())
	if err != nil {
		return err
	}
	if err := leaveGroups(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o, err := lockOutbox(ctx, tx, userID, vtdb.Now())
	if err != nil {
		return err
	}
	var name, current string
	if err := tx.QueryRowContext(ctx, `
		SELECT name, password_hash FROM user WHERE user_id = ?`, userID).Scan(&name, &current); err != nil {
		return err
	}
	if current != verified {
		return wrongPassword()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE user SET name = '', icon = '', avatar = NULL, password_hash = '', deleted_at = ?
		WHERE user_id = ?`, o.now, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE device SET token_hash = NULL, push_service = NULL, push_token = NULL
		WHERE user_id = ?`, userID); err != nil {
		return err
	}
	deleted := &pb.User{UserId: userID.Bytes(), Deleted: true}
	if err := o.add(ctx, "profile", &pb.Event{Body: &pb.Event_Profile{Profile: &pb.ProfileEvent{User: deleted}}}); err != nil {
		return err
	}
	if err := o.save(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM user_name WHERE name = ? AND user_id = ?`, name, userID); err != nil {
		s.log.Warn("releasing the deleted account's name failed", "user", userID, "err", err)
	}
	return nil
}
