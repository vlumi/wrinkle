package accounts

import (
	"context"
	"database/sql"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// outbox writes a user's changes to user_outbox in the transaction that
// makes them, under the lock on the user row, which holds the id counter.
type outbox struct {
	tx     *sql.Tx
	userID ids.UUID
	lastID uint64
	now    time.Time
}

// lockOutbox locks the user row and reads the counter.
func lockOutbox(ctx context.Context, tx *sql.Tx, userID ids.UUID, now time.Time) (*outbox, error) {
	o := &outbox{tx: tx, userID: userID, now: now}
	if err := tx.QueryRowContext(ctx, `
		SELECT last_outbox_id FROM user WHERE user_id = ? FOR UPDATE`, userID).Scan(&o.lastID); err != nil {
		return nil, err
	}
	return o, nil
}

// add writes one row; kind names the event body.
func (o *outbox) add(ctx context.Context, kind string, event *pb.Event) error {
	event.OccurredAt = timestamppb.New(o.now)
	payload, err := proto.Marshal(event)
	if err != nil {
		return err
	}
	o.lastID++
	if _, err := o.tx.ExecContext(ctx, `
		INSERT INTO user_outbox (user_id, outbox_id, kind, payload, created_at) VALUES (?, ?, ?, ?, ?)`,
		o.userID, o.lastID, kind, payload, o.now); err != nil {
		return err
	}
	// As with the conversation outbox: the binlog keeps the insert.
	_, err = o.tx.ExecContext(ctx, `
		DELETE FROM user_outbox WHERE user_id = ? AND outbox_id = ?`, o.userID, o.lastID)
	return err
}

// save stores the counter.
func (o *outbox) save(ctx context.Context) error {
	_, err := o.tx.ExecContext(ctx, `
		UPDATE user SET last_outbox_id = ? WHERE user_id = ?`, o.lastID, o.userID)
	return err
}

// addDevice writes a DeviceEvent with the device as it is now in the
// transaction.
func (o *outbox) addDevice(ctx context.Context, deviceID uint64, signedOut bool) (*pb.Device, error) {
	device, err := loadDevice(ctx, o.tx, o.userID, deviceID)
	if err != nil {
		return nil, err
	}
	return device, o.add(ctx, "device", &pb.Event{
		Body: &pb.Event_Device{Device: &pb.DeviceEvent{Device: device, SignedOut: signedOut, UserId: o.userID.Bytes()}},
	})
}
