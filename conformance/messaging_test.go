package conformance

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestOpenConversation(t *testing.T) {
	a := newUser(t, "alice")
	b := newUser(t, "bob")

	_, err := a.OpenConversation(ctx(t), uniqueName(t, "nobody"))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	_, err = a.OpenConversation(ctx(t), a.user.GetName())
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	fromA, err := a.OpenConversation(ctx(t), b.user.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if len(fromA.GetConversationId()) != 16 {
		t.Fatalf("conversation id of %d bytes", len(fromA.GetConversationId()))
	}
	if !bytes.Equal(fromA.GetPeer().GetUserId(), b.user.GetUserId()) || fromA.GetPeer().GetName() != b.user.GetName() {
		t.Fatalf("wrong peer: %v", fromA.GetPeer())
	}
	if fromA.GetLastSeq() != 0 || fromA.GetLastMessage() != nil {
		t.Fatalf("new conversation is not empty: %v", fromA)
	}
	fromB, err := b.OpenConversation(ctx(t), a.user.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fromB.GetConversationId(), fromA.GetConversationId()) {
		t.Fatalf("both sides should open the same conversation: %x and %x", fromA.GetConversationId(), fromB.GetConversationId())
	}
	again, err := a.OpenConversation(ctx(t), b.user.GetName())
	if err != nil || !bytes.Equal(again.GetConversationId(), fromA.GetConversationId()) {
		t.Fatalf("reopening changed the conversation: %v %v", again, err)
	}
}

func TestSendAssignsDenseSequence(t *testing.T) {
	a, _, conv := pair(t)
	for i := uint64(1); i <= 3; i++ {
		msg, err := a.SendText(ctx(t), conv, fmt.Sprintf("m%d", i), fmt.Sprintf("message %d", i))
		if err != nil {
			t.Fatal(err)
		}
		if msg.GetSeq() != i {
			t.Fatalf("message %d got seq %d", i, msg.GetSeq())
		}
		if !bytes.Equal(msg.GetConversationId(), conv) || !bytes.Equal(msg.GetSenderUserId(), a.user.GetUserId()) ||
			msg.GetClientMessageId() != fmt.Sprintf("m%d", i) || text(t, a, msg) != fmt.Sprintf("message %d", i) {
			t.Fatalf("response does not echo the message: %v", msg)
		}
		if msg.GetSentAt() == nil || time.Since(msg.GetSentAt().AsTime()) > time.Minute {
			t.Fatalf("bad sent_at: %v", msg.GetSentAt())
		}
	}
}

func TestSendIsIdempotent(t *testing.T) {
	a, _, conv := pair(t)
	first, err := a.SendText(ctx(t), conv, "same", "once")
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.SendText(ctx(t), conv, "same", "once again")
	if err != nil {
		t.Fatal(err)
	}
	if second.GetSeq() != first.GetSeq() || !proto.Equal(second.GetSealed(), first.GetSealed()) || text(t, a, second) != "once" {
		t.Fatalf("resend stored a new message: %v then %v", first, second)
	}
	next, err := a.SendText(ctx(t), conv, "other", "two")
	if err != nil {
		t.Fatal(err)
	}
	if next.GetSeq() != first.GetSeq()+1 {
		t.Fatalf("resend consumed a seq: next is %d", next.GetSeq())
	}
}

func TestSendValidation(t *testing.T) {
	a, _, conv := pair(t)
	epoch, err := a.PrepareKeys(ctx(t), conv)
	if err != nil {
		t.Fatal(err)
	}
	send := func(conversationID []byte, clientMessageID string, sealed *pb.Sealed) error {
		_, err := a.SendMessage(ctx(t), conversationID, clientMessageID, sealed)
		return err
	}
	wantCode(t, send(conv, "", junkSealed(epoch)), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	wantCode(t, send(conv, strings.Repeat("i", 65), junkSealed(epoch)), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	wantCode(t, send(conv, "no content", nil), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	// Sealed content: a 12-byte nonce and 1 to 49152 bytes of ciphertext.
	short := junkSealed(epoch)
	short.Nonce = short.Nonce[:11]
	wantCode(t, send(conv, "short nonce", short), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	empty := junkSealed(epoch)
	empty.Ciphertext = nil
	wantCode(t, send(conv, "empty", empty), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	largest := junkSealed(epoch)
	largest.Ciphertext = make([]byte, 49152)
	if err := send(conv, "largest", largest); err != nil {
		t.Fatalf("49152 bytes: %v", err)
	}
	tooLarge := junkSealed(epoch)
	tooLarge.Ciphertext = make([]byte, 49153)
	wantCode(t, send(conv, "too large", tooLarge), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	// The longest text sealed fits.
	if _, err := a.SendText(ctx(t), conv, "longest", strings.Repeat("😀", 4000)); err != nil {
		t.Fatalf("4000 characters: %v", err)
	}
	wantCode(t, send(randomID(t), "x", junkSealed(1)), pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	// Ids are 16 bytes.
	wantCode(t, send(conv[:15], "x", junkSealed(epoch)), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	wantCode(t, send(nil, "x", junkSealed(epoch)), pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	err = a.MarkRead(ctx(t), append(bytes.Clone(conv), 0), 1)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = a.GetMessages(ctx(t), conv[:8], 0, 0)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	outsider := newUser(t, "eve")
	_, err = outsider.SendMessage(ctx(t), conv, "x", junkSealed(epoch))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	_, err = outsider.GetMessages(ctx(t), conv, 0, 0)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
	err = outsider.MarkRead(ctx(t), conv, 1)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED)
}

func TestMessageIsPushedToEveryDevice(t *testing.T) {
	a, b, conv := pair(t)
	a2 := otherDevice(t, a)
	b2 := otherDevice(t, b)

	sent, err := a.SendText(ctx(t), conv, "m1", "hello everyone")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []*device{a, a2, b, b2} {
		got := messageEvent(t, d, conv, 1)
		if text(t, d, got) != "hello everyone" || !proto.Equal(got.GetSealed(), sent.GetSealed()) || !bytes.Equal(got.GetSenderUserId(), a.user.GetUserId()) ||
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
	_, err = b.GetMessages(ctx(t), randomID(t), 0, 0)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
}
