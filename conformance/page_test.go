package conformance

import (
	"bytes"
	"fmt"
	"math"
	"testing"

	"google.golang.org/protobuf/proto"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// pageBytes is how much a page of events, messages or conversations holds
// before it ends early, by the contract.
const pageBytes = 512 << 10

func wantReason(t *testing.T, err error, code pb.ErrorCode, reason pb.ErrorReason) {
	t.Helper()
	wantCode(t, err, code)
	if got := wrinkleclient.Reason(err); got != reason {
		t.Fatalf("expected %s, got %v (%s)", reason, got, err)
	}
}

func TestPagesEndBeforeTheirBytes(t *testing.T) {
	a, b, conv := pair(t)
	epoch, err := a.PrepareKeys(ctx(t), conv)
	if err != nil {
		t.Fatal(err)
	}
	// Fifteen of the largest messages are more than a page.
	big := &pb.Sealed{Epoch: epoch, Nonce: make([]byte, 12), Ciphertext: make([]byte, 48<<10)}
	var last uint64
	for i := range 15 {
		msg, err := a.SendMessage(ctx(t), conv, fmt.Sprintf("big%d", i), big)
		if err != nil {
			t.Fatal(err)
		}
		last = msg.GetSeq()
	}
	messageEvent(t, b, conv, last)

	page, err := b.GetMessagesPage(ctx(t), conv, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	n := len(page.GetMessages())
	if n == 0 || n >= 15 || !page.GetHasMore() || proto.Size(page) > 2*pageBytes {
		t.Fatalf("a page of large messages: %d messages, has_more %v, %d bytes", n, page.GetHasMore(), proto.Size(page))
	}
	// Paging on reads them all, newest first, and the last page says so.
	seen := n
	before := page.GetMessages()[n-1].GetSeq()
	for page.GetHasMore() {
		if page, err = b.GetMessagesPage(ctx(t), conv, before, 50); err != nil {
			t.Fatal(err)
		}
		if len(page.GetMessages()) == 0 {
			t.Fatal("an empty page with has_more")
		}
		seen += len(page.GetMessages())
		before = page.GetMessages()[len(page.GetMessages())-1].GetSeq()
	}
	if seen != int(last) || before != 1 {
		t.Fatalf("paged %d messages down to %d, want %d down to 1", seen, before, last)
	}

	resp, err := b.Sync(ctx(t), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetEvents()) >= int(last) || !resp.GetHasMore() || proto.Size(resp) > 2*pageBytes {
		t.Fatalf("a sync of large events: %d events, has_more %v, %d bytes", len(resp.GetEvents()), resp.GetHasMore(), proto.Size(resp))
	}

	// Limits are unsigned: the largest is the server's maximum.
	if _, err := b.Sync(ctx(t), 0, math.MaxUint32); err != nil {
		t.Fatalf("sync with the largest limit: %v", err)
	}
	if _, err := b.GetMessagesPage(ctx(t), conv, 0, math.MaxUint32); err != nil {
		t.Fatalf("history with the largest limit: %v", err)
	}
}

func TestListConversationsPages(t *testing.T) {
	a := newUser(t, "lister")
	var want [][]byte
	for range 5 {
		peer := newUser(t, "listed")
		conv, err := a.OpenConversation(ctx(t), peer.user.GetName())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.SendText(ctx(t), conv.GetConversationId(), "hello", "hello"); err != nil {
			t.Fatal(err)
		}
		// Newest first.
		want = append([][]byte{conv.GetConversationId()}, want...)
	}
	waitConversation(t, a, want[0], func(c *pb.Conversation) bool { return c.GetLastSeq() == 1 })

	var got [][]byte
	var token []byte
	for pages := 1; ; pages++ {
		resp, err := a.Call(ctx(t), &pb.Request{Body: &pb.Request_ListConversations{
			ListConversations: &pb.ListConversationsRequest{PageToken: token, Limit: 2}}})
		if err != nil {
			t.Fatal(err)
		}
		page := resp.GetListConversations()
		if len(page.GetConversations()) > 2 {
			t.Fatalf("page %d holds %d", pages, len(page.GetConversations()))
		}
		for _, c := range page.GetConversations() {
			got = append(got, c.GetConversationId())
		}
		if token = page.GetNextPageToken(); len(token) == 0 {
			break
		}
		if pages > 5 {
			t.Fatal("no last page")
		}
	}
	if len(got) != len(want) {
		t.Fatalf("listed %d conversations, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("conversation %d is %x, want %x", i, got[i], want[i])
		}
	}
	// A token the server did not give is refused.
	_, err := a.Call(ctx(t), &pb.Request{Body: &pb.Request_ListConversations{
		ListConversations: &pb.ListConversationsRequest{PageToken: []byte("nonsense")}}})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
}

func TestConversationKeysPage(t *testing.T) {
	a, b, conv := pair(t)
	for epoch := uint32(1); epoch <= 3; epoch++ {
		mustPublish(t, a, conv, epoch, randomBytes(t, 32), a, b)
	}
	var epochs []uint32
	var after uint32
	for {
		resp, err := b.Call(ctx(t), &pb.Request{Body: &pb.Request_GetConversationKeys{
			GetConversationKeys: &pb.GetConversationKeysRequest{ConversationId: conv, AfterEpoch: after, Limit: 1}}})
		if err != nil {
			t.Fatal(err)
		}
		page := resp.GetGetConversationKeys()
		if len(page.GetKeys()) != 1 {
			t.Fatalf("a page of one key holds %d", len(page.GetKeys()))
		}
		after = page.GetKeys()[0].GetEpoch()
		epochs = append(epochs, after)
		if !page.GetHasMore() {
			break
		}
	}
	if fmt.Sprint(epochs) != "[1 2 3]" {
		t.Fatalf("paged epochs %v", epochs)
	}
}

func TestDevicesPerAccount(t *testing.T) {
	d := newUser(t, "devices")
	var last *pb.DeviceCredentials
	for range 19 {
		last = signIn(t, d.user.GetName(), password)
	}
	_, err := dial(t).SignIn(ctx(t), d.user.GetName(), password, "one too many")
	wantReason(t, err, pb.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED, pb.ErrorReason_ERROR_REASON_TOO_MANY_DEVICES)
	// Signing one out makes room.
	if err := d.SignOutDevice(ctx(t), last.GetDeviceId()); err != nil {
		t.Fatal(err)
	}
	signIn(t, d.user.GetName(), password)
}
