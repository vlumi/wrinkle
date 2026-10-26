package wrinkleclient

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/vlumi/wrinkle/clients/go/e2ee"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	// maxPublish keeps a request within the 64 KiB frame, about 93 bytes a
	// key; maxDirectory is the contract's limit.
	maxPublish   = 500
	maxDirectory = 256
	// rotateAttempts bounds retries when other devices publish first.
	rotateAttempts = 5
)

var (
	ErrNotSealed = errors.New("message has no sealed content")
	ErrNoKey     = errors.New("no key for the message's epoch")
)

// Keys is a device's end-to-end encryption state: its key pair, the epoch
// keys it holds, and the keys of other devices it has seen. Connections of
// the same device may share it; it is safe for concurrent use.
type Keys struct {
	UserID   []byte
	DeviceID uint64
	Private  *ecdh.PrivateKey

	mu sync.Mutex
	// convs by conversation id.
	convs map[string]*conversationKeys
	// directory has every device key seen, by user id and device id, kept
	// after the device signs out so its earlier wraps still check.
	directory map[string]map[uint64][]byte
}

type conversationKeys struct {
	// rotating serializes this device's refreshes and rotations.
	rotating sync.Mutex

	// Guarded by Keys.mu.
	epochs map[uint32][]byte
	// gen counts invalidations: a change of membership or of a member's
	// devices. readyGen is the gen at which current was last checked.
	gen      uint64
	readyGen uint64
	current  uint32
	// direct: the users of a direct conversation or the notes, which never
	// change.
	direct [][]byte
	// members of a group and the gen they were read at.
	members    [][]byte
	membersGen uint64
}

// NewKeys makes the state of a device with the given key pair.
func NewKeys(userID []byte, deviceID uint64, private *ecdh.PrivateKey) *Keys {
	k := &Keys{
		UserID:    userID,
		DeviceID:  deviceID,
		Private:   private,
		convs:     map[string]*conversationKeys{},
		directory: map[string]map[uint64][]byte{},
	}
	k.directory[string(userID)] = map[uint64][]byte{deviceID: private.PublicKey().Bytes()}
	return k
}

// SetKeys makes the connection use the device's keys: SendText seals with
// them, OpenMessage opens with them, and received events keep them
// current. Nil turns them off.
func (c *Client) SetKeys(k *Keys) {
	c.keys.Store(k)
}

func (c *Client) Keys() *Keys {
	return c.keys.Load()
}

// EnableKeys generates a key pair for the connected device, publishes its
// public key and makes the connection use it.
func (c *Client) EnableKeys(ctx context.Context, userID []byte, deviceID uint64) (*Keys, error) {
	private, err := e2ee.GenerateKey()
	if err != nil {
		return nil, err
	}
	k := NewKeys(userID, deviceID, private)
	if err := c.SetDeviceKey(ctx, private.PublicKey().Bytes()); err != nil {
		return nil, err
	}
	c.SetKeys(k)
	return k, nil
}

func (c *Client) requireKeys() (*Keys, error) {
	if k := c.Keys(); k != nil {
		return k, nil
	}
	return nil, errors.New("the connection has no keys")
}

// ShareHistory wraps every epoch key this device has of the conversation
// for another device of its user.
func (c *Client) ShareHistory(ctx context.Context, conversationID []byte, deviceID uint64, publicKey []byte) error {
	k, err := c.requireKeys()
	if err != nil {
		return err
	}
	k.mu.Lock()
	cs := k.conv(conversationID)
	k.mu.Unlock()
	cs.rotating.Lock()
	_, err = k.refresh(ctx, c, conversationID, cs, 0)
	cs.rotating.Unlock()
	if err != nil {
		return err
	}
	k.mu.Lock()
	epochs := maps.Clone(cs.epochs)
	k.mu.Unlock()
	// One request per epoch: the server checks each against its rules.
	for _, e := range slices.Sorted(maps.Keys(epochs)) {
		wrapped, err := k.wrap(conversationID, e, epochs[e], k.UserID, deviceID, publicKey)
		if err != nil {
			return err
		}
		if err := c.PublishKeys(ctx, conversationID, e, []*pb.WrappedKey{wrapped}); err != nil {
			return err
		}
	}
	return nil
}

// EpochKey returns the key of an epoch this device holds, or nil.
func (k *Keys) EpochKey(conversationID []byte, epoch uint32) []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.conv(conversationID).epochs[epoch]
}

