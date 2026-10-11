// Package accounts is the account service: registration, sign-in with a
// password, device tokens, profiles and the name claims that keep names
// unique, against the user keyspace.
package accounts

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/blobs"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	maxNameBytes       = 64
	maxDeviceNameBytes = 64
	minPasswordBytes   = 8
	maxPasswordBytes   = 256
)

// The standard icons; empty is none.
var icons = []string{"cat", "dog", "fox", "owl", "bear", "frog", "panda", "rabbit", "tiger", "penguin", "octopus", "unicorn"}

type Service struct {
	db    *sql.DB
	blobs *blobs.Store
	now   func() time.Time
}

func New(db *sql.DB, blobs *blobs.Store) *Service {
	return &Service{db: db, blobs: blobs, now: func() time.Time { return time.Now().UTC() }}
}

// ValidName reports whether a user name is acceptable: valid UTF-8 of 1 to
// 64 bytes, no control characters, no white space at either end.
func ValidName(name string) bool {
	if name == "" || len(name) > maxNameBytes || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.Is(unicode.Cc, r) {
			return false
		}
	}
	first, _ := utf8.DecodeRuneInString(name)
	last, _ := utf8.DecodeLastRuneInString(name)
	return !unicode.Is(unicode.White_Space, first) && !unicode.Is(unicode.White_Space, last)
}

func validPassword(password string) bool {
	return len(password) >= minPasswordBytes && len(password) <= maxPasswordBytes
}

func validIcon(icon string) bool {
	return icon == "" || slices.Contains(icons, icon)
}

func checkName(name string) error {
	if !ValidName(name) {
		return apierror.InvalidArgument("name must be 1 to %d bytes of UTF-8, without control characters or white space at either end", maxNameBytes)
	}
	return nil
}

func checkPassword(password string) error {
	if !validPassword(password) {
		return apierror.InvalidArgument("password must be %d to %d bytes", minPasswordBytes, maxPasswordBytes)
	}
	return nil
}

func checkDeviceName(name string) error {
	if len(name) > maxDeviceNameBytes || !utf8.ValidString(name) {
		return apierror.InvalidArgument("device_name must be UTF-8 of at most %d bytes", maxDeviceNameBytes)
	}
	return nil
}

// Profile reads a user's profile; ok is false when there is no such user.
func (s *Service) Profile(ctx context.Context, userID ids.UUID) (*pb.User, bool, error) {
	user := &pb.User{UserId: userID.Bytes()}
	var deletedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT name, icon, avatar, deleted_at FROM user WHERE user_id = ?`, userID).
		Scan(&user.Name, &user.Icon, &user.Avatar, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	user.Deleted = deletedAt.Valid
	return user, true, nil
}
