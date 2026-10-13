package wrinkleclient

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"errors"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/clients/go/e2ee"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

var (
	alice = bytes.Repeat([]byte{1}, 16)
	bob   = bytes.Repeat([]byte{2}, 16)
	conv  = bytes.Repeat([]byte{9}, 16)
)

// fakeServices answers the key requests from scripted state and records
// what was asked, in place of a connection.
type fakeServices struct {
	t       *testing.T
	state   *pb.GetConversationKeysResponse
	devices []*pb.UserDevices
	// members of a group; nil makes GetMembers fail as for a direct
	// conversation.
	members []*pb.Member
	// publishErrs are the outcomes of the next PublishKeys calls, in
	// order, UNSPECIFIED being success; onPublishFail runs on a failure.
	publishErrs   []pb.ErrorCode
	onPublishFail func()

	calls     []string
	published []*pb.PublishKeysRequest
}

func (f *fakeServices) answer(_ context.Context, req *pb.Request) (*pb.Response, error) {
	resp := &pb.Response{RequestId: req.GetId()}
	fail := func(code pb.ErrorCode) {
		resp.Body = &pb.Response_Error{Error: &pb.Error{Code: code, Message: code.String()}}
	}
	switch r := req.GetBody().(type) {
	case *pb.Request_GetConversationKeys:
		f.calls = append(f.calls, "keys")
		st := proto.Clone(f.state).(*pb.GetConversationKeysResponse)
		st.Keys = slices.DeleteFunc(st.Keys, func(k *pb.ReceivedKey) bool { return k.GetEpoch() <= r.GetConversationKeys.GetAfterEpoch() })
		resp.Body = &pb.Response_GetConversationKeys{GetConversationKeys: st}
	case *pb.Request_GetDeviceKeys:
		f.calls = append(f.calls, "devices")
		resp.Body = &pb.Response_GetDeviceKeys{GetDeviceKeys: &pb.GetDeviceKeysResponse{Users: f.devices}}
	case *pb.Request_GetMembers:
		f.calls = append(f.calls, "members")
		if f.members == nil {
			fail(pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
			break
		}
		resp.Body = &pb.Response_GetMembers{GetMembers: &pb.GetMembersResponse{Members: f.members}}
	case *pb.Request_ListConversations:
		f.calls = append(f.calls, "list")
		resp.Body = &pb.Response_ListConversations{ListConversations: &pb.ListConversationsResponse{}}
	case *pb.Request_PublishKeys:
		f.calls = append(f.calls, "publish")
		f.published = append(f.published, r.PublishKeys)
		if len(f.publishErrs) > 0 && f.publishErrs[0] != pb.ErrorCode_ERROR_CODE_UNSPECIFIED {
			fail(f.publishErrs[0])
			f.publishErrs = f.publishErrs[1:]
			if f.onPublishFail != nil {
				f.onPublishFail()
			}
			break
		}
		if len(f.publishErrs) > 0 {
			f.publishErrs = f.publishErrs[1:]
		}
		// The epoch is made: its recipients are the request's.
		f.state.CurrentEpoch = r.PublishKeys.GetEpoch()
		f.state.Rotate = false
		f.state.CurrentRecipients = nil
		for _, k := range r.PublishKeys.GetKeys() {
			f.state.CurrentRecipients = append(f.state.CurrentRecipients, &pb.KeyRecipient{UserId: k.GetRecipientUserId(), DeviceId: k.GetRecipientDeviceId()})
		}
		resp.Body = &pb.Response_PublishKeys{PublishKeys: &pb.PublishKeysResponse{}}
	default:
		f.t.Fatalf("unexpected request %v", req)
	}
	return resp, nil
}

// took returns the requests made since the last call.
func (f *fakeServices) took() []string {
	calls := f.calls
	f.calls = nil
	return calls
}

func generate(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	private, err := e2ee.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return private
}

func device(userID []byte, deviceID uint64, private *ecdh.PrivateKey) *pb.UserDevices {
	return &pb.UserDevices{UserId: userID, Devices: []*pb.Device{{DeviceId: deviceID, PublicKey: private.PublicKey().Bytes()}}}
}

// aliceAndBob is alice's device 1 with its keys, bob's device 2 known to
// the services, and a direct conversation between them with no epoch yet.
func aliceAndBob(t *testing.T) (*Client, *Keys, *ecdh.PrivateKey, *fakeServices) {
	t.Helper()
	alicePrivate, bobPrivate := generate(t), generate(t)
	k := NewKeys(alice, 1, alicePrivate)
	f := &fakeServices{
		t:       t,
		state:   &pb.GetConversationKeysResponse{},
		devices: []*pb.UserDevices{device(alice, 1, alicePrivate), device(bob, 2, bobPrivate)},
	}
	c := &Client{answer: f.answer}
	c.SetKeys(k)
	k.SetPeer(conv, bob)
	return c, k, bobPrivate, f
}

func wantCalls(t *testing.T, f *fakeServices, want ...string) {
	t.Helper()
	if got := f.took(); !slices.Equal(got, want) {
		t.Fatalf("requests %v, want %v", got, want)
	}
}

func TestKeysPublishFirstEpochOnce(t *testing.T) {
	c, k, bobPrivate, f := aliceAndBob(t)
	epoch, key, err := k.sendKey(t.Context(), c, conv)
	if err != nil || epoch != 1 {
		t.Fatalf("sendKey = %d, %v", epoch, err)
	}
	wantCalls(t, f, "keys", "devices", "publish")
	wraps := f.published[0].GetKeys()
	if len(wraps) != 2 || !bytes.Equal(wraps[0].GetRecipientUserId(), alice) || wraps[0].GetRecipientDeviceId() != 1 {
		t.Fatalf("published %v, want alice's own device first, then bob's", wraps)
	}
	unwrapped, err := e2ee.Unwrap(bobPrivate, k.Private.PublicKey().Bytes(), e2ee.WrapContext{
		ConversationID: conv, Epoch: 1, SenderUserID: alice, SenderDeviceID: 1, RecipientUserID: bob, RecipientDeviceID: 2,
	}, wraps[1].GetWrapped())
	if err != nil || !bytes.Equal(unwrapped, key) {
		t.Fatalf("bob's wrap: %x, %v; want the epoch key", unwrapped, err)
	}

	// Ready: the next send asks nothing.
	if _, again, err := k.sendKey(t.Context(), c, conv); err != nil || !bytes.Equal(again, key) {
		t.Fatalf("second sendKey: %x, %v", again, err)
	}
	wantCalls(t, f)
}

func TestKeysInvalidationChecksWithoutRotating(t *testing.T) {
	c, k, _, f := aliceAndBob(t)
	if _, _, err := k.sendKey(t.Context(), c, conv); err != nil {
		t.Fatal(err)
	}
	f.took()

	// Nothing changed: the check finds the epoch still right.
	k.Invalidate(conv)
	epoch, _, err := k.sendKey(t.Context(), c, conv)
	if err != nil || epoch != 1 {
		t.Fatalf("after Invalidate: %d, %v", epoch, err)
	}
	wantCalls(t, f, "keys", "devices")

	// A device event for a device already known with that key changes
	// nothing; a new device of bob's needs a new epoch wrapped for it.
	k.observe(&pb.Event{Body: &pb.Event_Device{Device: &pb.DeviceEvent{UserId: bob, Device: f.devices[1].GetDevices()[0]}}})
	if _, _, err := k.sendKey(t.Context(), c, conv); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f)
	third := generate(t)
	f.devices = append(f.devices, device(bob, 3, third))
	k.observe(&pb.Event{Body: &pb.Event_Device{Device: &pb.DeviceEvent{UserId: bob, Device: f.devices[2].GetDevices()[0]}}})
	epoch, _, err = k.sendKey(t.Context(), c, conv)
	if err != nil || epoch != 2 {
		t.Fatalf("after a new device: %d, %v", epoch, err)
	}
	wantCalls(t, f, "keys", "devices", "publish")
	if wraps := f.published[1].GetKeys(); len(wraps) != 3 || f.published[1].GetEpoch() != 2 {
		t.Fatalf("published %v, want epoch 2 for three devices", f.published[1])
	}

	// A message sealed with a newer epoch, or a notice, means checking
	// again; one at the current epoch does not.
	sealed := func(epoch uint32) *pb.Event {
		return &pb.Event{Body: &pb.Event_Message{Message: &pb.MessageEvent{Message: &pb.Message{
			ConversationId: conv, Content: &pb.Message_Sealed{Sealed: &pb.Sealed{Epoch: epoch}},
		}}}}
	}
	k.observe(sealed(2))
	if _, _, err := k.sendKey(t.Context(), c, conv); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f)
	k.observe(sealed(3))
	if _, _, err := k.sendKey(t.Context(), c, conv); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "keys", "devices")
	k.observe(&pb.Event{Body: &pb.Event_Message{Message: &pb.MessageEvent{Message: &pb.Message{
		ConversationId: conv, Content: &pb.Message_Notice{Notice: &pb.Notice{}},
	}}}})
	if _, _, err := k.sendKey(t.Context(), c, conv); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "keys", "devices")
}

