package conformance

import (
	"context"
	"encoding/binary"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/tsuna/gohbase"
	"github.com/tsuna/gohbase/hrpc"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// hotWindow is how long the backend keeps messages in MySQL before moving
// them to the cold tier, as run.sh starts it.
const hotWindow = 20 * time.Second

// waitCold waits until the cold tier holds the message at seq.
func waitCold(t *testing.T, conversationID []byte, seq uint64) {
	t.Helper()
	quorum := os.Getenv("WRINKLE_HBASE")
	if quorum == "" {
		quorum = "hbase.localhost:2181"
	}
	client := gohbase.NewClient(quorum)
	defer client.Close()
	key := binary.BigEndian.AppendUint64(slices.Clone(conversationID), seq)
	deadline := time.Now().Add(2*hotWindow + eventTimeout)
	for {
		c, cancel := context.WithTimeout(ctx(t), 5*time.Second)
		get, err := hrpc.NewGet(c, []byte("message"), key)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Get(get)
		cancel()
		if err == nil && len(res.Cells) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("message %d not in the cold tier %s after the hot window (%v)", seq, 2*hotWindow+eventTimeout, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// seqs is the messages' seqs in order.
func seqs(msgs []*pb.Message) []uint64 {
	var s []uint64
	for _, m := range msgs {
		s = append(s, m.GetSeq())
	}
	return s
}

func TestHistoryAcrossTheColdTier(t *testing.T) {
	// Parallel, so the wait for the hot window overlaps the log retention's.
	t.Parallel()
	a, b, conv := pair(t)
	for i, s := range []string{"one", "two", "three"} {
		if _, err := a.SendText(ctx(t), conv, "c"+s, s); err != nil {
			t.Fatal(err)
		}
		messageEvent(t, b, conv, uint64(i+1))
	}
	bob := b.user.GetUserId()
	if err := b.SetReactions(ctx(t), conv, 2, []string{"👍"}); err != nil {
		t.Fatal(err)
	}
	reactionEvent(t, a, conv, 2, bob)
	waitCold(t, conv, 3)

	// With the whole conversation cold, the list still has its last message.
	entry := waitConversation(t, b, conv, func(c *pb.Conversation) bool { return c.GetLastSeq() == 3 })
	if text(t, b, entry.GetLastMessage()) != "three" {
		t.Fatalf("entry of a cold conversation: %v", entry)
	}

	for i, s := range []string{"four", "five"} {
		if _, err := a.SendText(ctx(t), conv, "c"+s, s); err != nil {
			t.Fatal(err)
		}
		messageEvent(t, b, conv, uint64(i+4))
	}
	msgs, err := b.GetMessages(ctx(t), conv, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs(msgs); !slices.Equal(got, []uint64{5, 4, 3, 2, 1}) {
		t.Fatalf("history across the watermark: %v", got)
	}
	for i, want := range []string{"five", "four", "three", "two", "one"} {
		if got := text(t, b, msgs[i]); got != want {
			t.Fatalf("message %d reads %q, want %q", msgs[i].GetSeq(), got, want)
		}
	}
	rs := msgs[3].GetReactions()
	if len(rs) != 1 || !slices.Equal(emoji(t, a, conv, 2, bob, rs[0].GetSealed()), []string{"👍"}) {
		t.Fatalf("reactions to the cold message 2: %v", rs)
	}

	// Pages cross the watermark wherever they start.
	for _, p := range []struct {
		before uint64
		want   []uint64
	}{{5, []uint64{4, 3}}, {4, []uint64{3, 2}}, {2, []uint64{1}}, {1, nil}} {
		page, err := b.GetMessages(ctx(t), conv, p.before, 2)
		if err != nil {
			t.Fatal(err)
		}
		if got := seqs(page); !slices.Equal(got, p.want) {
			t.Fatalf("page before %d: %v, want %v", p.before, got, p.want)
		}
	}

	// Reactions to cold messages are set, replaced and removed there.
	if err := b.SetReactions(ctx(t), conv, 1, []string{"🎉"}); err != nil {
		t.Fatal(err)
	}
	set := reactionEvent(t, a, conv, 1, bob)
	if rs := reactionsTo(t, a, conv, 1); len(rs) != 1 {
		t.Fatalf("reactions to the cold message 1: %v", rs)
	} else {
		sameReaction(t, rs[0], set)
	}
	if err := b.SetReactions(ctx(t), conv, 2, nil); err != nil {
		t.Fatal(err)
	}
	reactionEvent(t, a, conv, 2, bob)
	if rs := reactionsTo(t, a, conv, 2); len(rs) != 0 {
		t.Fatalf("reactions to the cold message 2 after removal: %v", rs)
	}

	// Deleting a cold message tombstones it there.
	if err := a.DeleteMessage(ctx(t), conv, 3); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, b, func(ev *pb.Event) bool { return ev.GetMessageDeleted().GetSeq() == 3 })
	page, err := b.GetMessages(ctx(t), conv, 4, 1)
	if err != nil || len(page) != 1 || page[0].GetDeletedAt() == nil || page[0].GetContent() != nil {
		t.Fatalf("the deleted cold message: %v %v", page, err)
	}

	// The same reaction again and deleting again tell no one: the next
	// event is the next message.
	if err := b.SetReaction(ctx(t), conv, 1, set.GetSealed()); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteMessage(ctx(t), conv, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendText(ctx(t), conv, "csix", "six"); err != nil {
		t.Fatal(err)
	}
	if ev := nextEvent(t, b, func(*pb.Event) bool { return true }); ev.GetMessage().GetMessage().GetSeq() != 6 {
		t.Fatalf("event after repeating changes to cold messages: %v", ev)
	}
}
