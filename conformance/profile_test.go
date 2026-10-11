package conformance

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func ptr(s string) *string { return &s }

func profileEvent(t *testing.T, d *device, userID []byte, name, icon string) {
	t.Helper()
	nextEvent(t, d, func(ev *pb.Event) bool {
		u := ev.GetProfile().GetUser()
		return u != nil && bytes.Equal(u.GetUserId(), userID) && u.GetName() == name && u.GetIcon() == icon
	})
}

func TestRenameKeepsIdAndFreesOldName(t *testing.T) {
	d := newUser(t, "rename")
	oldName := d.user.GetName()
	newName := uniqueName(t, "renamed")
	user, err := d.UpdateProfile(ctx(t), ptr(newName), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(user.GetUserId(), d.user.GetUserId()) || user.GetName() != newName {
		t.Fatalf("after rename: %v", user)
	}
	if again := reconnect(t, d); again.user.GetName() != newName {
		t.Fatalf("connect says %q", again.user.GetName())
	}

	other := newUser(t, "finder")
	conv, err := other.OpenConversation(ctx(t), newName)
	if err != nil {
		t.Fatalf("open by the new name: %v", err)
	}
	if !bytes.Equal(conv.GetPeer().GetUserId(), d.user.GetUserId()) || conv.GetPeer().GetName() != newName {
		t.Fatalf("peer %v", conv.GetPeer())
	}
	// The old name is free for anyone.
	taker := signUp(t, oldName)
	if bytes.Equal(taker.user.GetUserId(), d.user.GetUserId()) {
		t.Fatal("the old name still leads to the renamed user")
	}
	conv, err = other.OpenConversation(ctx(t), oldName)
	if err != nil || !bytes.Equal(conv.GetPeer().GetUserId(), taker.user.GetUserId()) {
		t.Fatalf("open by the old name: %v %v", conv, err)
	}
	// Signing in goes by the new name.
	_, err = dial(t).SignIn(ctx(t), oldName, password+"x", "")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	signIn(t, newName, password)
}

func TestRenameCaseOnly(t *testing.T) {
	d := newUser(t, "case")
	upper := strings.ToUpper(d.user.GetName())
	user, err := d.UpdateProfile(ctx(t), ptr(upper), nil)
	if err != nil {
		t.Fatal(err)
	}
	if user.GetName() != upper {
		t.Fatalf("name %q", user.GetName())
	}
	_, err = dial(t).Register(ctx(t), d.user.GetName(), password, "")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_ALREADY_EXISTS)
	creds := signIn(t, d.user.GetName(), password)
	if again := connectToken(t, creds.GetDeviceToken()); again.user.GetName() != upper {
		t.Fatalf("connect says %q", again.user.GetName())
	}
	other := newUser(t, "finder")
	conv, err := other.OpenConversation(ctx(t), d.user.GetName())
	if err != nil || conv.GetPeer().GetName() != upper {
		t.Fatalf("open after a case-only rename: %v %v", conv, err)
	}
}

func TestRenameToTakenName(t *testing.T) {
	d := newUser(t, "renamer")
	taken := newUser(t, "taken")
	for _, name := range []string{taken.user.GetName(), strings.ToUpper(taken.user.GetName())} {
		_, err := d.UpdateProfile(ctx(t), ptr(name), nil)
		wantCode(t, err, pb.ErrorCode_ERROR_CODE_ALREADY_EXISTS)
	}
	if again := reconnect(t, d); again.user.GetName() != d.user.GetName() {
		t.Fatalf("name changed to %q", again.user.GetName())
	}
}

func TestProfileValidationAndIcon(t *testing.T) {
	d := newUser(t, "icon")
	user, err := d.UpdateProfile(ctx(t), nil, nil)
	if err != nil || user.GetName() != d.user.GetName() || user.GetIcon() != "" {
		t.Fatalf("empty update: %v %v", user, err)
	}
	_, err = d.UpdateProfile(ctx(t), nil, ptr("dragon"))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = d.UpdateProfile(ctx(t), ptr(" padded"), nil)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = d.UpdateProfile(ctx(t), ptr(""), nil)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	user, err = d.UpdateProfile(ctx(t), nil, ptr("fox"))
	if err != nil || user.GetIcon() != "fox" || user.GetName() != d.user.GetName() {
		t.Fatalf("set icon: %v %v", user, err)
	}
	if again := reconnect(t, d); again.user.GetIcon() != "fox" {
		t.Fatalf("connect says icon %q", again.user.GetIcon())
	}
	user, err = d.UpdateProfile(ctx(t), nil, ptr(""))
	if err != nil || user.GetIcon() != "" {
		t.Fatalf("clear icon: %v %v", user, err)
	}
}

