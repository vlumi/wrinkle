package conformance

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// deletedEvent waits for the deletion of the message at seq.
func deletedEvent(t *testing.T, d *device, conversationID []byte, seq uint64) *pb.MessageDeletedEvent {
	t.Helper()
	ev := nextEvent(t, d, func(ev *pb.Event) bool {
		m := ev.GetMessageDeleted()
		return m != nil && bytes.Equal(m.GetConversationId(), conversationID) && m.GetSeq() == seq
	})
	deleted := ev.GetMessageDeleted()
	if deleted.GetDeletedAt() == nil {
		t.Fatalf("deletion without deleted_at: %v", ev)
	}
	return deleted
}

// syncAll returns every event after the cursor.
func syncAll(t *testing.T, d *device, cursor uint64) []*pb.Event {
	t.Helper()
	var events []*pb.Event
	for {
		resp, err := d.Sync(ctx(t), cursor, 0)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, resp.GetEvents()...)
		cursor = resp.GetCursor()
		if !resp.GetHasMore() {
			return events
		}
	}
}

// tombstone checks that the message is deleted as the event says: no
// content, and the event's deleted_at.
func tombstone(t *testing.T, what string, msg *pb.Message, deleted *pb.MessageDeletedEvent) {
	t.Helper()
	if msg.GetContent() != nil || !proto.Equal(msg.GetDeletedAt(), deleted.GetDeletedAt()) {
		t.Fatalf("%s is not deleted as of %v: %v", what, deleted.GetDeletedAt(), msg)
	}
}

