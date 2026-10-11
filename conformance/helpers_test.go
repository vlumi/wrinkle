// Conformance tests run against any backend through the contract. Point
// WRINKLE_URL at the gateway (default ws://localhost:8080/v1/ws); the
// backend must be connected to the shared infrastructure.
package conformance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

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

type device struct {
	*wrinkleclient.Client
	user     *pb.User
	deviceID uint64
	// cursor is the last event position this device has seen; it starts at
	// the log head at connect time, so only new events are expected.
	cursor uint64
	// deviceCursor is what the server remembered from acks.
	deviceCursor uint64
}

// connect dials and signs in as a new device of the named user.
func connect(t *testing.T, userName string) *device {
	t.Helper()
	return connectDevice(t, userName, 0)
}

func connectDevice(t *testing.T, userName string, deviceID uint64) *device {
	t.Helper()
	c := dial(t)
	resp, err := c.Connect(ctx(t), &pb.ConnectRequest{UserName: userName, DeviceId: deviceID, DeviceName: "conformance"})
	if err != nil {
		t.Fatalf("connect as %s: %v", userName, err)
	}
	return &device{
		Client:       c,
		user:         resp.GetUser(),
		deviceID:     resp.GetDeviceId(),
		cursor:       resp.GetLogHead(),
		deviceCursor: resp.GetDeviceCursor(),
	}
}

// pair connects two new users and opens their conversation.
func pair(t *testing.T) (a, b *device, conversationID uint64) {
	t.Helper()
	a = connect(t, uniqueName(t, "alice"))
	b = connect(t, uniqueName(t, "bob"))
	conv, err := a.OpenConversation(ctx(t), b.user.GetName())
	if err != nil {
		t.Fatalf("open conversation: %v", err)
	}
	return a, b, conv.GetConversationId()
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

func messageEvent(t *testing.T, d *device, conversationID, seq uint64) *pb.Message {
	t.Helper()
	ev := nextEvent(t, d, func(ev *pb.Event) bool {
		m := ev.GetMessage().GetMessage()
		return m != nil && m.GetConversationId() == conversationID && m.GetSeq() == seq
	})
	return ev.GetMessage().GetMessage()
}

func readEvent(t *testing.T, d *device, conversationID, userID, seq uint64) {
	t.Helper()
	nextEvent(t, d, func(ev *pb.Event) bool {
		r := ev.GetRead()
		return r != nil && r.GetConversationId() == conversationID && r.GetUserId() == userID && r.GetSeq() == seq
	})
}

func isMessage(ev *pb.Event) bool { return ev.GetMessage() != nil }

// sendMessages sends n messages with client ids m1 to mn.
func sendMessages(t *testing.T, d *device, conversationID uint64, n int, text string) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if _, err := d.SendMessage(ctx(t), conversationID, fmt.Sprintf("m%d", i), text); err != nil {
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

func wantCode(t *testing.T, err error, code pb.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got success", code)
	}
	if got := wrinkleclient.Code(err); got != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