func TestProfileEventReachesDevicesAndPartners(t *testing.T) {
	a, b, conv := pair(t)
	a2 := otherDevice(t, a)
	if _, err := b.SendMessage(ctx(t), conv, "m1", "hi"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, a2, b} {
		messageEvent(t, d, conv, 1)
	}

	newName := uniqueName(t, "alicia")
	if _, err := a.UpdateProfile(ctx(t), ptr(newName), ptr("owl")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, a2, b} {
		profileEvent(t, d, a.user.GetUserId(), newName, "owl")
	}
	c := findConversation(t, b, conv)
	if c.GetPeer().GetName() != newName || c.GetPeer().GetIcon() != "owl" {
		t.Fatalf("partner's list shows %v", c.GetPeer())
	}

	// The other way: the partner's change reaches a's devices and list.
	if _, err := b.UpdateProfile(ctx(t), nil, ptr("penguin")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, a2, b} {
		profileEvent(t, d, b.user.GetUserId(), b.user.GetName(), "penguin")
	}
	if c := findConversation(t, a, conv); c.GetPeer().GetIcon() != "penguin" || c.GetPeer().GetName() != b.user.GetName() {
		t.Fatalf("list shows %v", c.GetPeer())
	}

	// Sync returns the same events.
	resp, err := b.Sync(ctx(t), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	profiles := 0
	for _, ev := range resp.GetEvents() {
		if ev.GetProfile() != nil {
			profiles++
		}
	}
	if profiles != 2 {
		t.Fatalf("b's log has %d profile events, want 2", profiles)
	}
}

func TestNewConversationShowsPeerProfile(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	if _, err := b.UpdateProfile(ctx(t), nil, ptr("tiger")); err != nil {
		t.Fatal(err)
	}
	conv, err := a.OpenConversation(ctx(t), b.user.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if conv.GetPeer().GetIcon() != "tiger" {
		t.Fatalf("peer %v", conv.GetPeer())
	}
	// The peer's list gets the conversation with the first message, with
	// the opener's current profile.
	if _, err := a.UpdateProfile(ctx(t), nil, ptr("frog")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendMessage(ctx(t), conv.GetConversationId(), "m1", "hi"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, conv.GetConversationId(), 1)
	if c := findConversation(t, b, conv.GetConversationId()); c.GetPeer().GetIcon() != "frog" {
		t.Fatalf("peer %v", c.GetPeer())
	}
}

func TestOpenConversationFromBothSidesAtOnce(t *testing.T) {
	for range 5 {
		a := newUser(t, "alice")
		b := newUser(t, "bob")
		var wg sync.WaitGroup
		var fromA, fromB *pb.Conversation
		var errA, errB error
		wg.Go(func() { fromA, errA = a.OpenConversation(ctx(t), b.user.GetName()) })
		wg.Go(func() { fromB, errB = b.OpenConversation(ctx(t), a.user.GetName()) })
		wg.Wait()
		if errA != nil || errB != nil {
			t.Fatal(errA, errB)
		}
		if !bytes.Equal(fromA.GetConversationId(), fromB.GetConversationId()) {
			t.Fatalf("two conversations: %x and %x", fromA.GetConversationId(), fromB.GetConversationId())
		}
	}
}

func TestOpenConversationByNameInAnotherCase(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	first, err := a.OpenConversation(ctx(t), b.user.GetName())
	if err != nil {
		t.Fatal(err)
	}
	upper, err := a.OpenConversation(ctx(t), strings.ToUpper(b.user.GetName()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.GetConversationId(), upper.GetConversationId()) || upper.GetPeer().GetName() != b.user.GetName() {
		t.Fatalf("by another case: %v, first %v", upper, first)
	}
	_, err = a.OpenConversation(ctx(t), uniqueName(t, "nobody"))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	_, err = a.OpenConversation(ctx(t), strings.ToUpper(a.user.GetName()))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
}