// Invalidate makes the next send check the conversation's members, their
// devices and its epoch again.
func (k *Keys) Invalidate(conversationID []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.conv(conversationID).gen++
}

// conv returns a conversation's state; k.mu must be held.
func (k *Keys) conv(conversationID []byte) *conversationKeys {
	cs, ok := k.convs[string(conversationID)]
	if !ok {
		cs = &conversationKeys{epochs: map[uint32][]byte{}, gen: 1}
		k.convs[string(conversationID)] = cs
	}
	return cs
}

// SetPeer tells the keys the other user of a direct conversation, for a
// device that has not opened or listed it.
func (k *Keys) SetPeer(conversationID, peerUserID []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.conv(conversationID).direct = [][]byte{k.UserID, peerUserID}
}

// learnConversation remembers the users of a direct conversation, or of
// the user's notes.
func (k *Keys) learnConversation(conv *pb.Conversation) {
	if k == nil {
		return
	}
	var users [][]byte
	switch {
	case conv.GetKind() == pb.ConversationKind_CONVERSATION_KIND_DIRECT && conv.GetPeer() != nil:
		users = [][]byte{k.UserID, conv.GetPeer().GetUserId()}
	case conv.GetKind() == pb.ConversationKind_CONVERSATION_KIND_NOTES:
		users = [][]byte{k.UserID}
	default:
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.conv(conv.GetConversationId()).direct = users
}

// observe keeps the state current with a received event: a member's
// device key changing, or a group's membership, makes the next send check
// again.
func (k *Keys) observe(ev *pb.Event) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if d := ev.GetDevice(); d != nil {
		user, id, key := d.GetUserId(), d.GetDevice().GetDeviceId(), d.GetDevice().GetPublicKey()
		if user == nil || (len(key) == 0 && !d.GetSignedOut()) {
			return
		}
		known := k.directory[string(user)][id]
		// A device this device has not seen is in no roster it used.
		if d.GetSignedOut() && known == nil || !d.GetSignedOut() && bytes.Equal(known, key) {
			return
		}
		if len(key) > 0 {
			k.remember(user, id, key)
		}
		for _, cs := range k.convs {
			cs.gen++
		}
		return
	}
	m := ev.GetMessage().GetMessage()
	if m == nil {
		return
	}
	cs := k.conv(m.GetConversationId())
	if m.GetNotice() != nil || m.GetSealed().GetEpoch() > cs.current {
		cs.gen++
	}
}

func (k *Keys) remember(userID []byte, deviceID uint64, publicKey []byte) {
	devices := k.directory[string(userID)]
	if devices == nil {
		devices = map[uint64][]byte{}
		k.directory[string(userID)] = devices
	}
	devices[deviceID] = publicKey
}

// sendKey returns the epoch to seal with and its key: the current one if
// it is still right, otherwise a new one this device publishes.
func (k *Keys) sendKey(ctx context.Context, c *Client, conversationID []byte) (uint32, []byte, error) {
	k.mu.Lock()
	cs := k.conv(conversationID)
	if cs.readyGen == cs.gen && cs.epochs[cs.current] != nil {
		epoch, key := cs.current, cs.epochs[cs.current]
		k.mu.Unlock()
		return epoch, key, nil
	}
	k.mu.Unlock()

	cs.rotating.Lock()
	defer cs.rotating.Unlock()
	var lastErr error
	for range rotateAttempts {
		k.mu.Lock()
		gen := cs.gen
		if cs.readyGen == gen && cs.epochs[cs.current] != nil {
			epoch, key := cs.current, cs.epochs[cs.current]
			k.mu.Unlock()
			return epoch, key, nil
		}
		k.mu.Unlock()

		state, err := k.refresh(ctx, c, conversationID, cs, k.highest(cs))
		if err != nil {
			return 0, nil, err
		}
		members, err := k.members(ctx, c, conversationID, cs)
		if err != nil {
			return 0, nil, err
		}
		devices, err := k.memberDevices(ctx, c, members)
		if err != nil {
			return 0, nil, err
		}
		current := state.GetCurrentEpoch()
		k.mu.Lock()
		have := cs.epochs[current]
		k.mu.Unlock()
		if current > 0 && !state.GetRotate() && have != nil && sameRecipients(devices, state.GetCurrentRecipients()) {
			k.ready(cs, gen, current)
			return current, have, nil
		}

		epoch := current + 1
		key := e2ee.NewEpochKey()
		lastErr = k.publish(ctx, c, conversationID, epoch, key, devices)
		if lastErr == nil {
			k.mu.Lock()
			cs.epochs[epoch] = key
			k.mu.Unlock()
			k.ready(cs, gen, epoch)
			return epoch, key, nil
		}
		// Another device published first, or the members changed.
		code := Code(lastErr)
		if code != pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION && code != pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
			return 0, nil, lastErr
		}
		k.mu.Lock()
		cs.gen++
		k.mu.Unlock()
	}
	return 0, nil, fmt.Errorf("no epoch after %d attempts: %w", rotateAttempts, lastErr)
}

