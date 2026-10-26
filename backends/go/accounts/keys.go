package accounts

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	publicKeyBytes = 32
	maxKeyUsers    = 256
)

// SetDeviceKey stores the device's public key, once; the same key again
// does nothing. Partners and group co-members learn it from the
// DeviceEvent.
func (s *Service) SetDeviceKey(ctx context.Context, userID ids.UUID, deviceID uint64, key []byte) error {
	if len(key) != publicKeyBytes {
		return apierror.InvalidArgument("public_key must be %d bytes", publicKeyBytes)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	o, err := s.lockSignedIn(ctx, tx, userID, deviceID)
	if err != nil {
		return err
	}
	var current []byte
	if err := tx.QueryRowContext(ctx, `
		SELECT public_key FROM device WHERE user_id = ? AND device_id = ?`,
		userID, deviceID).Scan(&current); err != nil {
		return err
	}
	switch {
	case bytes.Equal(current, key):
		return nil
	case current != nil:
		return apierror.FailedPrecondition(pb.ErrorReason_ERROR_REASON_DEVICE_KEY_SET, "the device has a different key; sign in again as a new device")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE device SET public_key = ? WHERE user_id = ? AND device_id = ?`,
		key, userID, deviceID); err != nil {
		return err
	}
	if _, err := o.addDevice(ctx, deviceID, false); err != nil {
		return err
	}
	if err := o.save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

// DeviceKey returns the public key of one of the user's devices, nil when
// it has none.
func (s *Service) DeviceKey(ctx context.Context, userID ids.UUID, deviceID uint64) ([]byte, error) {
	var key []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT public_key FROM device WHERE user_id = ? AND device_id = ?`,
		userID, deviceID).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return key, err
}

// DeviceKeys returns the signed-in devices with keys of the users, in the
// order asked, each device with only its id and key, by device id. Unknown
// and deleted users are left out.
func (s *Service) DeviceKeys(ctx context.Context, userIDs [][]byte) ([]*pb.UserDevices, error) {
	if len(userIDs) == 0 || len(userIDs) > maxKeyUsers {
		return nil, apierror.InvalidArgument("user_ids must have 1 to %d ids", maxKeyUsers)
	}
	var order []ids.UUID
	seen := map[ids.UUID]bool{}
	for _, b := range userIDs {
		id, ok := ids.Parse(b)
		if !ok {
			return nil, apierror.InvalidArgument("user ids must be 16 bytes")
		}
		if !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	args := make([]any, len(order))
	for i, id := range order {
		args[i] = id
	}
	in := `(?` + strings.Repeat(", ?", len(order)-1) + `)`
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id FROM user WHERE user_id IN `+in+` AND deleted_at IS NULL`, args...)
	if err != nil {
		return nil, err
	}
	liveIDs, err := vtdb.Collect(rows, func(rows *sql.Rows) (id ids.UUID, err error) { return id, rows.Scan(&id) })
	if err != nil {
		return nil, err
	}
	live := make(map[ids.UUID]*pb.UserDevices, len(liveIDs))
	for _, id := range liveIDs {
		live[id] = &pb.UserDevices{UserId: id.Bytes()}
	}
	rows, err = s.db.QueryContext(ctx, `
		SELECT user_id, device_id, public_key FROM device
		WHERE user_id IN `+in+` AND token_hash IS NOT NULL AND public_key IS NOT NULL`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id ids.UUID
		d := &pb.Device{}
		if err := rows.Scan(&id, &d.DeviceId, &d.PublicKey); err != nil {
			return nil, err
		}
		if u := live[id]; u != nil {
			u.Devices = append(u.Devices, d)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	users := make([]*pb.UserDevices, 0, len(live))
	for _, id := range order {
		if u := live[id]; u != nil {
			slices.SortFunc(u.Devices, func(a, b *pb.Device) int { return cmp.Compare(a.DeviceId, b.DeviceId) })
			users = append(users, u)
		}
	}
	return users, nil
}
