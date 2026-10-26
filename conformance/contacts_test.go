package conformance

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vlumi/wrinkle/clients/go/e2ee"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	stateNone     = pb.ContactState_CONTACT_STATE_UNSPECIFIED
	stateContact  = pb.ContactState_CONTACT_STATE_CONTACT
	stateIncoming = pb.ContactState_CONTACT_STATE_INCOMING
	stateOutgoing = pb.ContactState_CONTACT_STATE_OUTGOING
	stateBlocked  = pb.ContactState_CONTACT_STATE_BLOCKED
)

// wantContacts checks d's contact list: exactly these users, in these
// states.
func wantContacts(t *testing.T, d *device, want map[*device]pb.ContactState) {
	t.Helper()
	list, err := d.ListContacts(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != len(want) {
		t.Fatalf("%s's contacts: %v, want %d", d.user.GetName(), list, len(want))
	}
	for other, state := range want {
		found := false
		for _, c := range list {
			if sameUser(c.GetUser(), other) {
				found = true
				if c.GetState() != state || c.GetUser().GetName() != other.user.GetName() || c.GetUpdatedAt() == nil {
					t.Fatalf("%s's contact %s: %v, want %v", d.user.GetName(), other.user.GetName(), c, state)
				}
			}
		}
		if !found {
			t.Fatalf("%s's contacts lack %s: %v", d.user.GetName(), other.user.GetName(), list)
		}
	}
}

func TestContactRequestAndAccept(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")

	asked, err := a.AddContact(ctx(t), strings.ToUpper(b.user.GetName()))
	if err != nil {
		t.Fatal(err)
	}
	if asked.GetState() != stateOutgoing || !sameUser(asked.GetUser(), b) || asked.GetUser().GetName() != b.user.GetName() {
		t.Fatalf("request: %v", asked)
	}
	if c := contactEvent(t, a, b, stateOutgoing); c.GetUser().GetName() != b.user.GetName() {
		t.Fatalf("requester's event: %v", c)
	}
	if c := contactEvent(t, b, a, stateIncoming); c.GetUser().GetName() != a.user.GetName() {
		t.Fatalf("other's event: %v", c)
	}
	wantContacts(t, a, map[*device]pb.ContactState{b: stateOutgoing})
	wantContacts(t, b, map[*device]pb.ContactState{a: stateIncoming})

	accepted, err := b.AddContactID(ctx(t), a.user.GetUserId())
	if err != nil {
		t.Fatal(err)
	}
	if accepted.GetState() != stateContact || !sameUser(accepted.GetUser(), a) {
		t.Fatalf("acceptance: %v", accepted)
	}
	contactEvent(t, a, b, stateContact)
	contactEvent(t, b, a, stateContact)
	wantContacts(t, a, map[*device]pb.ContactState{b: stateContact})
	wantContacts(t, b, map[*device]pb.ContactState{a: stateContact})
}

func TestMutualContactRequests(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	if _, err := a.AddContact(ctx(t), b.user.GetName()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddContact(ctx(t), a.user.GetName()); err != nil {
		t.Fatal(err)
	}
	contactEvent(t, a, b, stateContact)
	contactEvent(t, b, a, stateContact)
	wantContacts(t, a, map[*device]pb.ContactState{b: stateContact})

	// Asking again changes nothing.
	again, err := a.AddContact(ctx(t), b.user.GetName())
	if err != nil || again.GetState() != stateContact {
		t.Fatalf("asking again: %v %v", again, err)
	}

	_, err = a.AddContact(ctx(t), a.user.GetName())
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = a.AddContactID(ctx(t), a.user.GetUserId())
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = a.AddContact(ctx(t), uniqueName(t, "nobody"))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	_, err = a.AddContactID(ctx(t), randomID(t))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	_, err = a.AddContactID(ctx(t), b.user.GetUserId()[:8])
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
}

func TestRemoveContact(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")
	d := newUser(t, "dave")

	// Removing a contact ends it for both.
	befriend(t, a, b)
	if err := a.RemoveContact(ctx(t), b.user.GetUserId()); err != nil {
		t.Fatal(err)
	}
	contactEvent(t, a, b, stateNone)
	contactEvent(t, b, a, stateNone)
	wantContacts(t, a, nil)
	wantContacts(t, b, nil)

	// Declining ends the requester's request.
	if _, err := c.AddContact(ctx(t), a.user.GetName()); err != nil {
		t.Fatal(err)
	}
	contactEvent(t, a, c, stateIncoming)
	if err := a.RemoveContact(ctx(t), c.user.GetUserId()); err != nil {
		t.Fatal(err)
	}
	contactEvent(t, c, a, stateNone)
	contactEvent(t, a, c, stateNone)
	wantContacts(t, c, nil)
	wantContacts(t, a, nil)

	// Withdrawing ends the other's incoming request.
	if _, err := a.AddContact(ctx(t), d.user.GetName()); err != nil {
		t.Fatal(err)
	}
	contactEvent(t, d, a, stateIncoming)
	if err := a.RemoveContact(ctx(t), d.user.GetUserId()); err != nil {
		t.Fatal(err)
	}
	contactEvent(t, d, a, stateNone)
	wantContacts(t, d, nil)
	wantContacts(t, a, nil)

	// Nothing to end, unknown users and oneself change nothing.
	for _, id := range [][]byte{randomID(t), a.user.GetUserId()} {
		if err := a.RemoveContact(ctx(t), id); err != nil {
			t.Fatalf("remove %x: %v", id, err)
		}
	}
	wantCode(t, a.RemoveContact(ctx(t), nil), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	wantCode(t, a.RemoveContact(ctx(t), b.user.GetUserId()[:15]), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
}

func TestBlockUser(t *testing.T) {
	a, b, conv := pair(t)
	first, err := a.SendText(ctx(t), conv, "m1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, conv, first.GetSeq())
	befriend(t, a, b)

	// Bob blocks Alice: the contact ends for both, and only Bob sees a
	// block.
	if err := b.BlockUser(ctx(t), a.user.GetUserId()); err != nil {
		t.Fatal(err)
	}
	contactEvent(t, b, a, stateBlocked)
	contactEvent(t, a, b, stateNone)
	wantContacts(t, b, map[*device]pb.ContactState{a: stateBlocked})
	wantContacts(t, a, nil)
	if err := b.BlockUser(ctx(t), a.user.GetUserId()); err != nil {
		t.Fatalf("blocking again: %v", err)
	}
	wantCode(t, b.BlockUser(ctx(t), b.user.GetUserId()), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	wantCode(t, b.BlockUser(ctx(t), randomID(t)), pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	wantCode(t, b.BlockUser(ctx(t), a.user.GetUserId()[:4]), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	// The blocked user is refused, without being told why.
	denied := pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED
	_, err = a.AddContact(ctx(t), b.user.GetName())
	wantCode(t, err, denied)
	_, err = a.OpenConversation(ctx(t), b.user.GetName())
	wantCode(t, err, denied)
	_, err = a.SendText(ctx(t), conv, "m2", "anyone?")
	wantCode(t, err, denied)
	wantCode(t, a.SetReaction(ctx(t), conv, first.GetSeq(), junkSealed(1)), denied)
	wantCode(t, publish(t, a, conv, 2, e2ee.NewEpochKey(), a, b), denied)
	// A read still counts, but tells only the reader's own devices.
	if err := a.MarkRead(ctx(t), conv, first.GetSeq()); err != nil {
		t.Fatal(err)
	}

	// The blocker is told to unblock first.
	fp := pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
	_, err = b.OpenConversation(ctx(t), a.user.GetName())
	wantCode(t, err, fp)
	_, err = b.SendText(ctx(t), conv, "m1", "go away")
	wantCode(t, err, fp)
	_, err = b.AddContact(ctx(t), a.user.GetName())
	wantCode(t, err, fp)
	wantCode(t, publish(t, b, conv, 2, e2ee.NewEpochKey(), a, b), fp)

	// Unblocking restores messaging, not the contact.
	if err := b.UnblockUser(ctx(t), a.user.GetUserId()); err != nil {
		t.Fatal(err)
	}
	contactEvent(t, b, a, stateNone)
	if err := b.UnblockUser(ctx(t), a.user.GetUserId()); err != nil {
		t.Fatalf("unblocking again: %v", err)
	}
	if _, err := a.OpenConversation(ctx(t), b.user.GetName()); err != nil {
		t.Fatal(err)
	}
	again, err := a.SendText(ctx(t), conv, "m2", "hello again")
	if err != nil {
		t.Fatal(err)
	}
	// The read while blocked never reached Bob: his next event is this.
	if ev := nextEvent(t, b, func(*pb.Event) bool { return true }); ev.GetMessage().GetMessage().GetSeq() != again.GetSeq() {
		t.Fatalf("Bob's event after the block: %v", ev)
	}
	if _, err := b.SendText(ctx(t), conv, "m1", "hi"); err != nil {
		t.Fatal(err)
	}
	wantContacts(t, a, nil)
	wantContacts(t, b, nil)
}

func TestGroupsTakeContacts(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")

	given := strings.ToUpper(b.user.GetName())
	_, err := a.CreateGroup(ctx(t), "Strangers", []string{given}, uniqueName(t, "group"))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	if !strings.Contains(err.Error(), given) {
		t.Fatalf("the refusal does not name %s: %v", given, err)
	}
	// Asking is not enough.
	if _, err := a.AddContact(ctx(t), c.user.GetName()); err != nil {
		t.Fatal(err)
	}
	contactEvent(t, a, c, stateOutgoing)
	contactEvent(t, c, a, stateIncoming)

	id := newGroup(t, a, "Friends", b)
	err = a.AddMembers(ctx(t), id, []string{c.user.GetName()})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	if !strings.Contains(err.Error(), c.user.GetName()) {
		t.Fatalf("the refusal does not name %s: %v", c.user.GetName(), err)
	}
	if _, err := c.AddContact(ctx(t), a.user.GetName()); err != nil {
		t.Fatal(err)
	}
	contactEvent(t, a, c, stateContact)
	contactEvent(t, c, a, stateContact)
	if err := a.AddMembers(ctx(t), id, []string{c.user.GetName()}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, b, c} {
		noticeEvent(t, d, id, 2)
	}

	// A blocked user in a shared group still sends there, and the blocker
	// still receives it: hiding it is the client's.
	if err := c.BlockUser(ctx(t), b.user.GetUserId()); err != nil {
		t.Fatal(err)
	}
	contactEvent(t, c, b, stateBlocked)
	msg, err := b.SendText(ctx(t), id, "m1", "still here")
	if err != nil {
		t.Fatal(err)
	}
	if got := messageEvent(t, c, id, msg.GetSeq()); text(t, c, got) != "still here" {
		t.Fatalf("blocker's copy: %v", got)
	}
}

func TestGroupRetryAfterContactEnds(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	befriend(t, a, b)
	clientID := uniqueName(t, "group")
	conv, err := a.CreateGroup(ctx(t), "Once", []string{b.user.GetName()}, clientID)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveContact(ctx(t), b.user.GetUserId()); err != nil {
		t.Fatal(err)
	}
	contactEvent(t, a, b, stateNone)
	contactEvent(t, b, a, stateNone)

	// A retry returns the group created the first time; only a new group
	// takes contacts.
	again, err := a.CreateGroup(ctx(t), "Once", []string{b.user.GetName()}, clientID)
	if err != nil || !bytes.Equal(again.GetConversationId(), conv.GetConversationId()) {
		t.Fatalf("retry: %v %v", again, err)
	}
	_, err = a.CreateGroup(ctx(t), "Twice", []string{b.user.GetName()}, uniqueName(t, "group"))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
}