func (k *Keys) ready(cs *conversationKeys, gen uint64, epoch uint32) {
	k.mu.Lock()
	defer k.mu.Unlock()
	cs.current = max(cs.current, epoch)
	if cs.current == epoch {
		cs.readyGen = gen
	}
}

func (k *Keys) highest(cs *conversationKeys) uint32 {
	k.mu.Lock()
	defer k.mu.Unlock()
	var h uint32
	for e := range cs.epochs {
		h = max(h, e)
	}
	return h
}

// epochKey returns the key of an epoch, fetching the keys from that epoch
// on if this device does not hold it.
func (k *Keys) epochKey(ctx context.Context, c *Client, conversationID []byte, epoch uint32) ([]byte, error) {
	if key := k.EpochKey(conversationID, epoch); key != nil {
		return key, nil
	}
	if epoch == 0 {
		return nil, ErrNoKey
	}
	k.mu.Lock()
	cs := k.conv(conversationID)
	k.mu.Unlock()
	cs.rotating.Lock()
	defer cs.rotating.Unlock()
	if key := k.EpochKey(conversationID, epoch); key != nil {
		return key, nil
	}
	if _, err := k.refresh(ctx, c, conversationID, cs, epoch-1); err != nil {
		return nil, err
	}
	if key := k.EpochKey(conversationID, epoch); key != nil {
		return key, nil
	}
	return nil, ErrNoKey
}

// refresh fetches the conversation's key state and the wraps for this
// device after an epoch, and keeps the keys that unwrap from devices whose
// key matches what this device knows of them.
func (k *Keys) refresh(ctx context.Context, c *Client, conversationID []byte, cs *conversationKeys, after uint32) (*pb.GetConversationKeysResponse, error) {
	state, err := c.GetConversationKeys(ctx, conversationID, after)
	if err != nil {
		return nil, err
	}
	for _, rk := range state.GetKeys() {
		known, err := k.deviceKey(ctx, c, rk.GetSenderUserId(), rk.GetSenderDeviceId())
		if err != nil {
			return nil, err
		}
		if known != nil && !bytes.Equal(known, rk.GetSenderPublicKey()) {
			continue
		}
		key, err := e2ee.Unwrap(k.Private, rk.GetSenderPublicKey(), e2ee.WrapContext{
			ConversationID:    conversationID,
			Epoch:             rk.GetEpoch(),
			SenderUserID:      rk.GetSenderUserId(),
			SenderDeviceID:    rk.GetSenderDeviceId(),
			RecipientUserID:   k.UserID,
			RecipientDeviceID: k.DeviceID,
		}, rk.GetWrapped())
		if err != nil {
			continue
		}
		k.mu.Lock()
		cs.epochs[rk.GetEpoch()] = key
		k.mu.Unlock()
	}
	return state, nil
}

// deviceKey returns the key this device knows for another device, asking
// the directory if it has not seen the device; nil if the directory does
// not list it either (it signed out before this device saw it).
func (k *Keys) deviceKey(ctx context.Context, c *Client, userID []byte, deviceID uint64) ([]byte, error) {
	k.mu.Lock()
	known := k.directory[string(userID)][deviceID]
	k.mu.Unlock()
	if known != nil {
		return known, nil
	}
	if _, err := k.memberDevices(ctx, c, [][]byte{userID}); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.directory[string(userID)][deviceID], nil
}

