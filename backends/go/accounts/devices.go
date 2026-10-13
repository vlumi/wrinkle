package accounts

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const deviceColumns = `SELECT device_id, name, created_at, last_seen_at, push_token IS NOT NULL, public_key FROM device`

func scanDevice(scan func(...any) error) (*pb.Device, error) {
	d := &pb.Device{}
	var created, seen time.Time
	if err := scan(&d.DeviceId, &d.Name, &created, &seen, &d.Push, &d.PublicKey); err != nil {
		return nil, err
	}
	d.CreatedAt = timestamppb.New(created)
	d.LastSeenAt = timestamppb.New(seen)
	return d, nil
}

func loadDevice(ctx context.Context, q vtdb.RowQuerier, userID ids.UUID, deviceID uint64) (*pb.Device, error) {
	return scanDevice(q.QueryRowContext(ctx, deviceColumns+` WHERE user_id = ? AND device_id = ?`, userID, deviceID).Scan)
}

// ListDevices returns the user's signed-in devices, oldest first.
func (s *Service) ListDevices(ctx context.Context, userID ids.UUID) ([]*pb.Device, error) {
	rows, err := s.db.QueryContext(ctx, deviceColumns+`
		WHERE user_id = ? AND token_hash IS NOT NULL ORDER BY created_at, device_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*pb.Device
	for rows.Next() {
		d, err := scanDevice(rows.Scan)
		if err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, rows.Err()
}

func noSuchDevice() error { return apierror.NotFound("no such signed-in device") }

// lockSignedIn locks the user row for the outbox, then reports whether the
// device is the user's and signed in.
func (s *Service) lockSignedIn(ctx context.Context, tx *sql.Tx, userID ids.UUID, deviceID uint64) (*outbox, error) {
	o, err := lockOutbox(ctx, tx, userID, vtdb.Now())
	if err != nil {
		return nil, err
	}
	var one int
	err = tx.QueryRowContext(ctx, `
		SELECT 1 FROM device WHERE user_id = ? AND device_id = ? AND token_hash IS NOT NULL FOR UPDATE`,
		userID, deviceID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, noSuchDevice()
	}
	if err != nil {
		return nil, err
	}
	return o, nil
}

// RenameDevice renames one of the user's signed-in devices.
func (s *Service) RenameDevice(ctx context.Context, userID ids.UUID, req *pb.RenameDeviceRequest) (*pb.Device, error) {
	if err := checkDeviceName(req.GetName()); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	o, err := s.lockSignedIn(ctx, tx, userID, req.GetDeviceId())
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE device SET name = ? WHERE user_id = ? AND device_id = ?`,
		req.GetName(), userID, req.GetDeviceId()); err != nil {
		return nil, err
	}
	device, err := o.addDevice(ctx, req.GetDeviceId(), false)
	if err != nil {
		return nil, err
	}
	if err := o.save(ctx); err != nil {
		return nil, err
	}
	return device, tx.Commit()
}

// SignOutDevice signs out another of the user's devices.
func (s *Service) SignOutDevice(ctx context.Context, userID ids.UUID, deviceID, target uint64) error {
	if target == deviceID {
		return apierror.InvalidArgument("this is the requesting device; use SignOut")
	}
	return s.signOut(ctx, userID, target)
}

// SignOut invalidates the device's token and removes its push registration.
func (s *Service) SignOut(ctx context.Context, userID ids.UUID, deviceID uint64) error {
	err := s.signOut(ctx, userID, deviceID)
	if apierror.Code(err) == pb.ErrorCode_ERROR_CODE_NOT_FOUND {
		// Signed out already, by another device.
		return nil
	}
	return err
}

func (s *Service) signOut(ctx context.Context, userID ids.UUID, deviceID uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o, err := s.lockSignedIn(ctx, tx, userID, deviceID)
	if err != nil {
		return err
	}
	if err := signOutDevice(ctx, o, deviceID); err != nil {
		return err
	}
	if err := o.save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

// signOutDevice clears the device's token and push registration and
// writes its DeviceEvent.
func signOutDevice(ctx context.Context, o *outbox, deviceID uint64) error {
	if _, err := o.tx.ExecContext(ctx, `
		UPDATE device SET token_hash = NULL, push_service = NULL, push_token = NULL
		WHERE user_id = ? AND device_id = ?`, o.userID, deviceID); err != nil {
		return err
	}
	_, err := o.addDevice(ctx, deviceID, true)
	return err
}
