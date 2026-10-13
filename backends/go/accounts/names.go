package accounts

import (
	"context"
	"database/sql"
	"errors"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
)

const claimAttempts = 3

// A name claim in user_name is valid while its owner's row still has that
// name. Names are compared by MySQL, under the claim table's collation,
// never in Go.

// owns reports whether the user's row has the name, ignoring case.
func (s *Service) owns(ctx context.Context, userID ids.UUID, name string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM user WHERE user_id = ? AND name = ? COLLATE utf8mb4_0900_as_ci`,
		userID, name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// claimOwner returns the user holding the claim on the name, if any.
func (s *Service) claimOwner(ctx context.Context, name string) (ids.UUID, bool, error) {
	var owner ids.UUID
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM user_name WHERE name = ?`, name).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return owner, false, nil
	}
	return owner, err == nil, err
}

// claim takes the name for the user. A claim the user already holds is
// fine; a valid claim of another user fails with ALREADY_EXISTS; a stale
// one is taken over.
func (s *Service) claim(ctx context.Context, name string, userID ids.UUID) error {
	for range claimAttempts {
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO user_name (name, user_id, created_at) VALUES (?, ?, ?)`,
			name, userID, vtdb.Now())
		if err == nil {
			return nil
		}
		if !vtdb.IsDuplicate(err) {
			return err
		}
		owner, found, err := s.claimOwner(ctx, name)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if owner == userID {
			return nil
		}
		valid, err := s.owns(ctx, owner, name)
		if err != nil {
			return err
		}
		if valid {
			return apierror.AlreadyExists("the name is taken")
		}
		if _, err := s.db.ExecContext(ctx, `
			DELETE FROM user_name WHERE name = ? AND user_id = ?`, name, owner); err != nil {
			return err
		}
	}
	return apierror.Unavailable("the name is contended; try again")
}

// Resolve finds the user with the name, ignoring case; ok is false when no
// user has it.
func (s *Service) Resolve(ctx context.Context, name string) (ids.UUID, bool, error) {
	owner, found, err := s.claimOwner(ctx, name)
	if err != nil || !found {
		return owner, false, err
	}
	valid, err := s.owns(ctx, owner, name)
	if err != nil || !valid {
		return ids.UUID{}, false, err
	}
	return owner, true, nil
}