// members returns the users of a conversation: the two of a direct one,
// the user of their notes, the current members of a group.
func (k *Keys) members(ctx context.Context, c *Client, conversationID []byte, cs *conversationKeys) ([][]byte, error) {
	k.mu.Lock()
	if cs.direct != nil {
		defer k.mu.Unlock()
		return cs.direct, nil
	}
	if cs.members != nil && cs.membersGen == cs.gen {
		defer k.mu.Unlock()
		return cs.members, nil
	}
	gen := cs.gen
	k.mu.Unlock()

	list, err := c.GetMembers(ctx, conversationID)
	if Reason(err) == pb.ErrorReason_ERROR_REASON_NOT_A_GROUP {
		// Not a group: find the peer, or the notes.
		if _, err := c.ListConversations(ctx); err != nil {
			return nil, err
		}
		k.mu.Lock()
		defer k.mu.Unlock()
		if cs.direct == nil {
			return nil, errors.New("conversation not in the list")
		}
		return cs.direct, nil
	}
	if err != nil {
		return nil, err
	}
	members := make([][]byte, len(list))
	for i, m := range list {
		members[i] = m.GetUser().GetUserId()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	cs.members, cs.membersGen = members, gen
	return members, nil
}

// memberDevices returns the signed-in devices with keys of the users and
// remembers their keys.
func (k *Keys) memberDevices(ctx context.Context, c *Client, users [][]byte) ([]*pb.KeyRecipient, error) {
	var devices []*pb.KeyRecipient
	for chunk := range slices.Chunk(users, maxDirectory) {
		list, err := c.GetDeviceKeys(ctx, chunk)
		if err != nil {
			return nil, err
		}
		k.mu.Lock()
		for _, u := range list {
			for _, d := range u.GetDevices() {
				k.remember(u.GetUserId(), d.GetDeviceId(), d.GetPublicKey())
				devices = append(devices, &pb.KeyRecipient{UserId: u.GetUserId(), DeviceId: d.GetDeviceId()})
			}
		}
		k.mu.Unlock()
	}
	return devices, nil
}

func (k *Keys) wrap(conversationID []byte, epoch uint32, key, userID []byte, deviceID uint64, publicKey []byte) (*pb.WrappedKey, error) {
	wrapped, err := e2ee.Wrap(k.Private, publicKey, e2ee.WrapContext{
		ConversationID:    conversationID,
		Epoch:             epoch,
		SenderUserID:      k.UserID,
		SenderDeviceID:    k.DeviceID,
		RecipientUserID:   userID,
		RecipientDeviceID: deviceID,
	}, e2ee.NewNonce(), key)
	if err != nil {
		return nil, err
	}
	return &pb.WrappedKey{RecipientUserId: userID, RecipientDeviceId: deviceID, Wrapped: wrapped}, nil
}

// publish wraps a new epoch's key for every device, the first request
// starting the epoch and any further ones adding to it. The wrap for this
// device goes first: the server takes a request with it as making the
// epoch, and refuses it if another device made the epoch first.
func (k *Keys) publish(ctx context.Context, c *Client, conversationID []byte, epoch uint32, key []byte, devices []*pb.KeyRecipient) error {
	devices = slices.Clone(devices)
	if i := slices.IndexFunc(devices, func(d *pb.KeyRecipient) bool {
		return bytes.Equal(d.GetUserId(), k.UserID) && d.GetDeviceId() == k.DeviceID
	}); i > 0 {
		devices[0], devices[i] = devices[i], devices[0]
	}
	wraps := make([]*pb.WrappedKey, 0, len(devices))
	for _, d := range devices {
		k.mu.Lock()
		pub := k.directory[string(d.GetUserId())][d.GetDeviceId()]
		k.mu.Unlock()
		w, err := k.wrap(conversationID, epoch, key, d.GetUserId(), d.GetDeviceId(), pub)
		if err != nil {
			return err
		}
		wraps = append(wraps, w)
	}
	for chunk := range slices.Chunk(wraps, maxPublish) {
		if err := c.PublishKeys(ctx, conversationID, epoch, chunk); err != nil {
			return err
		}
	}
	return nil
}

// sameRecipients reports whether the devices are the epoch's recipients.
func sameRecipients(devices, recipients []*pb.KeyRecipient) bool {
	if len(devices) != len(recipients) {
		return false
	}
	set := make(map[string]bool, len(devices))
	for _, d := range devices {
		set[recipientKey(d)] = true
	}
	for _, r := range recipients {
		if !set[recipientKey(r)] {
			return false
		}
	}
	return true
}

func recipientKey(r *pb.KeyRecipient) string {
	return fmt.Sprintf("%x/%d", r.GetUserId(), r.GetDeviceId())
}
