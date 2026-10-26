package conversation

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	maxWrappedKeys = 500
	wrappedBytes   = 60
)

// DeviceKeys reads devices' public keys from the user keyspace.
type DeviceKeys interface {
	// DeviceKey returns nil when the device has no key.
	DeviceKey(ctx context.Context, userID ids.UUID, deviceID uint64) ([]byte, error)
	// DeviceKeys returns the signed-in devices with keys of the users.
	DeviceKeys(ctx context.Context, userIDs [][]byte) ([]*pb.UserDevices, error)
}

type wrap struct {
	userID   ids.UUID
	deviceID uint64
	wrapped  []byte
}

// PublishKeys stores keys of a conversation wrapped by the requesting
// device. Epoch key_epoch + 1 starts a new epoch, for current members; the
// current epoch takes more current members' devices; an older one only the
// user's own devices. A request that wraps for the requesting device
// itself makes the epoch: for an existing epoch it fails unless this device
// made it, so of two devices publishing the same epoch, the later fails.
func (s *Service) PublishKeys(ctx context.Context, userID ids.UUID, deviceID uint64, conversationID ids.UUID, epoch uint32, keys []*pb.WrappedKey) error {
	if len(keys) == 0 || len(keys) > maxWrappedKeys {
		return apierror.InvalidArgument("keys must have 1 to %d wrapped keys", maxWrappedKeys)
	}
	wraps := make([]wrap, len(keys))
	self, others := false, false
	for i, k := range keys {
		id, ok := ids.Parse(k.GetRecipientUserId())
		if !ok {
			return apierror.InvalidArgument("recipient_user_id must be 16 bytes")
		}
		if k.GetRecipientDeviceId() == 0 {
			return apierror.InvalidArgument("recipient_device_id must be set")
		}
		if len(k.GetWrapped()) != wrappedBytes {
			return apierror.InvalidArgument("wrapped must be %d bytes", wrappedBytes)
		}
		wraps[i] = wrap{id, k.GetRecipientDeviceId(), k.GetWrapped()}
		self = self || (id == userID && k.GetRecipientDeviceId() == deviceID)
		others = others || id != userID
	}
	// Across a block, either way, no keys go to the other side; the user's
	// own devices may still share the history.
	if others {
		if err := s.checkBlocks(ctx, userID, conversationID); err != nil {
			return err
		}
	}
	senderKey, err := s.keys.DeviceKey(ctx, userID, deviceID)
	if err != nil {
		return err
	}
	if err := s.recipientsHaveKeys(ctx, wraps); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := member(ctx, tx, conversationID, userID)
	if err != nil {
		return err
	}
	if senderKey == nil {
		return apierror.FailedPrecondition("this device has no key; set one first")
	}
	notNext := apierror.FailedPrecondition("epoch %d is not next: the current one is %d", epoch, st.keyEpoch)
	switch {
	case epoch == st.keyEpoch+1:
		if err := recipientsAreMembers(st, wraps); err != nil {
			return err
		}
	case epoch >= 1 && epoch <= st.keyEpoch:
		if epoch == st.keyEpoch {
			if err := recipientsAreMembers(st, wraps); err != nil {
				return err
			}
		} else {
			for _, w := range wraps {
				if w.userID != userID {
					return apierror.PermissionDenied("keys of an earlier epoch go only to the user's own devices")
				}
			}
		}
		if self {
			made, err := madeBy(ctx, tx, conversationID, epoch, userID, deviceID)
			if err != nil {
				return err
			}
			if !made {
				return notNext
			}
		}
	default:
		return notNext
	}
	p := publication{conversationID: conversationID, epoch: epoch, userID: userID, deviceID: deviceID, senderKey: senderKey, wraps: wraps}
	if err := p.insert(ctx, tx); err != nil {
		return err
	}
	if epoch > st.keyEpoch {
		if _, err := tx.ExecContext(ctx, `
			UPDATE conversation SET key_epoch = ?, rotate = FALSE WHERE conversation_id = ?`,
			epoch, conversationID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// recipientsHaveKeys checks that every wrap is for a signed-in device with
// a key, so the epoch holds rows for real devices only.
func (s *Service) recipientsHaveKeys(ctx context.Context, wraps []wrap) error {
	var users [][]byte
	seen := map[ids.UUID]bool{}
	for _, w := range wraps {
		if !seen[w.userID] {
			seen[w.userID] = true
			users = append(users, w.userID.Bytes())
		}
	}
	// More users than a conversation's members is refused there.
	found, err := s.keys.DeviceKeys(ctx, users)
	if err != nil {
		return err
	}
	type device struct {
		userID ids.UUID
		id     uint64
	}
	keyed := map[device]bool{}
	for _, u := range found {
		userID, _ := ids.Parse(u.GetUserId())
		for _, d := range u.GetDevices() {
			keyed[device{userID, d.GetDeviceId()}] = true
		}
	}
	for _, w := range wraps {
		if !keyed[device{w.userID, w.deviceID}] {
			return apierror.InvalidArgument("device %d of %s is not a signed-in device with a key", w.deviceID, w.userID)
		}
	}
	return nil
}

func recipientsAreMembers(st state, wraps []wrap) error {
	for _, w := range wraps {
		if !st.has(w.userID) {
			return apierror.InvalidArgument("recipient %s is not a member", w.userID)
		}
	}
	return nil
}

// madeBy reports whether the device holds the epoch's key wrapped by
// itself, that is, it made the epoch.
func madeBy(ctx context.Context, tx *sql.Tx, conversationID ids.UUID, epoch uint32, userID ids.UUID, deviceID uint64) (bool, error) {
	var senderUserID ids.UUID
	var senderDeviceID uint64
	err := tx.QueryRowContext(ctx, `
		SELECT sender_user_id, sender_device_id FROM conversation_key
		WHERE conversation_id = ? AND recipient_user_id = ? AND recipient_device_id = ? AND epoch = ?`,
		conversationID, userID, deviceID, epoch).Scan(&senderUserID, &senderDeviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return senderUserID == userID && senderDeviceID == deviceID, nil
}

// publication is one device's wraps of an epoch's key.
type publication struct {
	conversationID ids.UUID
	epoch          uint32
	// The publishing device and its key.
	userID    ids.UUID
	deviceID  uint64
	senderKey []byte
	wraps     []wrap
}

// insert stores the wraps, keeping those a recipient already has.
func (p publication) insert(ctx context.Context, tx *sql.Tx) error {
	now := vtdb.Now()
	values := make([]string, len(p.wraps))
	args := make([]any, 0, 9*len(p.wraps))
	for i, w := range p.wraps {
		values[i] = "(?, ?, ?, ?, ?, ?, ?, ?, ?)"
		args = append(args, p.conversationID, w.userID, w.deviceID, p.epoch, p.userID, p.deviceID, p.senderKey, w.wrapped, now)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO conversation_key
			(conversation_id, recipient_user_id, recipient_device_id, epoch, sender_user_id, sender_device_id, sender_public_key, wrapped, created_at)
		VALUES `+strings.Join(values, ", "), args...)
	return err
}

// Keys returns the conversation's key state and the keys wrapped for the
// device in epochs after afterEpoch, oldest first. Former members may ask:
// no wraps reach them after they leave, and they get none of the current
// membership.
func (s *Service) Keys(ctx context.Context, userID ids.UUID, deviceID uint64, conversationID ids.UUID, afterEpoch uint32) (*pb.GetConversationKeysResponse, error) {
	st, err := load(ctx, s.db, selectState, conversationID)
	if err != nil {
		return nil, err
	}
	var leftSeq sql.NullInt64
	err = s.db.QueryRowContext(ctx, `
		SELECT left_seq FROM conversation_member WHERE conversation_id = ? AND user_id = ?`,
		conversationID, userID).Scan(&leftSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.PermissionDenied("user %s is not in conversation %s", userID, conversationID)
	}
	if err != nil {
		return nil, err
	}
	current := !leftSeq.Valid
	resp := &pb.GetConversationKeysResponse{CurrentEpoch: st.keyEpoch, Rotate: current && st.rotate}
	if current && st.keyEpoch > 0 {
		rows, err := s.db.QueryContext(ctx, `
			SELECT recipient_user_id, recipient_device_id FROM conversation_key
			WHERE conversation_id = ? AND epoch = ? ORDER BY recipient_user_id, recipient_device_id`,
			conversationID, st.keyEpoch)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			r := &pb.KeyRecipient{}
			if err := rows.Scan(&r.UserId, &r.DeviceId); err != nil {
				return nil, err
			}
			resp.CurrentRecipients = append(resp.CurrentRecipients, r)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT epoch, sender_user_id, sender_device_id, sender_public_key, wrapped FROM conversation_key
		WHERE conversation_id = ? AND recipient_user_id = ? AND recipient_device_id = ? AND epoch > ?
		ORDER BY epoch`,
		conversationID, userID, deviceID, afterEpoch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		k := &pb.ReceivedKey{}
		if err := rows.Scan(&k.Epoch, &k.SenderUserId, &k.SenderDeviceId, &k.SenderPublicKey, &k.Wrapped); err != nil {
			return nil, err
		}
		resp.Keys = append(resp.Keys, k)
	}
	return resp, rows.Err()
}
