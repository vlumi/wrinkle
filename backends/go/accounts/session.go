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

func wrongPassword() error { return apierror.Unauthenticated("wrong password") }

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
	user := &pb.User{UserId: token.userID.Bytes()}
	var avatar []byte
	var deletedAt sql.NullTime
	if err := s.db.QueryRowContext(ctx, `
		SELECT name, icon, avatar, deleted_at, log_head FROM user WHERE user_id = ?`,
		token.userID).Scan(&user.Name, &user.Icon, &avatar, &deletedAt, &resp.LogHead); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, invalidToken()
		}
		return nil, err
	}
	if deletedAt.Valid {
		return nil, invalidToken()
	}
	user.Avatar = avatar
	resp.User = user
	if _, err := s.db.ExecContext(ctx, `
		UPDATE device SET last_seen_at = ? WHERE user_id = ? AND device_id = ?`,
		s.now(), token.userID, token.deviceID); err != nil {
		return nil, err
	}
	return resp, nil
}

// verifyCurrent checks the user's password, failing with UNAUTHENTICATED
// when it is wrong; it returns the hash it checked against.
func (s *Service) verifyCurrent(ctx context.Context, userID ids.UUID, password string) (string, error) {
	var stored string
	if err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM user WHERE user_id = ?`, userID).Scan(&stored); err != nil {
		return "", err
	}
	hash, candidate := stored, password
	if !validPassword(password) || stored == "" {
		// A deleted account has no hash; take as long as a wrong password.
		hash, candidate = dummyHash(), ""
	}
	ok, err := verifyPassword(ctx, candidate, hash)
	if err != nil {
		return "", err
	}
	if !ok || hash != stored {
		return "", wrongPassword()
	}
	return stored, nil
}

// ChangePassword replaces the password and signs out every other device of
// the user; their connections are closed when fan-out delivers the sign-out.
func (s *Service) ChangePassword(ctx context.Context, userID ids.UUID, deviceID uint64, req *pb.ChangePasswordRequest) error {
	verified, err := s.verifyCurrent(ctx, userID, req.GetCurrentPassword())
	if err != nil {
		return err
	}
	if err := checkPassword(req.GetNewPassword()); err != nil {
		return err
	}
	newHash, err := hashPassword(ctx, req.GetNewPassword())
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o, err := lockOutbox(ctx, tx, userID, s.now())
	if err != nil {
		return err
	}
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT password_hash FROM user WHERE user_id = ?`, userID).Scan(&current); err != nil {
		return err
	}
	if current != verified {
		// Changed concurrently: what was checked is no longer the password.
		return wrongPassword()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE user SET password_hash = ? WHERE user_id = ?`, newHash, userID); err != nil {
		return err
	}
	others, err := signedInDevices(ctx, tx, userID)
	if err != nil {
		return err
	}
	for _, other := range others {
		if other == deviceID {
			continue
		}
		if err := signOutDevice(ctx, o, other); err != nil {
			return err
		}
	}
	if err := o.save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

// signedInDevices lists the user's devices with a token, locking them.
func signedInDevices(ctx context.Context, tx *sql.Tx, userID ids.UUID) ([]uint64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT device_id FROM device WHERE user_id = ? AND token_hash IS NOT NULL FOR UPDATE`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []uint64
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		list = append(list, id)
	}
	return list, rows.Err()
}