func TestKeysGroupMembersFollowInvalidation(t *testing.T) {
	c, k, _, f := aliceAndBob(t)
	k = NewKeys(alice, 1, k.Private)
	c.SetKeys(k)
	f.members = []*pb.Member{{User: &pb.User{UserId: alice}}, {User: &pb.User{UserId: bob}}}
	if epoch, _, err := k.sendKey(t.Context(), c, conv); err != nil || epoch != 1 {
		t.Fatalf("sendKey = %d, %v", epoch, err)
	}
	wantCalls(t, f, "keys", "members", "devices", "publish")
	k.Invalidate(conv)
	if _, _, err := k.sendKey(t.Context(), c, conv); err != nil {
		t.Fatal(err)
	}
	wantCalls(t, f, "keys", "members", "devices")
}

func TestKeysRetryRotation(t *testing.T) {
	c, k, _, f := aliceAndBob(t)
	f.publishErrs = []pb.ErrorCode{pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION}
	epoch, _, err := k.sendKey(t.Context(), c, conv)
	if err != nil || epoch != 1 {
		t.Fatalf("sendKey = %d, %v", epoch, err)
	}
	wantCalls(t, f, "keys", "devices", "publish", "keys", "devices", "publish")

	// Another error ends the attempt.
	k.Invalidate(conv)
	f.state.Rotate = true
	f.publishErrs = []pb.ErrorCode{pb.ErrorCode_ERROR_CODE_INTERNAL}
	if _, _, err := k.sendKey(t.Context(), c, conv); Code(err) != pb.ErrorCode_ERROR_CODE_INTERNAL {
		t.Fatalf("sendKey error %v, want INTERNAL", err)
	}
	wantCalls(t, f, "keys", "devices", "publish")

	// Losing every attempt gives up.
	f.publishErrs = slices.Repeat([]pb.ErrorCode{pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION}, rotateAttempts)
	_, _, err = k.sendKey(t.Context(), c, conv)
	if Code(err) != pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION || len(f.took()) != 3*rotateAttempts {
		t.Fatalf("after %d lost attempts: %v", rotateAttempts, err)
	}
}

