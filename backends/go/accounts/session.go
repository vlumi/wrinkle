package accounts

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func invalidToken() error { return apierror.Unauthenticated("invalid device token") }

// Connect checks the device token and returns the user, the device's acked
// cursor and the log head.
func (s *Service) Connect(ctx context.Context, req *pb.ConnectRequest) (*pb.ConnectResponse, error) {
	token, ok := parseToken(req.GetDeviceToken())
	if !ok {
		return nil, invalidToken()
	}
	resp := &pb.ConnectResponse{DeviceId: token.deviceID}
	var stored []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT token_hash, acked_position FROM device WHERE user_id = ? AND device_id = ?`,
		token.userID, token.deviceID).Scan(&stored, &resp.DeviceCursor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, invalidToken()
	}
	if err != nil {
		return nil, err
	}
	if stored == nil || subtle.ConstantTimeCompare(stored, token.hash()) != 1 {
		return nil, invalidToken()
	}
	resp.User = &pb.User{UserId: token.userID.Bytes()}
	if err := s.db.QueryRowContext(ctx, `
		SELECT name, icon, log_head FROM user WHERE user_id = ?`,
		token.userID).Scan(&resp.User.Name, &resp.User.Icon, &resp.LogHead); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, invalidToken()
		}
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE device SET last_seen_at = ? WHERE user_id = ? AND device_id = ?`,
		s.now(), token.userID, token.deviceID); err != nil {
		return nil, err
	}
	return resp, nil
}

// ChangePassword replaces the password and signs out every other device of
// the user. Their connections already open keep running until they
// reconnect.
func (s *Service) ChangePassword(ctx context.Context, userID ids.UUID, deviceID uint64, req *pb.ChangePasswordRequest) error {
	var hash string
	if err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM user WHERE user_id = ?`, userID).Scan(&hash); err != nil {
		return err
	}
	current := req.GetCurrentPassword()
	if !validPassword(current) {
		current, hash = "", dummyHash()
	}
	ok, err := verifyPassword(ctx, current, hash)
	if err != nil {
		return err
	}
	if !ok || !validPassword(req.GetCurrentPassword()) {
		return apierror.Unauthenticated("wrong password")
	}
	if err := checkPassword(req.GetNewPassword()); err != nil {
		return err
	}
	newHash, err := hashPassword(ctx, req.GetNewPassword())
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE user SET password_hash = ? WHERE user_id = ?`, newHash, userID); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE device SET token_hash = NULL, push_service = NULL, push_token = NULL
		WHERE user_id = ? AND device_id <> ?`, userID, deviceID)
	return err
}

// SignOut invalidates the device's token and removes its push registration.
func (s *Service) SignOut(ctx context.Context, userID ids.UUID, deviceID uint64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE device SET token_hash = NULL, push_service = NULL, push_token = NULL
		WHERE user_id = ? AND device_id = ?`, userID, deviceID)
	return err
}
