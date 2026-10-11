package conformance

import (
	"bytes"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
	"github.com/vlumi/wrinkle/clients/go/e2ee"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func publicKey(d *device) []byte {
	return d.Keys().Private.PublicKey().Bytes()
}

// wrapFor wraps an epoch key from one device for another.
func wrapFor(t *testing.T, from, to *device, conversationID []byte, epoch uint32, key []byte) *pb.WrappedKey {
	t.Helper()
	wrapped, err := e2ee.Wrap(from.Keys().Private, publicKey(to), e2ee.WrapContext{
		ConversationID:    conversationID,
		Epoch:             epoch,
		SenderUserID:      from.user.GetUserId(),
		SenderDeviceID:    from.deviceID,
		RecipientUserID:   to.user.GetUserId(),
		RecipientDeviceID: to.deviceID,
	}, e2ee.NewNonce(), key)
	if err != nil {
		t.Fatal(err)
	}
	return &pb.WrappedKey{RecipientUserId: to.user.GetUserId(), RecipientDeviceId: to.deviceID, Wrapped: wrapped}
}

// publish publishes an epoch key from one device for the others.
func publish(t *testing.T, from *device, conversationID []byte, epoch uint32, key []byte, to ...*device) error {
	t.Helper()
	var keys []*pb.WrappedKey
	for _, d := range to {
		keys = append(keys, wrapFor(t, from, d, conversationID, epoch, key))
	}
	return from.PublishKeys(ctx(t), conversationID, epoch, keys)
}

func mustPublish(t *testing.T, from *device, conversationID []byte, epoch uint32, key []byte, to ...*device) {
	t.Helper()
	if err := publish(t, from, conversationID, epoch, key, to...); err != nil {
		t.Fatalf("publish epoch %d: %v", epoch, err)
	}
}

func conversationKeys(t *testing.T, d *device, conversationID []byte, after uint32) *pb.GetConversationKeysResponse {
	t.Helper()
	state, err := d.GetConversationKeys(ctx(t), conversationID, after)
	if err != nil {
		t.Fatalf("get conversation keys: %v", err)
	}
	return state
}

// receivedKey unwraps a key the device received and checks its sender.
func receivedKey(t *testing.T, d *device, conversationID []byte, rk *pb.ReceivedKey, from *device) []byte {
	t.Helper()
	if !bytes.Equal(rk.GetSenderUserId(), from.user.GetUserId()) || rk.GetSenderDeviceId() != from.deviceID ||
		!bytes.Equal(rk.GetSenderPublicKey(), publicKey(from)) {
		t.Fatalf("key of epoch %d not from %s's device %d: %v", rk.GetEpoch(), from.user.GetName(), from.deviceID, rk)
	}
	key, err := e2ee.Unwrap(d.Keys().Private, rk.GetSenderPublicKey(), e2ee.WrapContext{
		ConversationID:    conversationID,
		Epoch:             rk.GetEpoch(),
		SenderUserID:      rk.GetSenderUserId(),
		SenderDeviceID:    rk.GetSenderDeviceId(),
		RecipientUserID:   d.user.GetUserId(),
		RecipientDeviceID: d.deviceID,
	}, rk.GetWrapped())
	if err != nil {
		t.Fatalf("unwrap epoch %d: %v", rk.GetEpoch(), err)
	}
	return key
}

// wantRecipients checks the current epoch's recipients, ordered by user id
// and device id.
func wantRecipients(t *testing.T, state *pb.GetConversationKeysResponse, devices ...*device) {
	t.Helper()
	var want []*pb.KeyRecipient
	for _, d := range devices {
		want = append(want, &pb.KeyRecipient{UserId: d.user.GetUserId(), DeviceId: d.deviceID})
	}
	slices.SortFunc(want, func(a, b *pb.KeyRecipient) int {
		if c := bytes.Compare(a.GetUserId(), b.GetUserId()); c != 0 {
			return c
		}
		switch {
		case a.GetDeviceId() < b.GetDeviceId():
			return -1
		case a.GetDeviceId() > b.GetDeviceId():
			return 1
		}
		return 0
	})
	got := state.GetCurrentRecipients()
	ok := len(got) == len(want)
	for i := 0; ok && i < len(got); i++ {
		ok = bytes.Equal(got[i].GetUserId(), want[i].GetUserId()) && got[i].GetDeviceId() == want[i].GetDeviceId()
	}
	if !ok {
		t.Fatalf("current recipients %v, want %v", got, want)
	}
}

// keyOnly checks a device as others see it: its id and key, nothing else.
func keyOnly(t *testing.T, dev *pb.Device, d *device) {
	t.Helper()
	if dev.GetDeviceId() != d.deviceID || !bytes.Equal(dev.GetPublicKey(), publicKey(d)) || dev.GetName() != "" ||
		dev.GetCreatedAt() != nil || dev.GetLastSeenAt() != nil || dev.GetPush() {
		t.Fatalf("device %v, want only device %d and its key", dev, d.deviceID)
	}
}

func TestSetDeviceKey(t *testing.T) {
	d := newUser(t, "keys")
	key := publicKey(d)
	wantCode(t, d.SetDeviceKey(ctx(t), key[:31]), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	wantCode(t, d.SetDeviceKey(ctx(t), append(bytes.Clone(key), 0)), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	wantCode(t, d.SetDeviceKey(ctx(t), nil), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	// The same key again does nothing; a different one is refused.
	if err := d.SetDeviceKey(ctx(t), key); err != nil {
		t.Fatalf("same key again: %v", err)
	}
	other, _ := e2ee.GenerateKey()
	wantCode(t, d.SetDeviceKey(ctx(t), other.PublicKey().Bytes()), pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)

	d2 := otherDeviceWithoutKey(t, d)
	deviceEvent(t, d, d2.deviceID, func(e *pb.DeviceEvent) bool { return !e.GetSignedOut() })
	list, err := d.ListDevices(ctx(t))
	if err != nil || len(list) != 2 || !bytes.Equal(list[0].GetPublicKey(), key) || len(list[1].GetPublicKey()) != 0 {
		t.Fatalf("devices %v %v", list, err)
	}
	// Setting it reaches the user's devices, with the whole device.
	key2 := other.PublicKey().Bytes()
	if err := d2.SetDeviceKey(ctx(t), key2); err != nil {
		t.Fatal(err)
	}
	for _, own := range []*device{d, d2} {
		e := deviceEvent(t, own, d2.deviceID, func(e *pb.DeviceEvent) bool { return len(e.GetDevice().GetPublicKey()) > 0 })
		if !bytes.Equal(e.GetUserId(), d.user.GetUserId()) || !bytes.Equal(e.GetDevice().GetPublicKey(), key2) ||
			e.GetDevice().GetName() != "conformance" || e.GetDevice().GetCreatedAt() == nil || e.GetSignedOut() {
			t.Fatalf("own device event %v", e)
		}
	}
	if list, err := d2.ListDevices(ctx(t)); err != nil || !bytes.Equal(list[1].GetPublicKey(), key2) {
		t.Fatalf("devices %v %v", list, err)
	}
}

func TestGetDeviceKeys(t *testing.T) {
	a := newUser(t, "alice")
	a2 := otherDevice(t, a)
	otherDeviceWithoutKey(t, a)
	b := newUser(t, "bob")
	gone := newUser(t, "gone")
	if err := gone.DeleteAccount(ctx(t), password); err != nil {
		t.Fatal(err)
	}
	// Anyone signed in may ask.
	eve := newUser(t, "eve")
	users, err := eve.GetDeviceKeys(ctx(t), [][]byte{a.user.GetUserId(), randomID(t), b.user.GetUserId(), gone.user.GetUserId()})
	if err != nil {
		t.Fatal(err)
	}
	byUser := map[string][]*pb.Device{}
	for _, u := range users {
		byUser[string(u.GetUserId())] = u.GetDevices()
	}
	if len(users) != 2 || len(byUser) != 2 {
		t.Fatalf("users %v, want alice and bob", users)
	}
	// Signed-in devices with a key, by device id.
	ad := byUser[string(a.user.GetUserId())]
	first, second := a, a2
	if a2.deviceID < a.deviceID {
		first, second = a2, a
	}
	if len(ad) != 2 {
		t.Fatalf("alice's devices %v", ad)
	}
	keyOnly(t, ad[0], first)
	keyOnly(t, ad[1], second)
	if bd := byUser[string(b.user.GetUserId())]; len(bd) != 1 {
		t.Fatalf("bob's devices %v", bd)
	} else {
		keyOnly(t, bd[0], b)
	}

	// A device signed out is left out.
	if err := a.SignOutDevice(ctx(t), a2.deviceID); err != nil {
		t.Fatal(err)
	}
	users, err = eve.GetDeviceKeys(ctx(t), [][]byte{a.user.GetUserId()})
	if err != nil || len(users) != 1 || len(users[0].GetDevices()) != 1 {
		t.Fatalf("after signing out: %v %v", users, err)
	}
	keyOnly(t, users[0].GetDevices()[0], a)

	// 1 to 256 ids of 16 bytes.
	_, err = eve.GetDeviceKeys(ctx(t), nil)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	ids := make([][]byte, 256)
	for i := range ids {
		ids[i] = randomID(t)
	}
	if users, err := eve.GetDeviceKeys(ctx(t), ids); err != nil || len(users) != 0 {
		t.Fatalf("256 unknown users: %v %v", users, err)
	}
	_, err = eve.GetDeviceKeys(ctx(t), append(ids, randomID(t)))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = eve.GetDeviceKeys(ctx(t), [][]byte{a.user.GetUserId()[:15]})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
}

func TestDeviceKeyEventsReachPartners(t *testing.T) {
	a, b, _ := pair(t)
	c := newUser(t, "carol")
	// Bob is both a direct partner and in the group: one event each.
	newGroup(t, a, "Keys", b, c)
	dave := newUser(t, "dave")

	// A device without a key reaches only the user's own devices; one
	// with a key reaches partners and co-members as its id and key.
	a3 := otherDeviceWithoutKey(t, a)
	a2 := otherDevice(t, a)
	isDevice := func(ev *pb.Event) bool { return ev.GetDevice() != nil }
	for _, d := range []*device{b, c} {
		e := nextEvent(t, d, isDevice).GetDevice()
		if !bytes.Equal(e.GetUserId(), a.user.GetUserId()) || e.GetSignedOut() {
			t.Fatalf("%s got %v", d.user.GetName(), e)
		}
		keyOnly(t, e.GetDevice(), a2)
	}
	e := deviceEvent(t, a, a2.deviceID, func(e *pb.DeviceEvent) bool { return len(e.GetDevice().GetPublicKey()) > 0 })
	if !bytes.Equal(e.GetUserId(), a.user.GetUserId()) || e.GetDevice().GetName() != "conformance" {
		t.Fatalf("own device event %v", e)
	}

	// Signing out: again only the device with a key.
	if err := a.SignOutDevice(ctx(t), a3.deviceID); err != nil {
		t.Fatal(err)
	}
	waitSignedOut(t, a3)
	if err := a.SignOutDevice(ctx(t), a2.deviceID); err != nil {
		t.Fatal(err)
	}
	waitSignedOut(t, a2)
	for _, d := range []*device{b, c} {
		e := nextEvent(t, d, isDevice).GetDevice()
		if !bytes.Equal(e.GetUserId(), a.user.GetUserId()) || !e.GetSignedOut() {
			t.Fatalf("%s got %v, want alice's device %d signed out", d.user.GetName(), e, a2.deviceID)
		}
		keyOnly(t, e.GetDevice(), a2)
	}
	noEvent(t, dave, time.Second)
}

func TestPublishKeys(t *testing.T) {
	a, b, conv := pair(t)
	eve := newUser(t, "eve")
	k1 := e2ee.NewEpochKey()

	// The first epoch is 1.
	wantCode(t, publish(t, a, conv, 0, k1, a, b), pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	wantCode(t, publish(t, a, conv, 2, k1, a, b), pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	// At least one key, each 60 bytes for a valid recipient. (1001 keys
	// would not fit in a frame.)
	wantCode(t, a.PublishKeys(ctx(t), conv, 1, nil), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	bad := wrapFor(t, a, b, conv, 1, k1)
	bad.Wrapped = bad.Wrapped[:59]
	wantCode(t, a.PublishKeys(ctx(t), conv, 1, []*pb.WrappedKey{bad}), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	bad = wrapFor(t, a, b, conv, 1, k1)
	bad.RecipientUserId = bad.RecipientUserId[:15]
	wantCode(t, a.PublishKeys(ctx(t), conv, 1, []*pb.WrappedKey{bad}), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	// Recipients are members; only members publish, from a device with a
	// key.
	wantCode(t, publish(t, a, conv, 1, k1, a, eve), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	wantCode(t, publish(t, eve, conv, 1, k1, a, b), pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	keyless := otherDeviceWithoutKey(t, a)
	wantCode(t, keyless.PublishKeys(ctx(t), conv, 1, []*pb.WrappedKey{wrapFor(t, a, b, conv, 1, k1)}),
		pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	if state := conversationKeys(t, a, conv, 0); state.GetCurrentEpoch() != 0 || state.GetRotate() || len(state.GetKeys()) != 0 {
		t.Fatalf("keys before the first epoch: %v", state)
	}

	mustPublish(t, a, conv, 1, k1, a, b)
	// Each device gets only its own wrap.
	state := conversationKeys(t, b, conv, 0)
	if state.GetCurrentEpoch() != 1 || state.GetRotate() || len(state.GetKeys()) != 1 || state.GetKeys()[0].GetEpoch() != 1 {
		t.Fatalf("bob's keys: %v", state)
	}
	wantRecipients(t, state, a, b)
	if key := receivedKey(t, b, conv, state.GetKeys()[0], a); !bytes.Equal(key, k1) {
		t.Fatal("bob unwrapped another key")
	}
	_, err := eve.GetConversationKeys(ctx(t), conv, 0)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	_, err = eve.GetConversationKeys(ctx(t), conv[:15], 0)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	// The current epoch takes more recipients, members' devices only, and
	// keeps the wraps a recipient has.
	a2 := otherDevice(t, a)
	mustPublish(t, b, conv, 1, k1, a2)
	mustPublish(t, a, conv, 1, e2ee.NewEpochKey(), b)
	wantCode(t, publish(t, a, conv, 1, k1, eve), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	state = conversationKeys(t, b, conv, 0)
	wantRecipients(t, state, a, a2, b)
	if key := receivedKey(t, b, conv, state.GetKeys()[0], a); len(state.GetKeys()) != 1 || !bytes.Equal(key, k1) {
		t.Fatal("bob's wrap was replaced")
	}
	if got := conversationKeys(t, a2, conv, 0).GetKeys(); len(got) != 1 || !bytes.Equal(receivedKey(t, a2, conv, got[0], b), k1) {
		t.Fatalf("alice's second device's keys: %v", got)
	}

	// The next epoch; after_epoch leaves out the earlier ones.
	k2 := e2ee.NewEpochKey()
	mustPublish(t, b, conv, 2, k2, a, a2, b)
	state = conversationKeys(t, a, conv, 1)
	if state.GetCurrentEpoch() != 2 || len(state.GetKeys()) != 1 || state.GetKeys()[0].GetEpoch() != 2 {
		t.Fatalf("keys after epoch 1: %v", state)
	}
	wantRecipients(t, state, a, a2, b)
	if len(conversationKeys(t, a, conv, 2).GetKeys()) != 0 {
		t.Fatal("keys after the current epoch")
	}

	// Earlier epochs go only to the user's own devices: history for a new
	// device.
	a4 := otherDevice(t, a)
	wantCode(t, publish(t, b, conv, 1, k1, a4), pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	mustPublish(t, a, conv, 1, k1, a4)
	state = conversationKeys(t, a4, conv, 0)
	if len(state.GetKeys()) != 1 || state.GetKeys()[0].GetEpoch() != 1 || !bytes.Equal(receivedKey(t, a4, conv, state.GetKeys()[0], a), k1) {
		t.Fatalf("history keys: %v", state.GetKeys())
	}
	wantRecipients(t, state, a, a2, b)
}

func TestConcurrentPublish(t *testing.T) {
	a, b, conv := pair(t)
	for epoch := uint32(1); epoch <= 5; epoch++ {
		keys := map[*device][]byte{a: e2ee.NewEpochKey(), b: e2ee.NewEpochKey()}
		errs := map[*device]error{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for d, key := range keys {
			wraps := []*pb.WrappedKey{wrapFor(t, d, a, conv, epoch, key), wrapFor(t, d, b, conv, epoch, key)}
			wg.Go(func() {
				err := d.PublishKeys(ctx(t), conv, epoch, wraps)
				mu.Lock()
				errs[d] = err
				mu.Unlock()
			})
		}
		wg.Wait()
		winner, loser := a, b
		if errs[a] != nil {
			winner, loser = b, a
		}
		if errs[winner] != nil {
			t.Fatalf("epoch %d: both failed: %v, %v", epoch, errs[a], errs[b])
		}
		wantCode(t, errs[loser], pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
		for _, d := range []*device{a, b} {
			state := conversationKeys(t, d, conv, epoch-1)
			if state.GetCurrentEpoch() != epoch || len(state.GetKeys()) != 1 ||
				!bytes.Equal(receivedKey(t, d, conv, state.GetKeys()[0], winner), keys[winner]) {
				t.Fatalf("epoch %d: %s does not have the winner's key: %v", epoch, d.user.GetName(), state)
			}
		}
	}
}

func TestSendNeedsCurrentEpoch(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")
	d := newUser(t, "dave")
	id := newGroup(t, a, "Epochs", b, c)
	send := func(cmid string, epoch uint32) error {
		_, err := a.SendMessage(ctx(t), id, cmid, junkSealed(epoch))
		return err
	}
	rotate := func(want bool) {
		t.Helper()
		if got := conversationKeys(t, a, id, 0).GetRotate(); got != want {
			t.Fatalf("rotate is %v, want %v", got, want)
		}
	}

	// None yet; a change of members before the first epoch needs none.
	wantCode(t, send("m1", 0), pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	wantCode(t, send("m1", 1), pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	if err := a.AddMembers(ctx(t), id, []string{d.user.GetName()}); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveMember(ctx(t), id, d.user.GetUserId()); err != nil {
		t.Fatal(err)
	}
	rotate(false)
	mustPublish(t, a, id, 1, e2ee.NewEpochKey(), a, b, c)
	wantCode(t, send("m1", 2), pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	first, err := a.SendMessage(ctx(t), id, "m1", junkSealed(1))
	if err != nil {
		t.Fatal(err)
	}

	// Adding a member: no sends until the next epoch, then only with it.
	if err := a.AddMembers(ctx(t), id, []string{d.user.GetName()}); err != nil {
		t.Fatal(err)
	}
	rotate(true)
	wantCode(t, send("m2", 1), pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	// A resend still returns the message stored.
	if again, err := a.SendMessage(ctx(t), id, "m1", junkSealed(1)); err != nil || again.GetSeq() != first.GetSeq() {
		t.Fatalf("resend while rotating: %v %v", again, err)
	}
	mustPublish(t, a, id, 2, e2ee.NewEpochKey(), a, b, c, d)
	rotate(false)
	wantCode(t, send("m2", 1), pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	if err := send("m2", 2); err != nil {
		t.Fatal(err)
	}

	// Removing one.
	if err := a.RemoveMember(ctx(t), id, d.user.GetUserId()); err != nil {
		t.Fatal(err)
	}
	rotate(true)
	wantCode(t, send("m3", 2), pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	// The member removed may not publish, nor be a recipient, and gets no
	// later epoch.
	wantCode(t, publish(t, d, id, 3, e2ee.NewEpochKey(), a, b, c), pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	wantCode(t, publish(t, a, id, 3, e2ee.NewEpochKey(), a, b, c, d), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	mustPublish(t, a, id, 3, e2ee.NewEpochKey(), a, b, c)
	if err := send("m3", 3); err != nil {
		t.Fatal(err)
	}
	state := conversationKeys(t, d, id, 0)
	if len(state.GetKeys()) != 1 || state.GetKeys()[0].GetEpoch() != 2 {
		t.Fatalf("removed member's keys: %v", state.GetKeys())
	}

	// Leaving.
	if err := c.LeaveGroup(ctx(t), id); err != nil {
		t.Fatal(err)
	}
	rotate(true)
	wantCode(t, send("m4", 3), pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	mustPublish(t, a, id, 4, e2ee.NewEpochKey(), a, b)
	if err := send("m4", 4); err != nil {
		t.Fatal(err)
	}
	wantCode(t, c.PublishKeys(ctx(t), id, 4, []*pb.WrappedKey{wrapFor(t, c, a, id, 4, e2ee.NewEpochKey())}),
		pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)

	// The client does all of it itself.
	if err := a.AddMembers(ctx(t), id, []string{c.user.GetName()}); err != nil {
		t.Fatal(err)
	}
	noticeEvent(t, c, id, 11)
	msg, err := c.SendText(ctx(t), id, "m2", "back")
	if err != nil {
		t.Fatal(err)
	}
	if msg.GetSealed().GetEpoch() != 5 {
		t.Fatalf("sent with epoch %d, want 5", msg.GetSealed().GetEpoch())
	}
	if got := text(t, b, messageEvent(t, b, id, msg.GetSeq())); got != "back" {
		t.Fatalf("bob read %q", got)
	}
}

func TestContentIsEndToEnd(t *testing.T) {
	a, b, conv := pair(t)
	const secret = "the server never reads this"
	sent, err := a.SendText(ctx(t), conv, "m1", secret)
	if err != nil {
		t.Fatal(err)
	}
	got := messageEvent(t, b, conv, 1)
	if got.GetSealed() == nil || bytes.Contains(got.GetSealed().GetCiphertext(), []byte(secret)) {
		t.Fatalf("content is not sealed: %v", got)
	}
	if text(t, b, got) != secret {
		t.Fatal("bob read another text")
	}
	msgs, err := b.GetMessages(ctx(t), conv, 0, 0)
	if err != nil || len(msgs) != 1 || text(t, b, msgs[0]) != secret {
		t.Fatalf("history: %v %v", msgs, err)
	}
	if entry := findConversation(t, b, conv); text(t, b, entry.GetLastMessage()) != secret {
		t.Fatalf("list entry: %v", entry)
	}

	// The content is bound to its message: as another message, sender or
	// conversation it does not open.
	key := b.Keys().EpochKey(conv, sent.GetSealed().GetEpoch())
	for _, c := range []e2ee.ContentContext{
		{ConversationID: conv, Epoch: 1, SenderUserID: a.user.GetUserId(), ClientMessageID: "m2"},
		{ConversationID: conv, Epoch: 1, SenderUserID: b.user.GetUserId(), ClientMessageID: "m1"},
		{ConversationID: randomID(t), Epoch: 1, SenderUserID: a.user.GetUserId(), ClientMessageID: "m1"},
	} {
		if _, err := e2ee.OpenContent(key, c, got.GetSealed()); !errors.Is(err, e2ee.ErrDecrypt) {
			t.Fatalf("opened as %v: %v", c, err)
		}
	}
}

func TestDeviceChangesRotateKeys(t *testing.T) {
	a, b, conv := pair(t)
	if _, err := a.SendText(ctx(t), conv, "m1", "one"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, conv, 1)

	// A new device: the next send makes an epoch that includes it, though
	// it cannot read what came before until another device of its user
	// shares it.
	b2 := otherDevice(t, b)
	deviceEvent(t, a, b2.deviceID, func(e *pb.DeviceEvent) bool { return len(e.GetDevice().GetPublicKey()) > 0 })
	sent, err := a.SendText(ctx(t), conv, "m2", "two")
	if err != nil {
		t.Fatal(err)
	}
	if sent.GetSealed().GetEpoch() != 2 {
		t.Fatalf("sent with epoch %d, want 2", sent.GetSealed().GetEpoch())
	}
	wantRecipients(t, conversationKeys(t, a, conv, 0), a, b, b2)
	if got := text(t, b2, messageEvent(t, b2, conv, 2)); got != "two" {
		t.Fatalf("new device read %q", got)
	}
	msgs, err := b2.GetMessages(ctx(t), conv, 0, 0)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("history: %v %v", msgs, err)
	}
	if _, err := b2.OpenMessage(ctx(t), msgs[1]); !errors.Is(err, wrinkleclient.ErrNoKey) {
		t.Fatalf("new device opened the history before it was shared: %v", err)
	}
	if err := b.ShareHistory(ctx(t), conv, b2.deviceID, publicKey(b2)); err != nil {
		t.Fatal(err)
	}
	if got := text(t, b2, msgs[1]); got != "one" {
		t.Fatalf("shared history read %q", got)
	}

	// A device signed out: the next send leaves it out.
	if err := b.SignOutDevice(ctx(t), b2.deviceID); err != nil {
		t.Fatal(err)
	}
	deviceEvent(t, a, b2.deviceID, func(e *pb.DeviceEvent) bool { return e.GetSignedOut() })
	if sent, err := b.SendText(ctx(t), conv, "m1", "three"); err != nil || sent.GetSealed().GetEpoch() != 3 {
		t.Fatalf("sent %v %v, want epoch 3", sent, err)
	}
	wantRecipients(t, conversationKeys(t, a, conv, 0), a, b)
	if got := text(t, a, messageEvent(t, a, conv, 3)); got != "three" {
		t.Fatalf("alice read %q", got)
	}
}