func TestKeysTakeTheEpochAnotherDeviceMade(t *testing.T) {
	c, k, bobPrivate, f := aliceAndBob(t)
	bobsKey := e2ee.NewEpochKey()
	wrapped, err := e2ee.Wrap(bobPrivate, k.Private.PublicKey().Bytes(), e2ee.WrapContext{
		ConversationID: conv, Epoch: 1, SenderUserID: bob, SenderDeviceID: 2, RecipientUserID: alice, RecipientDeviceID: 1,
	}, e2ee.NewNonce(), bobsKey)
	if err != nil {
		t.Fatal(err)
	}
	// Bob publishes epoch 1 first; alice's publication is refused and she
	// finds his key on the next look.
	f.publishErrs = []pb.ErrorCode{pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION}
	f.onPublishFail = func() {
		f.state = &pb.GetConversationKeysResponse{
			CurrentEpoch:      1,
			CurrentRecipients: []*pb.KeyRecipient{{UserId: alice, DeviceId: 1}, {UserId: bob, DeviceId: 2}},
			Keys:              []*pb.ReceivedKey{{Epoch: 1, SenderUserId: bob, SenderDeviceId: 2, SenderPublicKey: bobPrivate.PublicKey().Bytes(), Wrapped: wrapped}},
		}
	}
	epoch, key, err := k.sendKey(t.Context(), c, conv)
	if err != nil || epoch != 1 || !bytes.Equal(key, bobsKey) {
		t.Fatalf("sendKey = %d, %x, %v; want bob's epoch 1", epoch, key, err)
	}
	wantCalls(t, f, "keys", "devices", "publish", "keys", "devices")
	if len(f.published) != 1 {
		t.Fatalf("published %d times, want once", len(f.published))
	}
}

