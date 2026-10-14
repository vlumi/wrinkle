// Conformance tests run against any backend through the contract. Point
// WRINKLE_URL at the gateway (default ws://localhost:8080/v1/ws); the
// backend must be connected to the shared infrastructure.
package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	callTimeout  = 10 * time.Second
	eventTimeout = 15 * time.Second
)

func gatewayURL() string {
	if u := os.Getenv("WRINKLE_URL"); u != "" {
		return u
	}
	return "ws://localhost:8080/v1/ws"
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), callTimeout)
	t.Cleanup(cancel)
	return c
}

// uniqueName makes a user name no earlier run has used, so tests do not
// see each other's data.
func uniqueName(t *testing.T, prefix string) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

func dial(t *testing.T) *wrinkleclient.Client {
	t.Helper()
	c, err := wrinkleclient.Dial(ctx(t), gatewayURL())
	if err != nil {
		t.Fatalf("dial %s: %v", gatewayURL(), err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// password is every test account's, unless a test says otherwise.
const password = "conformance password"

type device struct {
	*wrinkleclient.Client
	user     *pb.User
	deviceID uint64
	token    string
	// cursor is the last event position this device has seen; it starts at
	// the log head at connect time, so only new events are expected.
	cursor uint64
	// deviceCursor is what the server remembered from acks.
	deviceCursor uint64
}

// register creates an account on a connection of its own, and returns the
// device's credentials.
func register(t *testing.T, name string) *pb.DeviceCredentials {
	t.Helper()
	creds, err := dial(t).Register(ctx(t), name, password, "conformance")
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	return creds
}

// signIn signs a new device in to the account, on a connection of its own.
func signIn(t *testing.T, name, pw string) *pb.DeviceCredentials {
	t.Helper()
	creds, err := dial(t).SignIn(ctx(t), name, pw, "conformance")
	if err != nil {
		t.Fatalf("sign in as %s: %v", name, err)
	}
	return creds
}

// connectToken dials and connects with a device token.
func connectToken(t *testing.T, token string) *device {
	t.Helper()
	c := dial(t)
	resp, err := c.Connect(ctx(t), token)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return &device{
		Client:       c,
		user:         resp.GetUser(),
		deviceID:     resp.GetDeviceId(),
		token:        token,
		cursor:       resp.GetLogHead(),
		deviceCursor: resp.GetDeviceCursor(),
	}
}

// signUp registers a new user and connects its first device, once the
// registration's DeviceEvent is in the log, so that the tests see only the
// events they cause.
func signUp(t *testing.T, name string) *device {
	t.Helper()
	token := register(t, name).GetDeviceToken()
	deadline := time.Now().Add(eventTimeout)
	for {
		c := dial(t)
		resp, err := c.Connect(ctx(t), token)
		c.Close()
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		if resp.GetLogHead() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the registration's event is not in the log after %s", eventTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return withKey(t, connectToken(t, token))
}

// withKey gives a newly connected device a key pair, as clients do on
// their first connect, and consumes the device's own event for it.
func withKey(t *testing.T, d *device) *device {
	t.Helper()
	k, err := d.EnableKeys(ctx(t), d.user.GetUserId(), d.deviceID)
	if err != nil {
		t.Fatalf("set device key: %v", err)
	}
	pub := k.Private.PublicKey().Bytes()
	nextEvent(t, d, func(ev *pb.Event) bool {
		e := ev.GetDevice()
		return e.GetDevice().GetDeviceId() == d.deviceID && bytes.Equal(e.GetDevice().GetPublicKey(), pub)
	})
	return d
}

// newUser signs up a user with a unique name.
func newUser(t *testing.T, prefix string) *device {
	t.Helper()
	return signUp(t, uniqueName(t, prefix))
}

// otherDevice signs in and connects another device of d's user.
func otherDevice(t *testing.T, d *device) *device {
	t.Helper()
	return withKey(t, connectToken(t, signIn(t, d.user.GetName(), password).GetDeviceToken()))
}

// otherDeviceWithoutKey signs in another device that sets no key.
func otherDeviceWithoutKey(t *testing.T, d *device) *device {
	t.Helper()
	return connectToken(t, signIn(t, d.user.GetName(), password).GetDeviceToken())
}

// reconnect connects d's device again on a new connection.
func reconnect(t *testing.T, d *device) *device {
	t.Helper()
	again := connectToken(t, d.token)
	again.SetKeys(d.Keys())
	return again
}

// pair signs up two users and opens their conversation.
func pair(t *testing.T) (a, b *device, conversationID []byte) {
	t.Helper()
	a = newUser(t, "alice")
	b = newUser(t, "bob")
	conv, err := a.OpenConversation(ctx(t), b.user.GetName())
	if err != nil {
		t.Fatalf("open conversation: %v", err)
	}
	knowPeer(b, conv.GetConversationId(), a)
	return a, b, conv.GetConversationId()
}

// knowPeer tells d's keys who the other user of a direct conversation is,
// as a client knows when it shows the conversation; until its first
// message only the user who opened it has it listed.
func knowPeer(d *device, conversationID []byte, peer *device) {
	d.Keys().SetPeer(conversationID, peer.user.GetUserId())
}

// randomID is a 16-byte id nothing has.
func randomID(t *testing.T) []byte {
	t.Helper()
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return id
}

// nextEvent waits for the next pushed event that matches.
func nextEvent(t *testing.T, d *device, match func(*pb.Event) bool) *pb.Event {
	t.Helper()
	deadline := time.After(eventTimeout)
	for {
		select {
		case ev, ok := <-d.Events:
			if !ok {
				t.Fatalf("connection closed while waiting for an event: %v", d.Err())
			}
			if ev.GetCursor() <= d.cursor {
				t.Fatalf("event cursor %d not above the previous %d", ev.GetCursor(), d.cursor)
			}
			d.cursor = ev.GetCursor()
			if match(ev) {
				return ev
			}
		case <-deadline:
			t.Fatalf("no matching event within %s", eventTimeout)
		}
	}
}

func messageEvent(t *testing.T, d *device, conversationID []byte, seq uint64) *pb.Message {
	t.Helper()
	ev := nextEvent(t, d, func(ev *pb.Event) bool {
		m := ev.GetMessage().GetMessage()
		return m != nil && bytes.Equal(m.GetConversationId(), conversationID) && m.GetSeq() == seq
	})
	return ev.GetMessage().GetMessage()
}

func readEvent(t *testing.T, d *device, conversationID, userID []byte, seq uint64) {
	t.Helper()
	nextEvent(t, d, func(ev *pb.Event) bool {
		r := ev.GetRead()
		return r != nil && bytes.Equal(r.GetConversationId(), conversationID) && bytes.Equal(r.GetUserId(), userID) && r.GetSeq() == seq
	})
}

func isMessage(ev *pb.Event) bool { return ev.GetMessage() != nil }

func equalIDs(a, b [][]byte) bool {
	return slices.EqualFunc(a, b, bytes.Equal)
}

// text opens a message's sealed content on the device.
func text(t *testing.T, d *device, m *pb.Message) string {
	t.Helper()
	s, err := d.MessageText(ctx(t), m)
	if err != nil {
		t.Fatalf("open message %d: %v", m.GetSeq(), err)
	}
	return s
}

// junkSealed is well-formed sealed content no device can open, for
// requests that fail before the content matters.
func junkSealed(epoch uint32) *pb.Sealed {
	return &pb.Sealed{Epoch: epoch, Nonce: make([]byte, 12), Ciphertext: make([]byte, 32)}
}

// sendMessages sends n messages with client ids m1 to mn.
func sendMessages(t *testing.T, d *device, conversationID []byte, n int, text string) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if _, err := d.SendText(ctx(t), conversationID, fmt.Sprintf("m%d", i), text); err != nil {
			t.Fatal(err)
		}
	}
}

// noEvent asserts nothing arrives for a while.
func noEvent(t *testing.T, d *device, wait time.Duration) {
	t.Helper()
	select {
	case ev := <-d.Events:
		t.Fatalf("unexpected event: %v", ev)
	case <-time.After(wait):
	}
}

const (
	// signedOutCode closes the connections of a device signed out.
	signedOutCode websocket.StatusCode = 4001
	// signOutWithin is how soon they must close.
	signOutWithin = 5 * time.Second
)

// waitSignedOut waits for the server to close the connection as signed
// out.
func waitSignedOut(t *testing.T, d *device) {
	t.Helper()
	deadline := time.After(signOutWithin)
	for {
		select {
		case _, ok := <-d.Events:
			if !ok {
				if got := websocket.CloseStatus(d.Err()); got != signedOutCode {
					t.Fatalf("connection closed with %v (%v), want %v", got, d.Err(), signedOutCode)
				}
				return
			}
		case <-deadline:
			t.Fatalf("connection still open after %s", signOutWithin)
		}
	}
}

func wantCode(t *testing.T, err error, code pb.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got success", code)
	}
	if got := wrinkleclient.Code(err); got != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

// befriend makes two users contacts, as groups need: a asks, then b
// accepts, each step's ContactEvents awaited on both, so that the tests
// see only the events they cause. Users already contacts stay as they are.
func befriend(t *testing.T, a, b *device) {
	t.Helper()
	if contactState(t, a, b) == pb.ContactState_CONTACT_STATE_CONTACT {
		return
	}
	if _, err := a.AddContact(ctx(t), b.user.GetName()); err != nil {
		t.Fatalf("%s asks %s: %v", a.user.GetName(), b.user.GetName(), err)
	}
	contactEvent(t, a, b, pb.ContactState_CONTACT_STATE_OUTGOING)
	contactEvent(t, b, a, pb.ContactState_CONTACT_STATE_INCOMING)
	if _, err := b.AddContact(ctx(t), a.user.GetName()); err != nil {
		t.Fatalf("%s accepts %s: %v", b.user.GetName(), a.user.GetName(), err)
	}
	contactEvent(t, a, b, pb.ContactState_CONTACT_STATE_CONTACT)
	contactEvent(t, b, a, pb.ContactState_CONTACT_STATE_CONTACT)
}

// contactEvent waits for d's ContactEvent about the other user in the
// given state.
func contactEvent(t *testing.T, d, other *device, state pb.ContactState) *pb.Contact {
	t.Helper()
	ev := nextEvent(t, d, func(ev *pb.Event) bool {
		c := ev.GetContact().GetContact()
		return c != nil && sameUser(c.GetUser(), other) && c.GetState() == state
	})
	return ev.GetContact().GetContact()
}

// contactState is the other user's state in d's contact list.
func contactState(t *testing.T, d, other *device) pb.ContactState {
	t.Helper()
	list, err := d.ListContacts(ctx(t))
	if err != nil {
		t.Fatalf("list contacts: %v", err)
	}
	for _, c := range list {
		if sameUser(c.GetUser(), other) {
			return c.GetState()
		}
	}
	return pb.ContactState_CONTACT_STATE_UNSPECIFIED
}
