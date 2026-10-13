package conformance

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// newGroup creates a group of the creator and the members, and waits for
// every member's GroupCreated event.
func newGroup(t *testing.T, creator *device, topic string, members ...*device) []byte {
	t.Helper()
	var names []string
	for _, m := range members {
		names = append(names, m.user.GetName())
	}
	conv, err := creator.CreateGroup(ctx(t), topic, names, uniqueName(t, "group"))
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	for _, d := range append([]*device{creator}, members...) {
		if n := messageEvent(t, d, conv.GetConversationId(), 1).GetNotice(); n.GetGroupCreated() == nil {
			t.Fatalf("first message of the group is not GroupCreated: %v", n)
		}
	}
	return conv.GetConversationId()
}

// waitConversation polls the device's list until the conversation's entry
// satisfies ok.
func waitConversation(t *testing.T, d *device, conversationID []byte, ok func(*pb.Conversation) bool) *pb.Conversation {
	t.Helper()
	deadline := time.Now().Add(eventTimeout)
	for {
		list, err := d.ListConversations(ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		var found *pb.Conversation
		for _, c := range list {
			if bytes.Equal(c.GetConversationId(), conversationID) {
				found = c
			}
		}
		if found != nil && ok(found) {
			return found
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s's list entry of %x is %v after %s", d.user.GetName(), conversationID, found, eventTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// noticeEvent waits for the message at seq and returns its notice.
func noticeEvent(t *testing.T, d *device, conversationID []byte, seq uint64) (*pb.Message, *pb.Notice) {
	t.Helper()
	msg := messageEvent(t, d, conversationID, seq)
	if msg.GetNotice() == nil {
		t.Fatalf("message %d is not a notice: %v", seq, msg)
	}
	return msg, msg.GetNotice()
}

func sameUser(u *pb.User, d *device) bool {
	return u != nil && bytes.Equal(u.GetUserId(), d.user.GetUserId())
}

func userIDs(users []*pb.User) [][]byte {
	var out [][]byte
	for _, u := range users {
		out = append(out, u.GetUserId())
	}
	slices.SortFunc(out, bytes.Compare)
	return out
}

func usersOf(devices ...*device) [][]byte {
	var out [][]byte
	for _, d := range devices {
		out = append(out, d.user.GetUserId())
	}
	slices.SortFunc(out, bytes.Compare)
	return out
}

func TestCreateGroup(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")

	// The creator and repeats, by any case, are left out.
	names := []string{b.user.GetName(), c.user.GetName(), a.user.GetName(), strings.ToUpper(b.user.GetName())}
	clientID := uniqueName(t, "group")
	conv, err := a.CreateGroup(ctx(t), "Hikes", names, clientID)
	if err != nil {
		t.Fatal(err)
	}
	id := conv.GetConversationId()
	if len(id) != 16 || conv.GetKind() != pb.ConversationKind_CONVERSATION_KIND_GROUP || conv.GetTopic() != "Hikes" ||
		conv.GetMemberCount() != 3 || !conv.GetMember() || !conv.GetAdmin() || conv.GetLastSeq() != 1 || conv.GetPeer() != nil {
		t.Fatalf("created group: %v", conv)
	}
	if created := conv.GetLastMessage().GetNotice().GetGroupCreated(); created == nil || created.GetTopic() != "Hikes" {
		t.Fatalf("last message of a new group: %v", conv.GetLastMessage())
	}

	for _, d := range []*device{a, b, c} {
		msg, n := noticeEvent(t, d, id, 1)
		created := n.GetGroupCreated()
		if !bytes.Equal(msg.GetSenderUserId(), a.user.GetUserId()) || created.GetTopic() != "Hikes" ||
			!strings.HasPrefix(msg.GetClientMessageId(), "notice-") || !equalIDs(userIDs(created.GetMembers()), usersOf(b, c)) {
			t.Fatalf("GroupCreated for %s: %v", d.user.GetName(), msg)
		}
		for _, u := range created.GetMembers() {
			if sameUser(u, b) && u.GetName() != b.user.GetName() {
				t.Fatalf("member snapshot %v", u)
			}
		}
		entry := waitConversation(t, d, id, func(c *pb.Conversation) bool { return c.GetLastSeq() == 1 })
		if entry.GetKind() != pb.ConversationKind_CONVERSATION_KIND_GROUP || entry.GetTopic() != "Hikes" || entry.GetMemberCount() != 3 ||
			!entry.GetMember() || entry.GetAdmin() != (d == a) || entry.GetPeer() != nil || entry.GetLastMessage().GetNotice().GetGroupCreated() == nil {
			t.Fatalf("%s's list entry: %v", d.user.GetName(), entry)
		}
	}

	// A retry returns the same group and adds nothing.
	again, err := a.CreateGroup(ctx(t), "Other topic", nil, clientID)
	if err != nil || !bytes.Equal(again.GetConversationId(), id) || again.GetTopic() != "Hikes" || again.GetMemberCount() != 3 {
		t.Fatalf("retry: %v %v", again, err)
	}
	if msgs, err := b.GetMessages(ctx(t), id, 0, 0); err != nil || len(msgs) != 1 {
		t.Fatalf("messages after a retry: %v %v", msgs, err)
	}
	// Another client id is another group, alone is fine.
	solo, err := a.CreateGroup(ctx(t), "Notes", nil, uniqueName(t, "group"))
	if err != nil || bytes.Equal(solo.GetConversationId(), id) || solo.GetMemberCount() != 1 {
		t.Fatalf("second group: %v %v", solo, err)
	}
}

func TestCreateGroupValidation(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	create := func(topic string, names []string, clientID string) error {
		_, err := a.CreateGroup(ctx(t), topic, names, clientID)
		return err
	}
	members := []string{b.user.GetName()}
	wantCode(t, create("Trip", []string{b.user.GetName(), uniqueName(t, "nobody")}, uniqueName(t, "g")), pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	for _, topic := range []string{"", " lead", "trail ", "line\nbreak", strings.Repeat("t", 201)} {
		wantCode(t, create(topic, members, uniqueName(t, "g")), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	}
	if err := create(strings.Repeat("é", 100), members, uniqueName(t, "g")); err != nil {
		t.Fatalf("200-byte topic: %v", err)
	}
	wantCode(t, create("Trip", members, ""), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	wantCode(t, create("Trip", members, strings.Repeat("g", 65)), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	wantCode(t, create("Trip", []string{""}, uniqueName(t, "g")), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	tooMany := make([]string, 256)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("member-%d", i)
	}
	wantCode(t, create("Trip", tooMany, uniqueName(t, "g")), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
}

func TestAddMembers(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")
	id := newGroup(t, a, "Book club", b)
	if _, err := a.SendText(ctx(t), id, "m1", "before carol"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, id, 2)

	// Any member may add.
	if err := b.AddMembers(ctx(t), id, []string{c.user.GetName(), a.user.GetName()}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, b, c} {
		msg, n := noticeEvent(t, d, id, 3)
		if !bytes.Equal(msg.GetSenderUserId(), b.user.GetUserId()) || !equalIDs(userIDs(n.GetMembersAdded().GetUsers()), usersOf(c)) {
			t.Fatalf("MembersAdded for %s: %v", d.user.GetName(), msg)
		}
	}
	entry := waitConversation(t, c, id, func(c *pb.Conversation) bool { return c.GetLastSeq() == 3 })
	if !entry.GetMember() || entry.GetAdmin() || entry.GetMemberCount() != 3 || entry.GetTopic() != "Book club" {
		t.Fatalf("new member's entry: %v", entry)
	}
	waitConversation(t, a, id, func(c *pb.Conversation) bool { return c.GetLastSeq() == 3 && c.GetMemberCount() == 3 })

	// The new member reads from the notice that added them on.
	msgs, err := c.GetMessages(ctx(t), id, 0, 0)
	if err != nil || len(msgs) != 1 || msgs[0].GetSeq() != 3 {
		t.Fatalf("new member's history: %v %v", msgs, err)
	}
	if _, err := c.SendText(ctx(t), id, "m1", "hello all"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, b} {
		if got := messageEvent(t, d, id, 4); text(t, d, got) != "hello all" {
			t.Fatalf("message from the new member: %v", got)
		}
	}

	// Adding members already in does nothing.
	if err := a.AddMembers(ctx(t), id, []string{b.user.GetName(), c.user.GetName()}); err != nil {
		t.Fatal(err)
	}
	if msg, err := a.SendText(ctx(t), id, "m2", "next"); err != nil || msg.GetSeq() != 5 {
		t.Fatalf("adding existing members took a seq: %v %v", msg, err)
	}

	err = a.AddMembers(ctx(t), id, []string{uniqueName(t, "nobody")})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	outsider := newUser(t, "eve")
	err = outsider.AddMembers(ctx(t), id, []string{outsider.user.GetName()})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	err = a.AddMembers(ctx(t), randomID(t), []string{b.user.GetName()})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	err = a.AddMembers(ctx(t), id[:15], []string{b.user.GetName()})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	// Members in the order they joined, those who joined together by id.
	members, err := c.GetMembers(ctx(t), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 3 {
		t.Fatalf("members: %v", members)
	}
	first := usersOf(a, b)
	for i, m := range members[:2] {
		if !bytes.Equal(m.GetUser().GetUserId(), first[i]) || m.GetJoinedSeq() != 1 || m.GetAdmin() != bytes.Equal(first[i], a.user.GetUserId()) {
			t.Fatalf("member %d: %v", i, m)
		}
	}
	if m := members[2]; !sameUser(m.GetUser(), c) || m.GetJoinedSeq() != 3 || m.GetAdmin() || m.GetUser().GetName() != c.user.GetName() {
		t.Fatalf("added member: %v", m)
	}
}

func TestRemoveMember(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")
	id := newGroup(t, a, "Choir", b, c)

	err := b.RemoveMember(ctx(t), id, c.user.GetUserId())
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	err = a.RemoveMember(ctx(t), id, a.user.GetUserId())
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	err = a.RemoveMember(ctx(t), id, randomID(t))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	err = a.RemoveMember(ctx(t), id, c.user.GetUserId()[:8])
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	if err := a.RemoveMember(ctx(t), id, c.user.GetUserId()); err != nil {
		t.Fatal(err)
	}
	// The removed member hears of it too.
	for _, d := range []*device{a, b, c} {
		msg, n := noticeEvent(t, d, id, 2)
		if !bytes.Equal(msg.GetSenderUserId(), a.user.GetUserId()) || !sameUser(n.GetMemberRemoved().GetUser(), c) {
			t.Fatalf("MemberRemoved for %s: %v", d.user.GetName(), msg)
		}
	}
	entry := waitConversation(t, c, id, func(c *pb.Conversation) bool { return c.GetLastSeq() == 2 })
	if entry.GetMember() || entry.GetAdmin() || entry.GetMemberCount() != 2 {
		t.Fatalf("removed member's entry: %v", entry)
	}
	waitConversation(t, a, id, func(c *pb.Conversation) bool { return c.GetMemberCount() == 2 && c.GetMember() && c.GetAdmin() })

	_, err = c.SendText(ctx(t), id, "m1", "still here?")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	err = c.MarkRead(ctx(t), id, 1)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	_, err = c.GetMembers(ctx(t), id)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	err = c.LeaveGroup(ctx(t), id)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	err = a.RemoveMember(ctx(t), id, c.user.GetUserId())
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)

	// The history stays, up to the removal.
	if _, err := a.SendText(ctx(t), id, "m1", "after carol"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, id, 3)
	msgs, err := c.GetMessages(ctx(t), id, 0, 0)
	if err != nil || len(msgs) != 2 || msgs[0].GetSeq() != 2 || msgs[1].GetSeq() != 1 {
		t.Fatalf("former member's history: %v %v", msgs, err)
	}

	// Added again, they read from the new notice on.
	if err := b.AddMembers(ctx(t), id, []string{c.user.GetName()}); err != nil {
		t.Fatal(err)
	}
	noticeEvent(t, c, id, 4)
	waitConversation(t, c, id, func(c *pb.Conversation) bool { return c.GetMember() && c.GetLastSeq() == 4 })
	msgs, err = c.GetMessages(ctx(t), id, 0, 0)
	if err != nil || len(msgs) != 1 || msgs[0].GetSeq() != 4 {
		t.Fatalf("history after rejoining: %v %v", msgs, err)
	}
	if _, err := c.SendText(ctx(t), id, "m2", "back"); err != nil {
		t.Fatal(err)
	}
}

func TestLeaveGroup(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")
	id := newGroup(t, a, "Climbing", b)
	if err := a.AddMembers(ctx(t), id, []string{c.user.GetName()}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, b, c} {
		noticeEvent(t, d, id, 2)
	}

	// The last admin leaving makes the member who joined first admin.
	if err := a.LeaveGroup(ctx(t), id); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, b, c} {
		msg, n := noticeEvent(t, d, id, 3)
		if !bytes.Equal(msg.GetSenderUserId(), a.user.GetUserId()) || !sameUser(n.GetMemberLeft().GetPromoted(), b) {
			t.Fatalf("MemberLeft for %s: %v", d.user.GetName(), msg)
		}
	}
	waitConversation(t, b, id, func(c *pb.Conversation) bool {
		return c.GetLastSeq() == 3 && c.GetAdmin() && c.GetMember() && c.GetMemberCount() == 2
	})
	waitConversation(t, c, id, func(c *pb.Conversation) bool { return c.GetLastSeq() == 3 && !c.GetAdmin() && c.GetMember() })
	waitConversation(t, a, id, func(c *pb.Conversation) bool { return c.GetLastSeq() == 3 && !c.GetAdmin() && !c.GetMember() })

	members, err := c.GetMembers(ctx(t), id)
	if err != nil || len(members) != 2 || !sameUser(members[0].GetUser(), b) || !members[0].GetAdmin() ||
		!sameUser(members[1].GetUser(), c) || members[1].GetAdmin() {
		t.Fatalf("members after the admin left: %v %v", members, err)
	}
	err = a.LeaveGroup(ctx(t), id)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	_, err = a.SendText(ctx(t), id, "m1", "gone")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)

	// A member who is not an admin leaves without a promotion.
	if err := c.LeaveGroup(ctx(t), id); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{b, c} {
		if _, n := noticeEvent(t, d, id, 4); n.GetMemberLeft() == nil || n.GetMemberLeft().GetPromoted() != nil {
			t.Fatalf("MemberLeft without promotion: %v", n)
		}
	}
	waitConversation(t, b, id, func(c *pb.Conversation) bool { return c.GetLastSeq() == 4 && c.GetAdmin() && c.GetMemberCount() == 1 })
}

func TestSetTopic(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")
	id := newGroup(t, a, "Old topic", b, c)

	// Any member may.
	if err := b.SetTopic(ctx(t), id, "New topic"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, b, c} {
		msg, n := noticeEvent(t, d, id, 2)
		if !bytes.Equal(msg.GetSenderUserId(), b.user.GetUserId()) || n.GetTopicChanged().GetTopic() != "New topic" {
			t.Fatalf("TopicChanged for %s: %v", d.user.GetName(), msg)
		}
		waitConversation(t, d, id, func(c *pb.Conversation) bool { return c.GetLastSeq() == 2 && c.GetTopic() == "New topic" })
	}
	// The same topic changes nothing.
	if err := c.SetTopic(ctx(t), id, "New topic"); err != nil {
		t.Fatal(err)
	}
	if msg, err := a.SendText(ctx(t), id, "m1", "next"); err != nil || msg.GetSeq() != 3 {
		t.Fatalf("an unchanged topic took a seq: %v %v", msg, err)
	}

	err := a.SetTopic(ctx(t), id, " padded")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	err = a.SetTopic(ctx(t), id, "")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	outsider := newUser(t, "eve")
	err = outsider.SetTopic(ctx(t), id, "Mine now")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
}

func TestSetAdmin(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")
	id := newGroup(t, a, "Garden", b, c)

	err := b.SetAdmin(ctx(t), id, c.user.GetUserId(), true)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	err = a.SetAdmin(ctx(t), id, a.user.GetUserId(), false)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	err = a.SetAdmin(ctx(t), id, randomID(t), true)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)

	if err := a.SetAdmin(ctx(t), id, b.user.GetUserId(), true); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, b, c} {
		msg, n := noticeEvent(t, d, id, 2)
		if !bytes.Equal(msg.GetSenderUserId(), a.user.GetUserId()) || !sameUser(n.GetAdminChanged().GetUser(), b) || !n.GetAdminChanged().GetAdmin() {
			t.Fatalf("AdminChanged for %s: %v", d.user.GetName(), msg)
		}
	}
	waitConversation(t, b, id, func(c *pb.Conversation) bool { return c.GetLastSeq() == 2 && c.GetAdmin() })
	waitConversation(t, c, id, func(c *pb.Conversation) bool { return c.GetLastSeq() == 2 && !c.GetAdmin() })

	// No change is no notice.
	if err := b.SetAdmin(ctx(t), id, b.user.GetUserId(), true); err != nil {
		t.Fatal(err)
	}
	// With another admin, an admin may step down.
	if err := a.SetAdmin(ctx(t), id, a.user.GetUserId(), false); err != nil {
		t.Fatal(err)
	}
	if _, n := noticeEvent(t, a, id, 3); !sameUser(n.GetAdminChanged().GetUser(), a) || n.GetAdminChanged().GetAdmin() {
		t.Fatalf("AdminChanged: %v", n)
	}
	waitConversation(t, a, id, func(c *pb.Conversation) bool { return c.GetLastSeq() == 3 && !c.GetAdmin() && c.GetMember() })
	err = b.SetAdmin(ctx(t), id, b.user.GetUserId(), false)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	err = a.SetAdmin(ctx(t), id, c.user.GetUserId(), true)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)

	members, err := a.GetMembers(ctx(t), id)
	if err != nil || len(members) != 3 {
		t.Fatalf("members: %v %v", members, err)
	}
	for _, m := range members {
		if m.GetAdmin() != sameUser(m.GetUser(), b) {
			t.Fatalf("only bob is an admin: %v", members)
		}
	}
	// An admin leaving while another remains promotes nobody.
	if err := b.SetAdmin(ctx(t), id, a.user.GetUserId(), true); err != nil {
		t.Fatal(err)
	}
	if err := b.LeaveGroup(ctx(t), id); err != nil {
		t.Fatal(err)
	}
	if _, n := noticeEvent(t, c, id, 5); n.GetMemberLeft() == nil || n.GetMemberLeft().GetPromoted() != nil {
		t.Fatalf("MemberLeft with another admin: %v", n)
	}
}

func TestGroupOrdering(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")
	id := newGroup(t, a, "Relay", b, c)

	// Messages from three senders and notices, at once.
	const perSender = 6
	sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 3*perSender+2)
	for _, d := range []*device{a, b, c} {
		wg.Go(func() {
			for i := range perSender {
				if _, err := d.SendText(sendCtx, id, fmt.Sprintf("m%d", i), fmt.Sprintf("%s %d", d.user.GetName(), i)); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Go(func() {
		for _, topic := range []string{"Relay 2", "Relay 3"} {
			if err := b.SetTopic(sendCtx, id, topic); err != nil {
				errs <- err
			}
		}
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	// Every member gets the same sequence.
	const last = 1 + 3*perSender + 2
	var orders [][]string
	for _, d := range []*device{a, b, c} {
		var order []string
		for seq := uint64(2); seq <= last; seq++ {
			msg := nextEvent(t, d, isMessage).GetMessage().GetMessage()
			if msg.GetSeq() != seq {
				t.Fatalf("%s got seq %d, want %d", d.user.GetName(), msg.GetSeq(), seq)
			}
			order = append(order, fmt.Sprintf("%x/%s", msg.GetSenderUserId(), msg.GetClientMessageId()))
		}
		orders = append(orders, order)
	}
	for _, o := range orders[1:] {
		if !slices.Equal(o, orders[0]) {
			t.Fatalf("members saw different orders:\n%v\n%v", orders[0], o)
		}
	}
	msgs, err := c.GetMessages(ctx(t), id, 0, 100)
	if err != nil || len(msgs) != last {
		t.Fatalf("history: %d messages, %v", len(msgs), err)
	}
	for i, m := range msgs {
		if want := fmt.Sprintf("%x/%s", m.GetSenderUserId(), m.GetClientMessageId()); m.GetSeq() > 1 && orders[0][m.GetSeq()-2] != want {
			t.Fatalf("history at %d is %s, events had %s", i, want, orders[0][m.GetSeq()-2])
		}
	}
	waitConversation(t, a, id, func(c *pb.Conversation) bool { return c.GetLastSeq() == last && c.GetTopic() == "Relay 3" })
}

func TestGroupReadPositions(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")
	id := newGroup(t, a, "Readers", b, c)

	if _, err := a.SendText(ctx(t), id, "m1", "from alice"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, b, c} {
		messageEvent(t, d, id, 2)
	}

	// Bob read Alice's message: Alice and Bob hear of it, not Carol, who
	// sent nothing it covers.
	if err := b.MarkRead(ctx(t), id, 2); err != nil {
		t.Fatal(err)
	}
	readEvent(t, a, id, b.user.GetUserId(), 2)
	readEvent(t, b, id, b.user.GetUserId(), 2)
	if _, err := a.SendText(ctx(t), id, "m2", "again"); err != nil {
		t.Fatal(err)
	}
	// Events of a conversation arrive in order, so Carol's next one would
	// have been Bob's read.
	if ev := nextEvent(t, c, func(*pb.Event) bool { return true }); ev.GetMessage().GetMessage().GetSeq() != 3 {
		t.Fatalf("carol's next event is %v, want message 3", ev)
	}
	for _, d := range []*device{a, b} {
		messageEvent(t, d, id, 3)
	}
	if err := c.MarkRead(ctx(t), id, 3); err != nil {
		t.Fatal(err)
	}
	readEvent(t, a, id, c.user.GetUserId(), 3)
	readEvent(t, c, id, c.user.GetUserId(), 3)

	members, err := a.GetMembers(ctx(t), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		// Sending moves the sender's own read position: Alice sent 3.
		var ok bool
		switch {
		case sameUser(m.GetUser(), a):
			ok = m.GetReadSeq() >= 3
		case sameUser(m.GetUser(), b):
			ok = m.GetReadSeq() == 2
		case sameUser(m.GetUser(), c):
			ok = m.GetReadSeq() == 3
		}
		if !ok {
			t.Fatalf("read_seq of %s: %d", m.GetUser().GetName(), m.GetReadSeq())
		}
	}
	waitConversation(t, b, id, func(c *pb.Conversation) bool { return c.GetReadSeq() == 2 && c.GetPeerReadSeq() == 0 })
	waitConversation(t, a, id, func(c *pb.Conversation) bool { return c.GetPeerReadSeq() == 0 && c.GetLastSeq() == 3 })

	// Bob hears nothing of Carol's read; his next event is the next message.
	if _, err := c.SendText(ctx(t), id, "m1", "from carol"); err != nil {
		t.Fatal(err)
	}
	if ev := nextEvent(t, b, func(*pb.Event) bool { return true }); ev.GetMessage().GetMessage().GetSeq() != 4 {
		t.Fatalf("bob's next event is %v, want message 4", ev)
	}
	// A read covering two senders' messages reaches both.
	if err := b.MarkRead(ctx(t), id, 4); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, b, c} {
		readEvent(t, d, id, b.user.GetUserId(), 4)
	}
}

func TestGroupRequestsOnDirectConversation(t *testing.T) {
	a, b, conv := pair(t)
	opened, err := b.OpenConversation(ctx(t), a.user.GetName())
	if err != nil || opened.GetKind() != pb.ConversationKind_CONVERSATION_KIND_DIRECT {
		t.Fatalf("open: %v %v", opened, err)
	}
	if c := findConversation(t, a, conv); c.GetKind() != pb.ConversationKind_CONVERSATION_KIND_DIRECT || !c.GetMember() {
		t.Fatalf("direct list entry: %v", c)
	}
	fp := pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
	wantCode(t, a.AddMembers(ctx(t), conv, []string{b.user.GetName()}), fp)
	wantCode(t, a.RemoveMember(ctx(t), conv, b.user.GetUserId()), fp)
	wantCode(t, a.LeaveGroup(ctx(t), conv), fp)
	wantCode(t, a.SetTopic(ctx(t), conv, "Topic"), fp)
	wantCode(t, a.SetAdmin(ctx(t), conv, b.user.GetUserId(), true), fp)
	_, err = a.GetMembers(ctx(t), conv)
	wantCode(t, err, fp)
	_, err = a.GetMembers(ctx(t), randomID(t))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	wantCode(t, a.LeaveGroup(ctx(t), randomID(t)), pb.ErrorCode_ERROR_CODE_NOT_FOUND)
}

func TestDeleteAccountLeavesGroups(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")
	c := newUser(t, "carol")
	id := newGroup(t, a, "Farewell", b, c)
	if err := a.DeleteAccount(ctx(t), password); err != nil {
		t.Fatal(err)
	}
	// Bob and Carol joined together: the smaller id becomes admin.
	first := b
	if bytes.Compare(c.user.GetUserId(), b.user.GetUserId()) < 0 {
		first = c
	}
	for _, d := range []*device{b, c} {
		msg, n := noticeEvent(t, d, id, 2)
		if !bytes.Equal(msg.GetSenderUserId(), a.user.GetUserId()) || !sameUser(n.GetMemberLeft().GetPromoted(), first) {
			t.Fatalf("MemberLeft for %s: %v", d.user.GetName(), msg)
		}
	}
	members, err := b.GetMembers(ctx(t), id)
	if err != nil || len(members) != 2 {
		t.Fatalf("members after deletion: %v %v", members, err)
	}
	waitConversation(t, first, id, func(c *pb.Conversation) bool { return c.GetAdmin() && c.GetMemberCount() == 2 })
}
