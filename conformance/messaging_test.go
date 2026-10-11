package conformance

import (
	"fmt"
	"strings"
	"testing"
	"time"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestOpenConversation(t *testing.T) {
	a := connect(t, uniqueName(t, "alice"))
	b := connect(t, uniqueName(t, "bob"))

	_, err := a.OpenConversation(ctx(t), uniqueName(t, "nobody"))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	_, err = a.OpenConversation(ctx(t), a.user.GetName())
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	fromA, err := a.OpenConversation(ctx(t), b.user.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if fromA.GetPeer().GetUserId() != b.user.GetUserId() || fromA.GetPeer().GetName() != b.user.GetName() {
		t.Fatalf("wrong peer: %v", fromA.GetPeer())
	}
	if fromA.GetLastSeq() != 0 || fromA.GetLastMessage() != nil {
		t.Fatalf("new conversation is not empty: %v", fromA)
	}
	fromB, err := b.OpenConversation(ctx(t), a.user.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if fromB.GetConversationId() != fromA.GetConversationId() {
		t.Fatalf("both sides should open the same conversation: %d and %d", fromA.GetConversationId(), fromB.GetConversationId())
	}
	again, err := a.OpenConversation(ctx(t), b.user.GetName())
	if err != nil || again.GetConversationId() != fromA.GetConversationId() {
		t.Fatalf("reopening changed the conversation: %v %v", again, err)
	}
}

func TestSendAssignsDenseSequence(t *testing.T) {
	a, _, conv := pair(t)
	for i := uint64(1); i <= 3; i++ {
		msg, err := a.SendMessage(ctx(t), conv, fmt.Sprintf("m%d", i), fmt.Sprintf("message %d", i))
		if err != nil {
			t.Fatal(err)
		}
		if msg.GetSeq() != i {
			t.Fatalf("message %d got seq %d", i, msg.GetSeq())
		}
		if msg.GetConversationId() != conv || msg.GetSenderUserId() != a.user.GetUserId() ||
			msg.GetClientMessageId() != fmt.Sprintf("m%d", i) || msg.GetText() != fmt.Sprintf("message %d", i) {
			t.Fatalf("response does not echo the message: %v", msg)
		}
		if msg.GetSentAt() == nil || time.Since(msg.GetSentAt().AsTime()) > time.Minute {
			t.Fatalf("bad sent_at: %v", msg.GetSentAt())
		}
	}
}

func TestSendIsIdempotent(t *testing.T) {
	a, _, conv := pair(t)
	first, err := a.SendMessage(ctx(t), conv, "same", "once")
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.SendMessage(ctx(t), conv, "same", "once again")
	if err != nil {
		t.Fatal(err)
	}
	if second.GetSeq() != first.GetSeq() || second.GetText() != first.GetText() {
		t.Fatalf("resend stored a new message: %v then %v", first, second)
	}
	next, err := a.SendMessage(ctx(t), conv, "other", "two")
	if err != nil {
		t.Fatal(err)
	}
	if next.GetSeq() != first.GetSeq()+1 {
		t.Fatalf("resend consumed a seq: next is %d", next.GetSeq())
	}
}

func TestSendValidation(t *testing.T) {
	a, _, conv := pair(t)
	_, err := a.SendMessage(ctx(t), conv, "", "no id")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = a.SendMessage(ctx(t), conv, "empty", "")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = a.SendMessage(ctx(t), conv, strings.Repeat("i", 65), "id too long")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	// Text is limited to 4000 characters (code points), not bytes or UTF-16 units.
	if _, err := a.SendMessage(ctx(t), conv, "longest", strings.Repeat("😀", 4000)); err != nil {
		t.Fatalf("4000 characters: %v", err)
	}
	_, err = a.SendMessage(ctx(t), conv, "too long", strings.Repeat("é", 4001))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = a.SendMessage(ctx(t), conv+1, "x", "no such conversation")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)

	outsider := connect(t, uniqueName(t, "eve"))
	_, err = outsider.SendMessage(ctx(t), conv, "x", "not mine")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	_, err = outsider.GetMessages(ctx(t), conv, 0, 0)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	err = outsider.MarkRead(ctx(t), conv, 1)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
}

func TestMessageIsPushedToEveryDevice(t *testing.T) {
	a, b, conv := pair(t)
	a2 := connect(t, a.user.GetName())
	b2 := connect(t, b.user.GetName())

	sent, err := a.SendMessage(ctx(t), conv, "m1", "hello everyone")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, a2, b, b2} {
		got := messageEvent(t, d, conv, 1)
		if got.GetText() != sent.GetText() || got.GetSenderUserId() != a.user.GetUserId() ||
			got.GetClientMessageId() != "m1" || !got.GetSentAt().AsTime().Equal(sent.GetSentAt().AsTime()) {
			t.Fatalf("pushed message differs from the sent one: %v vs %v", got, sent)
		}
	}
}

func TestEventsArriveInSequenceOrder(t *testing.T) {
	a, b, conv := pair(t)
	const n = 20
	sendMessages(t, a, conv, n, "x")
	for i := uint64(1); i <= n; i++ {
		ev := nextEvent(t, b, isMessage)
		if got := ev.GetMessage().GetMessage().GetSeq(); got != i {
			t.Fatalf("expected seq %d next, got %d", i, got)
		}
	}
}

func TestGetMessages(t *testing.T) {
	a, b, conv := pair(t)
	sendMessages(t, a, conv, 5, "text")
	all, err := b.GetMessages(ctx(t), conv, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(all))
	}
	for i, m := range all {
		if want := uint64(5 - i); m.GetSeq() != want {
			t.Fatalf("message %d has seq %d, want %d (newest first)", i, m.GetSeq(), want)
		}
	}
	page, err := b.GetMessages(ctx(t), conv, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].GetSeq() != 3 || page[1].GetSeq() != 2 {
		t.Fatalf("page before 4 limit 2: %v", page)
	}
	_, err = b.GetMessages(ctx(t), conv+1, 0, 0)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
}
