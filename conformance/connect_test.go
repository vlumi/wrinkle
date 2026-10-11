package conformance

import (
	"strings"
	"testing"
	"time"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestConnectCreatesUserAndDevice(t *testing.T) {
	name := uniqueName(t, "connect")
	d := connect(t, name)
	if d.user.GetName() != name || d.user.GetUserId() == 0 {
		t.Fatalf("bad user in response: %v", d.user)
	}
	if d.deviceID == 0 {
		t.Fatal("expected a device id")
	}
	again := connect(t, name)
	if again.user.GetUserId() != d.user.GetUserId() {
		t.Fatalf("same name gave different users: %d and %d", d.user.GetUserId(), again.user.GetUserId())
	}
	if again.deviceID == d.deviceID {
		t.Fatal("a new device got the old device id")
	}
	same := connectDevice(t, name, d.deviceID)
	if same.deviceID != d.deviceID {
		t.Fatalf("reconnecting device %d got %d", d.deviceID, same.deviceID)
	}
}

func TestConnectValidation(t *testing.T) {
	c := dial(t)
	_, err := c.Connect(ctx(t), &pb.ConnectRequest{UserName: ""})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = c.Connect(ctx(t), &pb.ConnectRequest{UserName: strings.Repeat("n", 65)})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = c.Connect(ctx(t), &pb.ConnectRequest{UserName: uniqueName(t, "dev"), DeviceName: strings.Repeat("d", 65)})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	// Names are limited in characters: 64 of them fit even when each takes four bytes.
	if _, err := c.Connect(ctx(t), &pb.ConnectRequest{UserName: strings.Repeat("😀", 51) + uniqueName(t, "")}); err != nil {
		t.Fatalf("64 characters: %v", err)
	}
}

func TestRequestsBeforeConnect(t *testing.T) {
	c := dial(t)
	_, err := c.ListConversations(ctx(t))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	_, err = c.Sync(ctx(t), 0, 0)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
}

func TestConnectTwice(t *testing.T) {
	d := connect(t, uniqueName(t, "twice"))
	_, err := d.Connect(ctx(t), &pb.ConnectRequest{UserName: uniqueName(t, "other")})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
}

func TestConnectTwiceConcurrently(t *testing.T) {
	c := dial(t)
	name := uniqueName(t, "racer")
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := c.Connect(ctx(t), &pb.ConnectRequest{UserName: name})
			errs <- err
		}()
	}
	var failed []error
	for range 2 {
		if err := <-errs; err != nil {
			failed = append(failed, err)
		}
	}
	if len(failed) != 1 {
		t.Fatalf("want exactly one of two concurrent connects to fail, got errors %v", failed)
	}
	wantCode(t, failed[0], pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
}

func TestPing(t *testing.T) {
	c := dial(t)
	if err := c.Ping(ctx(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Pongs:
	case <-ctx(t).Done():
		t.Fatal("no pong")
	}
}

func TestAckIsRememberedPerDevice(t *testing.T) {
	a, b, conv := pair(t)
	if _, err := b.SendMessage(ctx(t), conv, "m1", "hello"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, a, conv, 1)
	if err := a.Ack(ctx(t), a.cursor); err != nil {
		t.Fatal(err)
	}
	// An ack has no response, so poll reconnects until it shows.
	deadline := time.Now().Add(eventTimeout)
	for {
		again := connectDevice(t, a.user.GetName(), a.deviceID)
		got := again.deviceCursor
		again.Close()
		if got == a.cursor {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("device cursor is %d after acking %d", got, a.cursor)
		}
		time.Sleep(200 * time.Millisecond)
	}
	fresh := connect(t, a.user.GetName())
	if fresh.deviceCursor != 0 {
		t.Fatalf("new device has cursor %d", fresh.deviceCursor)
	}
	if fresh.cursor < a.cursor {
		t.Fatalf("log head %d is below the delivered event %d", fresh.cursor, a.cursor)
	}
}
