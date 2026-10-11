package conformance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// rawDial opens a WebSocket without the client library, to send what a
// well-behaved client would not.
func rawDial(t *testing.T) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.Dial(ctx(t), gatewayURL(), nil)
	if err != nil {
		t.Fatalf("dial %s: %v", gatewayURL(), err)
	}
	ws.SetReadLimit(1 << 20)
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

// wantClose reads until the server closes the connection and checks the code.
func wantClose(t *testing.T, ws *websocket.Conn, want websocket.StatusCode) {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, _, err := ws.Read(c)
		if err == nil {
			continue
		}
		if got := websocket.CloseStatus(err); got != want {
			t.Fatalf("closed with %v (%v), want %v", got, err, want)
		}
		return
	}
}

func TestOversizedFrameCloses(t *testing.T) {
	ws := rawDial(t)
	// The server may close before it has read the whole frame, which can
	// fail the write; the close frame is what counts, so read meanwhile.
	c := ctx(t)
	go func() { _ = ws.Write(c, websocket.MessageBinary, make([]byte, 64*1024+1)) }()
	wantClose(t, ws, websocket.StatusMessageTooBig)
}

func TestTextFrameCloses(t *testing.T) {
	ws := rawDial(t)
	if err := ws.Write(ctx(t), websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	wantClose(t, ws, websocket.StatusUnsupportedData)
}

func TestMalformedFrameCloses(t *testing.T) {
	ws := rawDial(t)
	if err := ws.Write(ctx(t), websocket.MessageBinary, []byte{0xff, 0xff, 0xff, 0xff}); err != nil {
		t.Fatal(err)
	}
	wantClose(t, ws, websocket.StatusPolicyViolation)
}

func TestConnectDeadline(t *testing.T) {
	ws := rawDial(t)
	start := time.Now()
	wantClose(t, ws, websocket.StatusPolicyViolation)
	if elapsed := time.Since(start); elapsed < 3*time.Second {
		t.Fatalf("closed after %v, before the 5 s deadline", elapsed)
	}
}

func TestRequestFlood(t *testing.T) {
	d := connect(t, uniqueName(t, "flood"))
	const n = 200
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[pb.ErrorCode]int{}
	for range n {
		wg.Go(func() {
			_, err := d.ListConversations(ctx(t))
			mu.Lock()
			codes[wrinkleclient.Code(err)]++
			mu.Unlock()
		})
	}
	wg.Wait()
	exhausted := codes[pb.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED]
	ok := codes[pb.ErrorCode_ERROR_CODE_UNSPECIFIED]
	if exhausted == 0 || exhausted+ok != n {
		t.Fatalf("want successes and RESOURCE_EXHAUSTED only, some of each: %v", codes)
	}

	// The limits recover; the connection stays usable.
	time.Sleep(2500 * time.Millisecond)
	if _, err := d.ListConversations(ctx(t)); err != nil {
		t.Fatalf("after the flood: %v", err)
	}
}
