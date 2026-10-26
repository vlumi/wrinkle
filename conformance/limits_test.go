package conformance

import (
	"context"
	"fmt"
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
	d := newUser(t, "flood")
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
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		_, err := d.ListConversations(ctx(t))
		if err == nil {
			return
		}
		if wrinkleclient.Code(err) != pb.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED {
			t.Fatalf("after the flood: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("still limited after the flood: %v", err)
		}
	}
}

func TestSignInsPerConnection(t *testing.T) {
	c := dial(t)
	for range 5 {
		_, err := c.SignIn(ctx(t), fmt.Sprintf("nobody-%x", randomID(t)), "wrong password", "conformance")
		wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	}
	_, err := c.SignIn(ctx(t), fmt.Sprintf("nobody-%x", randomID(t)), "wrong password", "conformance")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED)
}

func TestFailedSignInsKeepTheDeadline(t *testing.T) {
	// Parallel: it waits out the deadline.
	t.Parallel()
	c := dial(t)
	start := time.Now()
	for range 4 {
		_, err := c.SignIn(ctx(t), fmt.Sprintf("nobody-%x", randomID(t)), "wrong password", "conformance")
		wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
		time.Sleep(time.Second)
	}
	// Five seconds after opening, not after the last attempt, the
	// connection is closed.
	time.Sleep(time.Until(start.Add(6 * time.Second)))
	_, err := c.SignIn(ctx(t), fmt.Sprintf("nobody-%x", randomID(t)), "wrong password", "conformance")
	if err == nil || wrinkleclient.Code(err) != pb.ErrorCode_ERROR_CODE_UNSPECIFIED {
		t.Fatalf("a sign-in 6 s after opening: %v, want the connection closed", err)
	}
}
