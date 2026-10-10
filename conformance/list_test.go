package conformance

import (
	"testing"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func findConversation(t *testing.T, d *device, conversationID uint64) *pb.Conversation {
	t.Helper()
	list, err := d.ListConversations(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.GetConversationId() == conversationID {
			return c
		}
	}
	t.Fatalf("conversation %d not in %s's list", conversationID, d.user.GetName())
	return nil
}

func TestListConversations(t *testing.T) {
	a, b, conv := pair(t)

	// Opened but empty: visible to the opener.
	c := findConversation(t, a, conv)
	if c.GetLastSeq() != 0 || c.GetLastMessage() != nil || c.GetReadSeq() != 0 || c.GetPeerReadSeq() != 0 {
		t.Fatalf("empty conversation: %v", c)
	}

	sent, err := a.SendMessage(ctx(t), conv, "m1", "first")
	if err != nil {
		t.Fatal(err)
	}
	messageEvent(b, t, conv, 1)
	sent2, err := a.SendMessage(ctx(t), conv, "m2", "second")
	if err != nil {
		t.Fatal(err)
	}
	messageEvent(b, t, conv, 2)
	_ = sent

	// The peer sees it after the first message, with the last message.
	c = findConversation(t, b, conv)
	if c.GetPeer().GetUserId() != a.user.GetUserId() || c.GetPeer().GetName() != a.user.GetName() {
		t.Fatalf("wrong peer: %v", c.GetPeer())
	}
	if c.GetLastSeq() != 2 || c.GetLastMessage().GetSeq() != 2 || c.GetLastMessage().GetText() != sent2.GetText() {
		t.Fatalf("last message not reflected: %v", c)
	}
	if c.GetReadSeq() != 0 || c.GetPeerReadSeq() != 0 {
		t.Fatalf("nothing read yet: %v", c)
	}

	if err := b.MarkRead(ctx(t), conv, 2); err != nil {
		t.Fatal(err)
	}
	readEvent(b, t, conv, b.user.GetUserId(), 2)
	readEvent(a, t, conv, b.user.GetUserId(), 2)
	if c = findConversation(t, b, conv); c.GetReadSeq() != 2 {
		t.Fatalf("reader's own read_seq: %v", c)
	}
	if c = findConversation(t, a, conv); c.GetPeerReadSeq() != 2 || c.GetReadSeq() != 0 {
		t.Fatalf("peer's read position: %v", c)
	}
}

func TestListOrdersByActivity(t *testing.T) {
	a := connect(t, uniqueName(t, "alice"))
	b := connect(t, uniqueName(t, "bob"))
	c := connect(t, uniqueName(t, "carol"))
	withB, err := a.OpenConversation(ctx(t), b.user.GetName())
	if err != nil {
		t.Fatal(err)
	}
	withC, err := a.OpenConversation(ctx(t), c.user.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.SendMessage(ctx(t), withB.GetConversationId(), "m", "from b"); err != nil {
		t.Fatal(err)
	}
	messageEvent(a, t, withB.GetConversationId(), 1)
	list, err := a.ListConversations(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].GetConversationId() != withB.GetConversationId() || list[1].GetConversationId() != withC.GetConversationId() {
		t.Fatalf("expected the active conversation first: %v", list)
	}
	if _, err := c.SendMessage(ctx(t), withC.GetConversationId(), "m", "from c"); err != nil {
		t.Fatal(err)
	}
	messageEvent(a, t, withC.GetConversationId(), 1)
	list, err = a.ListConversations(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if list[0].GetConversationId() != withC.GetConversationId() {
		t.Fatalf("expected the newly active conversation first: %v", list)
	}
}
