package accounts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"sync"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const wrongCredentials = "wrong name or password"

// dummyHash is verified against when there is no account to check, so an
// unknown name takes as long as a wrong password.
var dummyHash = sync.OnceValue(func() string {
	salt := make([]byte, argonSaltLen)
	_, _ = rand.Read(salt)
	return encodeHash(rand.Text(), salt, argonMemory, argonTime, argonThreads)
})

// Register creates an account and a device for it. The user row comes
// before the name claim, so an interruption leaves at worst a user nobody
// can reach, never a claim without its user.
func (s *Service) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.DeviceCredentials, error) {
	if err := checkName(req.GetName()); err != nil {
		return nil, err
	}
	if err := checkPassword(req.GetPassword()); err != nil {
		return nil, err
	}
	if err := checkDeviceName(req.GetDeviceName()); err != nil {
		return nil, err
	}
	// A taken name fails before the hash's cost; the claim below decides.
	if _, taken, err := s.Resolve(ctx, req.GetName()); err != nil {
		return nil, err
	} else if taken {
		return nil, apierror.AlreadyExists("the name is taken")
	}
	hash, err := hashPassword(ctx, req.GetPassword())
	if err != nil {
		return nil, err
	}
	userID := ids.New()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO user (user_id, name, icon, password_hash, log_head, last_outbox_id, created_at)
		VALUES (?, ?, '', ?, 0, 0, ?)`,
		userID, req.GetName(), hash, vtdb.Now()); err != nil {
		return nil, err
	}
	if err := s.claim(ctx, req.GetName(), userID); err != nil {
		if _, derr := s.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM user WHERE user_id = ?`, userID); derr != nil {
			err = errors.Join(err, derr)
		}
		return nil, err
	}
	return s.newDevice(ctx, userID, req.GetDeviceName())
}

// SignIn checks the password and creates a device. A wrong name and a wrong
// password fail alike, in about the same time.
func (s *Service) SignIn(ctx context.Context, req *pb.SignInRequest) (*pb.DeviceCredentials, error) {
	if err := checkDeviceName(req.GetDeviceName()); err != nil {
		return nil, err
	}
	userID, hash, err := s.passwordOf(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	password := req.GetPassword()
	if hash == "" || !validPassword(password) {
		hash = dummyHash()
		password = ""
	}
	ok, err := verifyPassword(ctx, password, hash)
	if err != nil {
		return nil, err
	}
	if !ok || userID.IsZero() || !validPassword(req.GetPassword()) {
		return nil, apierror.Unauthenticated(wrongCredentials)
	}
	return s.newDevice(ctx, userID, req.GetDeviceName())
}

// passwordOf returns the user holding a valid claim on the name and their
// password hash; both are empty when there is none.
func (s *Service) passwordOf(ctx context.Context, name string) (ids.UUID, string, error) {
	if !ValidName(name) {
		return ids.UUID{}, "", nil
	}
	owner, found, err := s.claimOwner(ctx, name)
	if err != nil || !found {
		return ids.UUID{}, "", err
	}
	var hash string
	err = s.db.QueryRowContext(ctx, `
		SELECT password_hash FROM user WHERE user_id = ? AND name = ? COLLATE utf8mb4_0900_as_ci`,
		owner, name).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ids.UUID{}, "", nil
	}
	if err != nil {
		return ids.UUID{}, "", err
	}
	return owner, hash, nil
}

// newDevice signs a device in, with its DeviceEvent for the user's other
// devices.
func (s *Service) newDevice(ctx context.Context, userID ids.UUID, name string) (*pb.DeviceCredentials, error) {
	token := newToken(userID, ids.Random())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	o, err := lockOutbox(ctx, tx, userID, vtdb.Now())
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO device (user_id, device_id, name, token_hash, acked_position, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, 0, ?, ?)`,
		userID, token.deviceID, name, token.hash(), o.now, o.now); err != nil {
		return nil, err
	}
	if _, err := o.addDevice(ctx, token.deviceID, false); err != nil {
		return nil, err
	}
	if err := o.save(ctx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &pb.DeviceCredentials{UserId: userID.Bytes(), DeviceId: token.deviceID, DeviceToken: token.String()}, nil
}
