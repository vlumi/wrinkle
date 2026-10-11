package conformance

import (
	"bytes"
	"testing"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestDeleteAccount(t *testing.T) {
	a, b, conv := pair(t)
	a2 := otherDevice(t, a)
	name := a.user.GetName()
	if _, err := b.SendText(ctx(t), conv, "m1", "hi"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, a2, b} {
		messageEvent(t, d, conv, 1)
	}

	err := a.DeleteAccount(ctx(t), "not the password")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	err = a.DeleteAccount(ctx(t), "short")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	if err := a.DeleteAccount(ctx(t), password); err != nil {
		t.Fatal(err)
	}
	waitSignedOut(t, a)
	waitSignedOut(t, a2)

	// The partner sees a deleted account.
	ev := nextEvent(t, b, func(ev *pb.Event) bool {
		u := ev.GetProfile().GetUser()
		return u != nil && bytes.Equal(u.GetUserId(), a.user.GetUserId())
	})
	if u := ev.GetProfile().GetUser(); !u.GetDeleted() || u.GetName() != "" || u.GetIcon() != "" || len(u.GetAvatar()) != 0 {
		t.Fatalf("profile event %v", u)
	}
	if c := findConversation(t, b, conv); !c.GetPeer().GetDeleted() || c.GetPeer().GetName() != "" ||
		!bytes.Equal(c.GetPeer().GetUserId(), a.user.GetUserId()) {
		t.Fatalf("partner's list shows %v", c.GetPeer())
	}

	// The credentials are gone.
	for _, token := range []string{a.token, a2.token} {
		_, err = dial(t).Connect(ctx(t), token)
		wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	}
	_, err = dial(t).SignIn(ctx(t), name, password, "")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)

	// The name is free for someone else.
	taker := signUp(t, name)
	if bytes.Equal(taker.user.GetUserId(), a.user.GetUserId()) {
		t.Fatal("the name still leads to the deleted user")
	}
	found, err := b.OpenConversation(ctx(t), name)
	if err != nil || !bytes.Equal(found.GetPeer().GetUserId(), taker.user.GetUserId()) {
		t.Fatalf("open by the freed name: %v %v", found, err)
	}
	// The conversation stays with the partner.
	if msgs, err := b.GetMessages(ctx(t), conv, 0, 0); err != nil || len(msgs) != 1 {
		t.Fatalf("messages after deletion: %v %v", msgs, err)
	}
}
