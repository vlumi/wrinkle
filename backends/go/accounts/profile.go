package accounts

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// UpdateProfile changes the user's name, icon or both. A new name is
// claimed first; the change and its outbox row are one transaction on the
// user's shard; the old claim is released last. An interruption leaves at
// worst a stale claim, which the next claimant takes over.
func (s *Service) UpdateProfile(ctx context.Context, userID ids.UUID, req *pb.UpdateProfileRequest) (*pb.User, error) {
	if req.Name == nil && req.Icon == nil {
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
	current, found, err := s.Profile(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("user %s missing", userID)
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
		_ = s.release(ctx, userID, oldName, user.GetName())
	}
	return user, nil
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
	var outboxID uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT name, icon, last_outbox_id FROM user WHERE user_id = ? FOR UPDATE`,
		userID).Scan(&user.Name, &user.Icon, &outboxID); err != nil {
		return "", nil, err
	}
	oldName := user.Name
	if req.Name != nil {
		user.Name = req.GetName()
	}
	if req.Icon != nil {
		user.Icon = req.GetIcon()
	}
	outboxID++
	now := s.now()
	if _, err := tx.ExecContext(ctx, `
		UPDATE user SET name = ?, icon = ?, last_outbox_id = ? WHERE user_id = ?`,
		user.Name, user.Icon, outboxID, userID); err != nil {
		return "", nil, err
	}
	payload, err := proto.Marshal(&pb.Event{
		OccurredAt: timestamppb.New(now),
		Body:       &pb.Event_Profile{Profile: &pb.ProfileEvent{User: user}},
	})
	if err != nil {
		return "", nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_outbox (user_id, outbox_id, kind, payload, created_at) VALUES (?, ?, 'profile', ?, ?)`,
		userID, outboxID, payload, now); err != nil {
		return "", nil, err
	}
	// As with the conversation outbox: the binlog keeps the insert.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM user_outbox WHERE user_id = ? AND outbox_id = ?`, userID, outboxID); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	return oldName, user, nil
}

// release gives up the claim on the old name after a rename. When the names
// differ only in case they share one claim, which takes the new spelling.
func (s *Service) release(ctx context.Context, userID ids.UUID, oldName, newName string) error {
	var same bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT ? = ? COLLATE utf8mb4_0900_as_ci`, oldName, newName).Scan(&same); err != nil {
		return err
	}
	if same {
		_, err := s.db.ExecContext(ctx, `
			UPDATE user_name SET name = ? WHERE name = ? AND user_id = ?`, newName, oldName, userID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM user_name WHERE name = ? AND user_id = ?`, oldName, userID)
	return err
}
