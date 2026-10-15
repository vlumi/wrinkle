package conformance

import (
	"bytes"
	"sync"
	"testing"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestOpenNotes(t *testing.T) {
	a := newUser(t, "alice")
	a2 := otherDevice(t, a)
	b := newUser(t, "bob")

	// Two devices opening at once find one.
	var wg sync.WaitGroup
	convs := make([]*pb.Conversation, 2)
	errs := make([]error, 2)
	for i, d := range []*device{a, a2} {
		wg.Go(func() { convs[i], errs[i] = d.OpenNotes(ctx(t)) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	notes := convs[0]
	if !bytes.Equal(convs[1].GetConversationId(), notes.GetConversationId()) {
		t.Fatalf("two devices opened different notes: %x and %x", notes.GetConversationId(), convs[1].GetConversationId())
	}
	if notes.GetKind() != pb.ConversationKind_CONVERSATION_KIND_NOTES || notes.GetPeer() != nil ||
		notes.GetMemberCount() != 1 || !notes.GetMember() || notes.GetAdmin() || notes.GetTopic() != "" || notes.GetLastSeq() != 0 {
		t.Fatalf("not empty notes: %v", notes)
	}
	again, err := a.OpenNotes(ctx(t))
	if err != nil || !bytes.Equal(again.GetConversationId(), notes.GetConversationId()) {
		t.Fatalf("reopening changed the notes: %v %v", again, err)
	}
	other, err := b.OpenNotes(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(other.GetConversationId(), notes.GetConversationId()) {
		t.Fatal("two users share notes")
	}
	list, err := a.ListConversations(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !bytes.Equal(list[0].GetConversationId(), notes.GetConversationId()) ||
		list[0].GetKind() != pb.ConversationKind_CONVERSATION_KIND_NOTES {
		t.Fatalf("list should hold the notes: %v", list)
	}
}

func TestNotesReachTheUsersDevices(t *testing.T) {
	a := newUser(t, "alice")
	a2 := otherDevice(t, a)
	notes, err := a.OpenNotes(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	conv := notes.GetConversationId()
	if _, err := a.SendText(ctx(t), conv, "n1", "remember the milk"); err != nil {
		t.Fatal(err)
	}
	if got := text(t, a2, messageEvent(t, a2, conv, 1)); got != "remember the milk" {
		t.Fatalf("other device read %q", got)
	}
	messageEvent(t, a, conv, 1)

	// The other device writes back, with the epoch the first one made.
	if _, err := a2.SendText(ctx(t), conv, "n2", "and eggs"); err != nil {
		t.Fatal(err)
	}
	if got := text(t, a, messageEvent(t, a, conv, 2)); got != "and eggs" {
		t.Fatalf("first device read %q", got)
	}
	if err := a.SetReactions(ctx(t), conv, 2, []string{"👍"}); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteMessage(ctx(t), conv, 1); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, a2, func(ev *pb.Event) bool {
		d := ev.GetMessageDeleted()
		return d != nil && bytes.Equal(d.GetConversationId(), conv) && d.GetSeq() == 1
	})

	list, err := a2.ListConversations(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].GetLastSeq() != 2 || list[0].GetReadSeq() != 2 {
		t.Fatalf("list after two notes: %v", list)
	}
}

func TestNotesAreNotAGroup(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	befriend(t, a, b)
	notes, err := a.OpenNotes(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	conv := notes.GetConversationId()
	wantCode(t, a.AddMembers(ctx(t), conv, []string{b.user.GetName()}), pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	_, err = a.GetMembers(ctx(t), conv)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)

	// Nobody else reads or writes them.
	if _, err := a.SendText(ctx(t), conv, "n1", "mine"); err != nil {
		t.Fatal(err)
	}
	_, err = b.SendMessage(ctx(t), conv, "x", junkSealed(1))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	_, err = b.GetMessages(ctx(t), conv, 0, 10)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
}
