package conformance

import (
	"bytes"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// reactionEvent waits for a change of the user's reactions to the message
// at seq.
func reactionEvent(t *testing.T, d *device, conversationID []byte, seq uint64, userID []byte) *pb.ReactionEvent {
	t.Helper()
	ev := nextEvent(t, d, func(ev *pb.Event) bool {
		r := ev.GetReaction()
		return r != nil && bytes.Equal(r.GetConversationId(), conversationID) && r.GetSeq() == seq &&
			bytes.Equal(r.GetUserId(), userID)
	})
	r := ev.GetReaction()
	if r.GetUpdatedAt() == nil {
		t.Fatalf("reaction without updated_at: %v", ev)
	}
	return r
}

// reactionEvents waits for the same change on every device.
func reactionEvents(t *testing.T, devices []*device, conversationID []byte, seq uint64, userID []byte) *pb.ReactionEvent {
	t.Helper()
	var first *pb.ReactionEvent
	for _, d := range devices {
		r := reactionEvent(t, d, conversationID, seq, userID)
		if first != nil && !proto.Equal(r, first) {
			t.Fatalf("reaction events differ: %v and %v", r, first)
		}
		first = r
	}
	return first
}

// emoji opens sealed reactions on the device.
func emoji(t *testing.T, d *device, conversationID []byte, seq uint64, userID []byte, sealed *pb.Sealed) []string {
	t.Helper()
	e, err := d.OpenReactions(ctx(t), conversationID, seq, userID, sealed)
	if err != nil {
		t.Fatalf("open reactions to %d: %v", seq, err)
	}
	return e
}

// reactionsTo returns the reactions GetMessages gives the message at seq.
func reactionsTo(t *testing.T, d *device, conversationID []byte, seq uint64) []*pb.Reaction {
	t.Helper()
	msgs, err := d.GetMessages(ctx(t), conversationID, seq+1, 1)
	if err != nil || len(msgs) != 1 || msgs[0].GetSeq() != seq {
		t.Fatalf("message %d: %v %v", seq, msgs, err)
	}
	return msgs[0].GetReactions()
}

// sameReaction checks a reaction from GetMessages against its event.
func sameReaction(t *testing.T, r *pb.Reaction, ev *pb.ReactionEvent) {
	t.Helper()
	if !bytes.Equal(r.GetUserId(), ev.GetUserId()) || !proto.Equal(r.GetSealed(), ev.GetSealed()) ||
		!proto.Equal(r.GetUpdatedAt(), ev.GetUpdatedAt()) {
		t.Fatalf("reaction %v is not as its event %v", r, ev)
	}
}

func TestReactions(t *testing.T) {
	a, b, conv := pair(t)
	a2 := otherDevice(t, a)
	if _, err := a.SendText(ctx(t), conv, "m1", "react to this"); err != nil {
		t.Fatal(err)
	}
	all := []*device{a, a2, b}
	for _, d := range all {
		messageEvent(t, d, conv, 1)
	}
	bob := b.user.GetUserId()

	// A device offline meanwhile gets the reactions by sync.
	off := otherDevice(t, a)
	offCursor := off.cursor
	off.Close()

	if err := b.SetReactions(ctx(t), conv, 1, []string{"👍"}); err != nil {
		t.Fatal(err)
	}
	set := reactionEvents(t, all, conv, 1, bob)
	if set.GetSealed() == nil {
		t.Fatalf("reaction without sealed: %v", set)
	}
	if got := emoji(t, a, conv, 1, bob, set.GetSealed()); !slices.Equal(got, []string{"👍"}) {
		t.Fatalf("alice read %q", got)
	}
	rs := reactionsTo(t, a, conv, 1)
	if len(rs) != 1 {
		t.Fatalf("reactions: %v", rs)
	}
	sameReaction(t, rs[0], set)

	// Replacing the set.
	if err := b.SetReactions(ctx(t), conv, 1, []string{"❤️", "😂"}); err != nil {
		t.Fatal(err)
	}
	replaced := reactionEvents(t, all, conv, 1, bob)
	if got := emoji(t, a2, conv, 1, bob, replaced.GetSealed()); !slices.Equal(got, []string{"❤️", "😂"}) {
		t.Fatalf("alice's other device read %q", got)
	}
	if err := a.SetReactions(ctx(t), conv, 1, []string{"😂"}); err != nil {
		t.Fatal(err)
	}
	own := reactionEvents(t, all, conv, 1, a.user.GetUserId())
	if got := emoji(t, b, conv, 1, a.user.GetUserId(), own.GetSealed()); !slices.Equal(got, []string{"😂"}) {
		t.Fatalf("bob read %q", got)
	}
	// Each user's latest, oldest first.
	rs = reactionsTo(t, b, conv, 1)
	if len(rs) != 2 {
		t.Fatalf("reactions: %v", rs)
	}
	sameReaction(t, rs[0], replaced)
	sameReaction(t, rs[1], own)

	// The same bytes again, and removing none, tell no one: the next
	// event is the next message.
	if err := b.SetReaction(ctx(t), conv, 1, replaced.GetSealed()); err != nil {
		t.Fatal(err)
	}
	if err := a2.SetReactions(ctx(t), conv, 1, []string{"😂"}); err != nil {
		t.Fatal(err)
	}
	ownAgain := reactionEvents(t, all, conv, 1, a.user.GetUserId())
	if proto.Equal(ownAgain.GetSealed(), own.GetSealed()) {
		t.Fatal("sealed twice with the same nonce")
	}
	if _, err := a.SendText(ctx(t), conv, "m2", "next"); err != nil {
		t.Fatal(err)
	}
	for _, d := range all {
		if ev := nextEvent(t, d, func(*pb.Event) bool { return true }); ev.GetMessage().GetMessage().GetSeq() != 2 {
			t.Fatalf("event after an identical reaction: %v", ev)
		}
	}

	// Removing.
	if err := b.SetReactions(ctx(t), conv, 1, nil); err != nil {
		t.Fatal(err)
	}
	removed := reactionEvents(t, all, conv, 1, bob)
	if removed.GetSealed() != nil {
		t.Fatalf("removal with sealed: %v", removed)
	}
	rs = reactionsTo(t, a, conv, 1)
	if len(rs) != 1 {
		t.Fatalf("reactions after removal: %v", rs)
	}
	sameReaction(t, rs[0], ownAgain)
	if err := b.SetReaction(ctx(t), conv, 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendText(ctx(t), conv, "m3", "next"); err != nil {
		t.Fatal(err)
	}
	for _, d := range all {
		if ev := nextEvent(t, d, func(*pb.Event) bool { return true }); ev.GetMessage().GetMessage().GetSeq() != 3 {
			t.Fatalf("event after removing nothing: %v", ev)
		}
	}

	// Reactions take no seq and leave the list alone.
	for _, d := range []*device{a, b} {
		entry := findConversation(t, d, conv)
		if entry.GetLastSeq() != 3 || len(entry.GetLastMessage().GetReactions()) != 0 {
			t.Fatalf("%s's entry: %v", d.user.GetName(), entry)
		}
	}
	msgs, err := a.GetMessages(ctx(t), conv, 0, 0)
	if err != nil || len(msgs) != 3 || len(msgs[0].GetReactions()) != 0 || len(msgs[2].GetReactions()) != 1 {
		t.Fatalf("history: %v %v", msgs, err)
	}

	var synced []*pb.ReactionEvent
	for _, ev := range syncAll(t, reconnect(t, off), offCursor) {
		if r := ev.GetReaction(); r != nil {
			synced = append(synced, r)
		}
	}
	want := []*pb.ReactionEvent{set, replaced, own, ownAgain, removed}
	if len(synced) != len(want) {
		t.Fatalf("synced %d reactions, want %d: %v", len(synced), len(want), synced)
	}
	for i := range want {
		if !proto.Equal(synced[i], want[i]) {
			t.Fatalf("synced %v, want %v", synced[i], want[i])
		}
	}
}

func TestReactionValidation(t *testing.T) {
	a, b, conv := pair(t)
	if _, err := a.SendText(ctx(t), conv, "m1", "hi"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, conv, 1)
	epoch := conversationKeys(t, a, conv, 0).GetCurrentEpoch()
	sealed := func(nonce, ciphertext int) *pb.Sealed {
		return &pb.Sealed{Epoch: epoch, Nonce: make([]byte, nonce), Ciphertext: make([]byte, ciphertext)}
	}
	invalid := pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
	wantCode(t, a.SetReaction(ctx(t), conv, 0, sealed(12, 32)), invalid)
	wantCode(t, a.SetReaction(ctx(t), conv, 0, nil), invalid)
	wantCode(t, a.SetReaction(ctx(t), conv, 1, sealed(11, 32)), invalid)
	wantCode(t, a.SetReaction(ctx(t), conv, 1, sealed(12, 0)), invalid)
	wantCode(t, a.SetReaction(ctx(t), conv, 1, sealed(12, 1025)), invalid)
	wantCode(t, a.SetReaction(ctx(t), conv[:8], 1, sealed(12, 32)), invalid)
	wantCode(t, a.SetReaction(ctx(t), randomID(t), 1, sealed(12, 32)), pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	wantCode(t, a.SetReaction(ctx(t), conv, 2, sealed(12, 32)), pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	c := newUser(t, "carol")
	wantCode(t, c.SetReaction(ctx(t), conv, 1, sealed(12, 32)), pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	wantCode(t, c.SetReaction(ctx(t), conv, 1, nil), pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	wantCode(t, dial(t).SetReaction(ctx(t), conv, 1, nil), pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)

	// Sealed with an epoch other than the current one.
	fp := pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
	wantCode(t, a.SetReaction(ctx(t), conv, 1, junkSealed(0)), fp)
	wantCode(t, a.SetReaction(ctx(t), conv, 1, junkSealed(epoch+1)), fp)

	// The largest ciphertext.
	if err := a.SetReaction(ctx(t), conv, 1, sealed(12, 1024)); err != nil {
		t.Fatal(err)
	}
	reactionEvent(t, b, conv, 1, a.user.GetUserId())

	// Not to notices.
	group := newGroup(t, a, "Notices", b)
	wantCode(t, a.SetReaction(ctx(t), group, 1, junkSealed(1)), invalid)
}

func TestReactionsToDeletedMessage(t *testing.T) {
	a, b, conv := pair(t)
	if _, err := a.SendText(ctx(t), conv, "m1", "soon gone"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, conv, 1)
	for _, d := range []*device{a, b} {
		if err := d.SetReactions(ctx(t), conv, 1, []string{"👍"}); err != nil {
			t.Fatal(err)
		}
		reactionEvent(t, b, conv, 1, d.user.GetUserId())
	}
	if len(reactionsTo(t, b, conv, 1)) != 2 {
		t.Fatal("reactions missing before the deletion")
	}
	if err := a.DeleteMessage(ctx(t), conv, 1); err != nil {
		t.Fatal(err)
	}
	deletedEvent(t, b, conv, 1)
	if rs := reactionsTo(t, b, conv, 1); len(rs) != 0 {
		t.Fatalf("a deleted message's reactions: %v", rs)
	}
	fp := pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
	wantCode(t, b.SetReactions(ctx(t), conv, 1, []string{"😢"}), fp)
	wantCode(t, b.SetReaction(ctx(t), conv, 1, nil), fp)
}

func TestReactionsInGroup(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")
	d := newUser(t, "dave")
	id := newGroup(t, a, "Reactions", b, c)
	if _, err := c.SendText(ctx(t), id, "m1", "from carol"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*device{a, b, c} {
		messageEvent(t, m, id, 2)
	}
	if err := a.SetReactions(ctx(t), id, 2, []string{"🙏"}); err != nil {
		t.Fatal(err)
	}
	r := reactionEvents(t, []*device{a, b, c}, id, 2, a.user.GetUserId())
	if got := emoji(t, c, id, 2, a.user.GetUserId(), r.GetSealed()); !slices.Equal(got, []string{"🙏"}) {
		t.Fatalf("carol read %q", got)
	}

	// A former member may not react, nor hears of reactions.
	if err := c.LeaveGroup(ctx(t), id); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*device{a, b, c} {
		noticeEvent(t, m, id, 3)
	}
	cCursor := c.cursor
	wantCode(t, c.SetReaction(ctx(t), id, 2, nil), pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	wantCode(t, c.SetReaction(ctx(t), id, 2, junkSealed(1)), pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)

	// The membership changed: keys must rotate first, as for sending.
	if !conversationKeys(t, a, id, 0).GetRotate() {
		t.Fatal("no rotation after leaving")
	}
	wantCode(t, a.SetReaction(ctx(t), id, 2, junkSealed(1)), pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	if err := b.SetReactions(ctx(t), id, 2, []string{"😮"}); err != nil {
		t.Fatal(err)
	}
	r = reactionEvents(t, []*device{a, b}, id, 2, b.user.GetUserId())
	if r.GetSealed().GetEpoch() != 2 {
		t.Fatalf("reacted with epoch %d, want 2", r.GetSealed().GetEpoch())
	}
	if got := emoji(t, a, id, 2, b.user.GetUserId(), r.GetSealed()); !slices.Equal(got, []string{"😮"}) {
		t.Fatalf("alice read %q", got)
	}
	for _, ev := range syncAll(t, c, cCursor) {
		if ev.GetReaction() != nil {
			t.Fatalf("a former member got %v", ev)
		}
	}
	// The former member still reads the reactions to what they saw.
	if rs := reactionsTo(t, c, id, 2); len(rs) != 2 {
		t.Fatalf("former member's reactions: %v", rs)
	}

	// A member added later cannot react to what came before.
	if err := a.AddMembers(ctx(t), id, []string{d.user.GetName()}); err != nil {
		t.Fatal(err)
	}
	noticeEvent(t, d, id, 4)
	wantCode(t, d.SetReaction(ctx(t), id, 2, junkSealed(2)), pb.ErrorCode_ERROR_CODE_NOT_FOUND)
}
