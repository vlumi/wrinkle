package conformance

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestRegisterNameTakenIgnoringCase(t *testing.T) {
	name := uniqueName(t, "taken")
	register(t, name)
	c := dial(t)
	for _, again := range []string{name, strings.ToUpper(name)} {
		_, err := c.Register(ctx(t), again, password, "")
		wantCode(t, err, pb.ErrorCode_ERROR_CODE_ALREADY_EXISTS)
	}
}

func TestRegisterValidation(t *testing.T) {
	c := dial(t)
	for _, name := range []string{"", strings.Repeat("n", 65), " " + uniqueName(t, "lead"), uniqueName(t, "trail") + " ",
		uniqueName(t, "ctl") + "\x07x", uniqueName(t, "nl") + "\nx", uniqueName(t, "nbsp") + "\u00a0"} {
		_, err := c.Register(ctx(t), name, password, "")
		wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	}
	for _, pw := range []string{"", "1234567", strings.Repeat("p", 257)} {
		_, err := c.Register(ctx(t), uniqueName(t, "pw"), pw, "")
		wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	}
	// Names are limited in bytes: 64 fit, of any characters but control ones.
	longest := uniqueName(t, "long")
	longest += strings.Repeat("é", (64-len(longest))/2)
	longest += strings.Repeat("x", 64-len(longest))
	if _, err := c.Register(ctx(t), longest, password, ""); err != nil {
		t.Fatalf("64 bytes: %v", err)
	}
	if _, err := c.Register(ctx(t), uniqueName(t, "inner space")+" ok", strings.Repeat("p", 8), ""); err != nil {
		t.Fatalf("inner space and an 8-byte password: %v", err)
	}
}

func TestSignIn(t *testing.T) {
	name := uniqueName(t, "signin")
	first := register(t, name)
	c := dial(t)
	_, err := c.SignIn(ctx(t), name, password+"x", "")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	_, err = c.SignIn(ctx(t), uniqueName(t, "nobody"), password, "")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	// Out of range is a wrong password too, not a malformed request.
	_, err = c.SignIn(ctx(t), name, "short", "")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)

	upper, err := c.SignIn(ctx(t), strings.ToUpper(name), password, "")
	if err != nil {
		t.Fatalf("sign in with the name in another case: %v", err)
	}
	again := signIn(t, name, password)
	if !bytes.Equal(upper.GetUserId(), first.GetUserId()) || !bytes.Equal(again.GetUserId(), first.GetUserId()) {
		t.Fatal("sign-in gave another user")
	}
	devices := map[uint64]bool{first.GetDeviceId(): true, upper.GetDeviceId(): true, again.GetDeviceId(): true}
	if len(devices) != 3 || upper.GetDeviceToken() == again.GetDeviceToken() {
		t.Fatal("each sign-in should get a new device")
	}
	d := connectToken(t, upper.GetDeviceToken())
	if d.user.GetName() != name || d.deviceID != upper.GetDeviceId() {
		t.Fatalf("connected as %v device %d", d.user, d.deviceID)
	}
}

func TestConnectWithInvalidToken(t *testing.T) {
	creds := register(t, uniqueName(t, "tokens"))
	signedOut := newUser(t, "gone")
	if err := signedOut.SignOut(ctx(t)); err != nil {
		t.Fatal(err)
	}
	signedOut.Close()

	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(creds.GetDeviceToken(), "v1."))
	wrongSecret := bytes.Clone(raw)
	wrongSecret[len(wrongSecret)-1] ^= 1
	unknown := make([]byte, len(raw))
	_, _ = rand.Read(unknown)

	c := dial(t)
	for _, token := range []string{
		"",
		"garbage",
		creds.GetDeviceToken() + "x",
		"v2." + strings.TrimPrefix(creds.GetDeviceToken(), "v1."),
		"v1." + base64.RawURLEncoding.EncodeToString(wrongSecret),
		"v1." + base64.RawURLEncoding.EncodeToString(unknown),
		signedOut.token,
	} {
		_, err := c.Connect(ctx(t), token)
		wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	}
	// The connection is still unconnected and usable.
	_, err := c.ListConversations(ctx(t))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	if _, err := c.Connect(ctx(t), creds.GetDeviceToken()); err != nil {
		t.Fatalf("connect after failures: %v", err)
	}
	if _, err := c.ListConversations(ctx(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAccountRequestsAfterConnect(t *testing.T) {
	d := newUser(t, "after")
	_, err := d.Register(ctx(t), uniqueName(t, "again"), password, "")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	_, err = d.SignIn(ctx(t), d.user.GetName(), password, "")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	_, err = d.Connect(ctx(t), d.token)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
}

func TestChangePassword(t *testing.T) {
	d := newUser(t, "pw")
	other := otherDevice(t, d)
	newPassword := "a new password"

	err := d.ChangePassword(ctx(t), "not the password", newPassword)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	err = d.ChangePassword(ctx(t), password, "short")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	if err := d.ChangePassword(ctx(t), password, newPassword); err != nil {
		t.Fatal(err)
	}

	// The other device is signed out; this one is not.
	_, err = dial(t).Connect(ctx(t), other.token)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	reconnect(t, d)

	_, err = dial(t).SignIn(ctx(t), d.user.GetName(), password, "")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	creds := signIn(t, d.user.GetName(), newPassword)
	connectToken(t, creds.GetDeviceToken())
}

func TestSignOut(t *testing.T) {
	d := newUser(t, "signout")
	other := otherDevice(t, d)
	if err := d.SignOut(ctx(t)); err != nil {
		t.Fatal(err)
	}
	d.Close()
	_, err := dial(t).Connect(ctx(t), d.token)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	// Only that device.
	reconnect(t, other)
}

func TestIdsAre16Bytes(t *testing.T) {
	a, b, conv := pair(t)
	if len(a.user.GetUserId()) != 16 || len(b.user.GetUserId()) != 16 || len(conv) != 16 {
		t.Fatalf("ids of %d, %d and %d bytes", len(a.user.GetUserId()), len(b.user.GetUserId()), len(conv))
	}
	msg, err := a.SendMessage(ctx(t), conv, "m1", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(msg.GetConversationId(), conv) || !bytes.Equal(msg.GetSenderUserId(), a.user.GetUserId()) {
		t.Fatalf("message ids: %x %x", msg.GetConversationId(), msg.GetSenderUserId())
	}
}
