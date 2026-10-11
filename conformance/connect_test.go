package conformance

import (
	"bytes"
	"strings"
	"testing"
	"time"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestConnectWithToken(t *testing.T) {
	name := uniqueName(t, "connect")
	creds := register(t, name)
	if len(creds.GetUserId()) != 16 || creds.GetDeviceId() == 0 || creds.GetDeviceToken() == "" {
		t.Fatalf("bad credentials: %v", creds)
	}
	d := connectToken(t, creds.GetDeviceToken())
	if d.user.GetName() != name || !bytes.Equal(d.user.GetUserId(), creds.GetUserId()) || d.user.GetIcon() != "" {
		t.Fatalf("bad user in response: %v", d.user)
	}
	if d.deviceID != creds.GetDeviceId() {
		t.Fatalf("connected as device %d, registered %d", d.deviceID, creds.GetDeviceId())
	}
	same := reconnect(t, d)
	if same.deviceID != d.deviceID || !bytes.Equal(same.user.GetUserId(), d.user.GetUserId()) {
		t.Fatalf("reconnecting device %d got %d", d.deviceID, same.deviceID)
	}
	other := otherDevice(t, d)
	if !bytes.Equal(other.user.GetUserId(), d.user.GetUserId()) || other.deviceID == d.deviceID {
		t.Fatalf("sign-in gave user %x device %d", other.user.GetUserId(), other.deviceID)
	}
}

func TestDeviceNameValidation(t *testing.T) {
	c := dial(t)
	_, err := c.Register(ctx(t), uniqueName(t, "dev"), password, strings.Repeat("d", 65))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	if _, err := c.Register(ctx(t), uniqueName(t, "dev"), password, ""); err != nil {
		t.Fatalf("empty device name: %v", err)
	}
}

func TestRequestsBeforeConnect(t *testing.T) {
	c := dial(t)
	_, err := c.ListConversations(ctx(t))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	_, err = c.Sync(ctx(t), 0, 0)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	_, err = c.OpenConversation(ctx(t), uniqueName(t, "nobody"))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	_, err = c.SendMessage(ctx(t), randomID(t), "m1", "hi")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	err = c.ChangePassword(ctx(t), password, password+"!")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	name := "someone"
	_, err = c.UpdateProfile(ctx(t), &name, nil)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	err = c.SignOut(ctx(t))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	// Still usable: an account can be made and connected.
	creds, err := c.Register(ctx(t), uniqueName(t, "late"), password, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Connect(ctx(t), creds.GetDeviceToken()); err != nil {
		t.Fatal(err)
	}
}

func TestConnectTwice(t *testing.T) {
	d := newUser(t, "twice")
	_, err := d.Connect(ctx(t), d.token)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
}

func TestConnectTwiceConcurrently(t *testing.T) {
	token := register(t, uniqueName(t, "racer")).GetDeviceToken()
	c := dial(t)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := c.Connect(ctx(t), token)
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
		again := reconnect(t, a)
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
	fresh := otherDevice(t, a)
	if fresh.deviceCursor != 0 {
		t.Fatalf("new device has cursor %d", fresh.deviceCursor)
	}
	if fresh.cursor < a.cursor {
		t.Fatalf("log head %d is below the delivered event %d", fresh.cursor, a.cursor)
	}
}
