package conformance

import (
	"testing"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestMarkRead(t *testing.T) {
	a, b, conv := pair(t)
	a2 := otherDevice(t, a)
	sendMessages(t, b, conv, 2, "hi")
	for _, d := range []*device{a, a2, b} {
		messageEvent(t, d, conv, 2)
	}

	if err := a.MarkRead(ctx(t), conv, 2); err != nil {
		t.Fatal(err)
	}
	// The reader's other device and the peer both learn about it.
	for _, d := range []*device{a, a2, b} {
		readEvent(t, d, conv, a.user.GetUserId(), 2)
	}

	// Going backwards or staying put produces nothing: a message sent
	// after them is the next thing the peer gets, since a conversation's
	// events arrive in order.
	if err := a.MarkRead(ctx(t), conv, 1); err != nil {
		t.Fatal(err)
	}
	if err := a.MarkRead(ctx(t), conv, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendText(ctx(t), conv, "m3", "seen"); err != nil {
		t.Fatal(err)
	}
	ev := nextEvent(t, b, func(ev *pb.Event) bool { return ev.GetRead() != nil || isMessage(ev) })
	if ev.GetRead() != nil {
		t.Fatalf("read event for a mark that did not move: %v", ev)
	}

	err := a.MarkRead(ctx(t), conv, 4)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	err = a.MarkRead(ctx(t), conv, 0)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
}
