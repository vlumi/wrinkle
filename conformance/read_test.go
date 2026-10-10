package conformance

import (
	"testing"
	"time"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestMarkRead(t *testing.T) {
	a, b, conv := pair(t)
	a2 := connect(t, a.user.GetName())
	for i := 1; i <= 2; i++ {
		if _, err := b.SendMessage(ctx(t), conv, string(rune('a'+i)), "hi"); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []*device{a, a2, b} {
		messageEvent(d, t, conv, 2)
	}

	if err := a.MarkRead(ctx(t), conv, 2); err != nil {
		t.Fatal(err)
	}
	// The reader's other device and the peer both learn about it.
	for _, d := range []*device{a, a2, b} {
		readEvent(d, t, conv, a.user.GetUserId(), 2)
	}

	// Going backwards or staying put produces nothing.
	if err := a.MarkRead(ctx(t), conv, 1); err != nil {
		t.Fatal(err)
	}
	if err := a.MarkRead(ctx(t), conv, 2); err != nil {
		t.Fatal(err)
	}
	noEvent(t, b, 2*time.Second)

	err := a.MarkRead(ctx(t), conv, 3)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	err = a.MarkRead(ctx(t), conv, 0)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
}