func TestKeysFetchEpochKeyForOpening(t *testing.T) {
	c, k, bobPrivate, f := aliceAndBob(t)
	bobsKey := e2ee.NewEpochKey()
	wrapped, err := e2ee.Wrap(bobPrivate, k.Private.PublicKey().Bytes(), e2ee.WrapContext{
		ConversationID: conv, Epoch: 3, SenderUserID: bob, SenderDeviceID: 2, RecipientUserID: alice, RecipientDeviceID: 1,
	}, e2ee.NewNonce(), bobsKey)
	if err != nil {
		t.Fatal(err)
	}
	f.state = &pb.GetConversationKeysResponse{
		CurrentEpoch: 3,
		Keys:         []*pb.ReceivedKey{{Epoch: 3, SenderUserId: bob, SenderDeviceId: 2, SenderPublicKey: bobPrivate.PublicKey().Bytes(), Wrapped: wrapped}},
	}
	if _, err := k.epochKey(t.Context(), c, conv, 0); !errors.Is(err, ErrNoKey) {
		t.Fatalf("epoch 0: %v, want ErrNoKey", err)
	}
	key, err := k.epochKey(t.Context(), c, conv, 3)
	if err != nil || !bytes.Equal(key, bobsKey) {
		t.Fatalf("epoch 3: %x, %v", key, err)
	}
	// The sender's key is checked against the directory first.
	wantCalls(t, f, "keys", "devices")
	if _, err := k.epochKey(t.Context(), c, conv, 2); !errors.Is(err, ErrNoKey) {
		t.Fatalf("epoch 2: %v, want ErrNoKey", err)
	}
	wantCalls(t, f, "keys")

	// A wrap from a device whose key differs from the one known is not
	// trusted.
	impostor := generate(t)
	wrapped, _ = e2ee.Wrap(impostor, k.Private.PublicKey().Bytes(), e2ee.WrapContext{
		ConversationID: conv, Epoch: 4, SenderUserID: bob, SenderDeviceID: 2, RecipientUserID: alice, RecipientDeviceID: 1,
	}, e2ee.NewNonce(), bobsKey)
	f.state.Keys = append(f.state.Keys, &pb.ReceivedKey{Epoch: 4, SenderUserId: bob, SenderDeviceId: 2, SenderPublicKey: impostor.PublicKey().Bytes(), Wrapped: wrapped})
	if _, err := k.epochKey(t.Context(), c, conv, 4); !errors.Is(err, ErrNoKey) {
		t.Fatalf("epoch 4 from a changed key: %v, want ErrNoKey", err)
	}
}