func TestDeleteMessage(t *testing.T) {
	a, b, conv := pair(t)
	a2 := otherDevice(t, a)
	sent, err := a.SendMessage(ctx(t), conv, "m1", "secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, a2, b} {
		messageEvent(t, d, conv, 1)
	}
	// Another conversation, active later, stays above it in the list.
	c := newUser(t, "carol")
	other, err := b.OpenConversation(ctx(t), c.user.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SendMessage(ctx(t), other.GetConversationId(), "m1", "later"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, other.GetConversationId(), 1)

	if err := a.DeleteMessage(ctx(t), conv, 1); err != nil {
		t.Fatal(err)
	}
	var deleted *pb.MessageDeletedEvent
	for _, d := range []*device{a, a2, b} {
		got := deletedEvent(t, d, conv, 1)
		if deleted != nil && !proto.Equal(got, deleted) {
			t.Fatalf("deletions differ: %v and %v", got, deleted)
		}
		deleted = got
	}

	// The history keeps the message, without its content.
	for _, d := range []*device{a, b} {
		msgs, err := d.GetMessages(ctx(t), conv, 0, 0)
		if err != nil || len(msgs) != 1 {
			t.Fatalf("history: %v %v", msgs, err)
		}
		m := msgs[0]
		if m.GetSeq() != 1 || !bytes.Equal(m.GetSenderUserId(), a.user.GetUserId()) || m.GetClientMessageId() != "m1" ||
			!proto.Equal(m.GetSentAt(), sent.GetSentAt()) {
			t.Fatalf("deleted message in the history: %v", m)
		}
		tombstone(t, "history", m, deleted)
	}

	// A device syncing from the start gets the message without its text,
	// then the deletion.
	var copied, told bool
	for _, ev := range syncAll(t, b, 0) {
		if m := ev.GetMessage().GetMessage(); bytes.Equal(m.GetConversationId(), conv) && m.GetSeq() == 1 {
			tombstone(t, "synced message", m, deleted)
			if m.GetClientMessageId() != "m1" || !bytes.Equal(m.GetSenderUserId(), a.user.GetUserId()) {
				t.Fatalf("synced message: %v", m)
			}
			copied = true
		}
		if d := ev.GetMessageDeleted(); d != nil && bytes.Equal(d.GetConversationId(), conv) {
			if !copied || told || !proto.Equal(d, deleted) {
				t.Fatalf("synced deletion %v after the message %v, before %v", d, copied, told)
			}
			told = true
		}
	}
	if !copied || !told {
		t.Fatalf("sync has the message %v, the deletion %v", copied, told)
	}

	// The lists show the last message deleted, where they had it.
	for _, d := range []*device{a, b} {
		entry := findConversation(t, d, conv)
		if entry.GetLastSeq() != 1 || entry.GetLastMessage().GetSeq() != 1 || entry.GetLastMessage().GetClientMessageId() != "m1" {
			t.Fatalf("%s's entry: %v", d.user.GetName(), entry)
		}
		tombstone(t, "last message", entry.GetLastMessage(), deleted)
	}
	list, err := b.ListConversations(ctx(t))
	if err != nil || len(list) != 2 || !bytes.Equal(list[0].GetConversationId(), other.GetConversationId()) {
		t.Fatalf("a deletion moved the conversation in the list: %v %v", list, err)
	}

	// Deleting again does nothing: the next event is the next message.
	if err := a.DeleteMessage(ctx(t), conv, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendMessage(ctx(t), conv, "m2", "again"); err != nil {
		t.Fatal(err)
	}
	ev := nextEvent(t, b, func(*pb.Event) bool { return true })
	if m := ev.GetMessage().GetMessage(); m.GetSeq() != 2 || m.GetText() != "again" {
		t.Fatalf("event after deleting twice: %v", ev)
	}
	entry := waitConversation(t, b, conv, func(c *pb.Conversation) bool { return c.GetLastSeq() == 2 })
	if entry.GetLastMessage().GetText() != "again" || entry.GetLastMessage().GetDeletedAt() != nil {
		t.Fatalf("entry after a later message: %v", entry)
	}

	// Resending a deleted message's client id returns it deleted.
	resent, err := a.SendMessage(ctx(t), conv, "m1", "secret")
	if err != nil || resent.GetSeq() != 1 {
		t.Fatalf("resent: %v %v", resent, err)
	}
	tombstone(t, "resent message", resent, deleted)
}

func TestDeleteMessageValidation(t *testing.T) {
	a, b, conv := pair(t)
	if _, err := a.SendMessage(ctx(t), conv, "m1", "hi"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, conv, 1)

	wantCode(t, b.DeleteMessage(ctx(t), conv, 1), pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	wantCode(t, a.DeleteMessage(ctx(t), conv, 2), pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	wantCode(t, a.DeleteMessage(ctx(t), conv, 0), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	wantCode(t, a.DeleteMessage(ctx(t), conv[:8], 1), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	wantCode(t, a.DeleteMessage(ctx(t), randomID(t), 1), pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	c := newUser(t, "carol")
	wantCode(t, c.DeleteMessage(ctx(t), conv, 1), pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	wantCode(t, dial(t).DeleteMessage(ctx(t), conv, 1), pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)

	// Notices stay, even one's own.
	group := newGroup(t, a, "Notices", b)
	wantCode(t, a.DeleteMessage(ctx(t), group, 1), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	// Nothing was deleted: the next event is the next message.
	if _, err := a.SendMessage(ctx(t), conv, "m2", "still here"); err != nil {
		t.Fatal(err)
	}
	ev := nextEvent(t, b, func(*pb.Event) bool { return true })
	if ev.GetMessage().GetMessage().GetSeq() != 2 {
		t.Fatalf("event after failed deletions: %v", ev)
	}
	msgs, err := b.GetMessages(ctx(t), conv, 0, 0)
	if err != nil || len(msgs) != 2 || msgs[1].GetText() != "hi" || msgs[1].GetDeletedAt() != nil {
		t.Fatalf("history after failed deletions: %v %v", msgs, err)
	}
}

func TestDeleteMessageInGroup(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")
	d := newUser(t, "dave")
	id := newGroup(t, a, "Rowing", b, c, d)
	if _, err := b.SendMessage(ctx(t), id, "m1", "from bob"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*device{a, b, c, d} {
		messageEvent(t, m, id, 2)
	}
	if err := a.RemoveMember(ctx(t), id, d.user.GetUserId()); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*device{a, b, c, d} {
		noticeEvent(t, m, id, 3)
	}

	// Everyone in the group hears of it; the member removed meanwhile
	// does not, but reads the message deleted.
	dCursor := d.cursor
	if err := b.DeleteMessage(ctx(t), id, 2); err != nil {
		t.Fatal(err)
	}
	var deleted *pb.MessageDeletedEvent
	for _, m := range []*device{a, b, c} {
		deleted = deletedEvent(t, m, id, 2)
	}
	for _, ev := range syncAll(t, d, dCursor) {
		if ev.GetMessageDeleted() != nil {
			t.Fatalf("a removed member got %v", ev)
		}
	}
	msgs, err := d.GetMessages(ctx(t), id, 0, 0)
	if err != nil || len(msgs) != 3 || msgs[1].GetSeq() != 2 {
		t.Fatalf("removed member's history: %v %v", msgs, err)
	}
	tombstone(t, "removed member's history", msgs[1], deleted)
	// Outside what they may read is not found, inside it not theirs.
	wantCode(t, d.DeleteMessage(ctx(t), id, 4), pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	wantCode(t, d.DeleteMessage(ctx(t), id, 2), pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)

	// A member who left may still delete what they sent before.
	if _, err := c.SendMessage(ctx(t), id, "m1", "from carol"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*device{a, b, c} {
		messageEvent(t, m, id, 4)
	}
	if err := c.LeaveGroup(ctx(t), id); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*device{a, b, c} {
		noticeEvent(t, m, id, 5)
	}
	if _, err := a.SendMessage(ctx(t), id, "m1", "after carol"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*device{a, b} {
		messageEvent(t, m, id, 6)
	}
	wantCode(t, c.DeleteMessage(ctx(t), id, 6), pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	if err := c.DeleteMessage(ctx(t), id, 4); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*device{a, b, c} {
		deleted = deletedEvent(t, m, id, 4)
	}
	msgs, err = a.GetMessages(ctx(t), id, 5, 1)
	if err != nil || len(msgs) != 1 || msgs[0].GetSeq() != 4 {
		t.Fatalf("history: %v %v", msgs, err)
	}
	tombstone(t, "former member's message", msgs[0], deleted)
	entry := findConversation(t, a, id)
	if entry.GetLastSeq() != 6 || entry.GetLastMessage().GetText() != "after carol" {
		t.Fatalf("a deletion changed a later last message: %v", entry)
	}
}
