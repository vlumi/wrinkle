package conformance

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func findConversation(t *testing.T, d *device, conversationID []byte) *pb.Conversation {
	t.Helper()
	list, err := d.ListConversations(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if bytes.Equal(c.GetConversationId(), conversationID) {
			return c
		}
	}
	t.Fatalf("conversation %x not in %s's list", conversationID, d.user.GetName())
	return nil
}

func TestListConversations(t *testing.T) {
	a, b, conv := pair(t)

	// Opened but empty: visible to the opener.
	c := findConversation(t, a, conv)
	if c.GetLastSeq() != 0 || c.GetLastMessage() != nil || c.GetReadSeq() != 0 || c.GetPeerReadSeq() != 0 {
		t.Fatalf("empty conversation: %v", c)
	}

	if _, err := a.SendText(ctx(t), conv, "m1", "first"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, conv, 1)
	sent2, err := a.SendText(ctx(t), conv, "m2", "second")
	if err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, conv, 2)

	// The peer sees it after the first message, with the last message.
	c = findConversation(t, b, conv)
	if !bytes.Equal(c.GetPeer().GetUserId(), a.user.GetUserId()) || c.GetPeer().GetName() != a.user.GetName() {
		t.Fatalf("wrong peer: %v", c.GetPeer())
	}
	if c.GetLastSeq() != 2 || c.GetLastMessage().GetSeq() != 2 || text(t, b, c.GetLastMessage()) != "second" || !proto.Equal(c.GetLastMessage().GetSealed(), sent2.GetSealed()) {
		t.Fatalf("last message not reflected: %v", c)
	}
	if c.GetReadSeq() != 0 || c.GetPeerReadSeq() != 0 {
		t.Fatalf("nothing read yet: %v", c)
	}

	if err := b.MarkRead(ctx(t), conv, 2); err != nil {
		t.Fatal(err)
	}
	readEvent(t, b, conv, b.user.GetUserId(), 2)
	readEvent(t, a, conv, b.user.GetUserId(), 2)
	if c = findConversation(t, b, conv); c.GetReadSeq() != 2 {
		t.Fatalf("reader's own read_seq: %v", c)
	}
	// The sender's own position moved with each send.
	if c = findConversation(t, a, conv); c.GetPeerReadSeq() != 2 || c.GetReadSeq() != 2 {
		t.Fatalf("peer's read position: %v", c)
	}
}

func TestListOrdersByActivity(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")
	withB, err := a.OpenConversation(ctx(t), b.user.GetName())
	if err != nil {
		t.Fatal(err)
	}
	withC, err := a.OpenConversation(ctx(t), c.user.GetName())
	if err != nil {
		t.Fatal(err)
	}
	knowPeer(b, withB.GetConversationId(), a)
	knowPeer(c, withC.GetConversationId(), a)
	if _, err := b.SendText(ctx(t), withB.GetConversationId(), "m", "from b"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, a, withB.GetConversationId(), 1)
	list, err := a.ListConversations(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || !bytes.Equal(list[0].GetConversationId(), withB.GetConversationId()) || !bytes.Equal(list[1].GetConversationId(), withC.GetConversationId()) {
		t.Fatalf("expected the active conversation first: %v", list)
	}
	if _, err := c.SendText(ctx(t), withC.GetConversationId(), "m", "from c"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, a, withC.GetConversationId(), 1)
	list, err = a.ListConversations(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(list[0].GetConversationId(), withC.GetConversationId()) {
		t.Fatalf("expected the newly active conversation first: %v", list)
	}
}

func TestSendingMarksOwnMessageRead(t *testing.T) {
	a, b, conv := pair(t)
	if _, err := b.SendText(ctx(t), conv, "m1", "from b"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, a, conv, 1)
	messageEvent(t, b, conv, 1)
	if _, err := a.SendText(ctx(t), conv, "m2", "from a"); err != nil {
		t.Fatal(err)
	}
	// Once the sender's own log has the message, so has the sender's list.
	messageEvent(t, a, conv, 2)
	c := findConversation(t, a, conv)
	if c.GetLastSeq() != 2 || c.GetReadSeq() != 2 {
		t.Fatalf("after sending, the sender's list should show it read: last %d, read %d", c.GetLastSeq(), c.GetReadSeq())
	}
	messageEvent(t, b, conv, 2)
	if c := findConversation(t, b, conv); c.GetReadSeq() != 1 {
		t.Fatalf("the peer's read position moved: %d", c.GetReadSeq())
	}
}
