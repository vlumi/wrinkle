package gateway

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/presence"
	backendv1 "github.com/vlumi/wrinkle/proto/go/wrinkle/backend/v1"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// testTimeout bounds every wait in these tests; nothing waits for it to
// pass.
const testTimeout = 10 * time.Second

// fakeBackend answers Connect with a fixed identity and anything else with
// an empty response. With hold set, the other calls, and every ack, report
// themselves on calls or acks and wait for hold to close, or for a release
// on acks.
type fakeBackend struct {
	hold       chan struct{}
	calls      chan *pb.Request
	acks       chan uint64
	ackRelease chan struct{}
	// completed receives the context's error as each held call returns.
	completed chan error
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		calls: make(chan *pb.Request, 64), acks: make(chan uint64, 64),
		ackRelease: make(chan struct{}), completed: make(chan error, 64),
	}
}

func (b *fakeBackend) Call(ctx context.Context, _ ids.UUID, _ uint64, req *pb.Request) (*pb.Response, error) {
	resp := &pb.Response{RequestId: req.GetId()}
	if req.GetConnect() != nil {
		resp.Body = &pb.Response_Connect{Connect: &pb.ConnectResponse{User: &pb.User{UserId: user1.Bytes()}, DeviceId: 7}}
		return resp, nil
	}
	if b.hold != nil {
		b.calls <- req
		select {
		case <-b.hold:
		case <-ctx.Done():
		}
		b.completed <- ctx.Err()
	}
	return resp, nil
}

func (b *fakeBackend) Ack(ctx context.Context, _ ids.UUID, _ uint64, cursor uint64) error {
	b.acks <- cursor
	select {
	case <-b.ackRelease:
	case <-ctx.Done():
	}
	return nil
}

type fakePresence struct{}

func (fakePresence) Announce(context.Context) error                             { return nil }
func (fakePresence) Withdraw(context.Context) error                             { return nil }
func (fakePresence) Instance() string                                           { return "test" }
func (fakePresence) DeviceConnected(context.Context, ids.UUID, uint64) error    { return nil }
func (fakePresence) DeviceDisconnected(context.Context, ids.UUID, uint64) error { return nil }

// newTestServer serves the gateway on a local listener, with subscriptions
// confirmed as soon as they are requested, as a Valkey answering at once
// would.
func newTestServer(t *testing.T, backend *fakeBackend) (*Server, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := newRegistry(nil, log)
	r.install(&presence.Subscription{})
	s := &Server{
		ctx: ctx, cancel: cancel, requestCtx: requestCtx, cancelRequests: cancelRequests,
		backend: backend, presence: fakePresence{}, registry: r, log: log, conns: map[*conn]struct{}{},
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.wake:
			}
			for {
				_, add, remove := r.next()
				if len(add) == 0 && len(remove) == 0 {
					break
				}
				for _, userID := range append(add, remove...) {
					r.confirm(userID)
				}
			}
		}
	}()
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		cancel()
		cancelRequests()
		srv.Close()
	})
	return s, "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/ws"
}

func dialTest(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	ws.SetReadLimit(1 << 20)
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

func writeFrame(t *testing.T, ws *websocket.Conn, frame *pb.Frame) {
	t.Helper()
	data, err := proto.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageBinary, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readFrame(t *testing.T, ws *websocket.Conn) *pb.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	frame := &pb.Frame{}
	if err := proto.Unmarshal(data, frame); err != nil {
		t.Fatalf("malformed frame: %v", err)
	}
	return frame
}

// readClose reads until the server closes the connection and returns the
// status code.
func readClose(t *testing.T, ws *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func request(id uint64, body any) *pb.Frame {
	req := &pb.Request{Id: id}
	switch b := body.(type) {
	case *pb.ConnectRequest:
		req.Body = &pb.Request_Connect{Connect: b}
	case *pb.SignInRequest:
		req.Body = &pb.Request_SignIn{SignIn: b}
	case *pb.ListConversationsRequest:
		req.Body = &pb.Request_ListConversations{ListConversations: b}
	}
	return &pb.Frame{Kind: &pb.Frame_Request{Request: req}}
}

func ack(cursor uint64) *pb.Frame {
	return &pb.Frame{Kind: &pb.Frame_Ack{Ack: &pb.Ack{Cursor: cursor}}}
}

// connect completes Connect on the connection.
func connect(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	writeFrame(t, ws, request(1, &pb.ConnectRequest{DeviceToken: "t"}))
	resp := readFrame(t, ws).GetResponse()
	if resp.GetConnect() == nil {
		t.Fatalf("connect: %v", resp)
	}
}

func wantError(t *testing.T, frame *pb.Frame, code pb.ErrorCode) {
	t.Helper()
	if got := frame.GetResponse().GetError().GetCode(); got != code {
		t.Fatalf("got %v, want %v", frame, code)
	}
}

func TestConnRefusesRequestsBeforeConnect(t *testing.T) {
	_, url := newTestServer(t, newFakeBackend())
	ws := dialTest(t, url)
	writeFrame(t, ws, request(1, &pb.ListConversationsRequest{}))
	wantError(t, readFrame(t, ws), pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)

	// Acks from an unidentified connection are dropped; a ping still gets
	// its pong.
	writeFrame(t, ws, ack(5))
	writeFrame(t, ws, &pb.Frame{Kind: &pb.Frame_Ping{Ping: &pb.Ping{}}})
	if readFrame(t, ws).GetPong() == nil {
		t.Fatal("no pong")
	}

	connect(t, ws)
	writeFrame(t, ws, request(2, &pb.ConnectRequest{DeviceToken: "t"}))
	wantError(t, readFrame(t, ws), pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	writeFrame(t, ws, request(3, &pb.ListConversationsRequest{}))
	if resp := readFrame(t, ws).GetResponse(); resp.GetError() != nil || resp.GetRequestId() != 3 {
		t.Fatalf("after connect: %v", resp)
	}
}

func TestConnLimitsRequestsInFlight(t *testing.T) {
	backend := newFakeBackend()
	backend.hold = make(chan struct{})
	_, url := newTestServer(t, backend)
	ws := dialTest(t, url)
	// The read loop counts a request before its goroutine starts, so the
	// one over the limit is refused whatever the scheduling.
	for id := uint64(1); id <= maxInFlight+1; id++ {
		writeFrame(t, ws, request(id, &pb.SignInRequest{Name: "n", Password: "p"}))
	}
	refused := readFrame(t, ws)
	wantError(t, refused, pb.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED)
	if refused.GetResponse().GetRequestId() != maxInFlight+1 {
		t.Fatalf("refused request %d, want %d", refused.GetResponse().GetRequestId(), maxInFlight+1)
	}
	for range maxInFlight {
		<-backend.calls
	}
	close(backend.hold)
	for range maxInFlight {
		if resp := readFrame(t, ws).GetResponse(); resp.GetError() != nil {
			t.Fatalf("held request failed: %v", resp)
		}
	}
}

func TestConnCoalescesAcks(t *testing.T) {
	backend := newFakeBackend()
	_, url := newTestServer(t, backend)
	ws := dialTest(t, url)
	connect(t, ws)

	// The first ack is being written while the rest arrive: one write
	// follows, with the highest cursor.
	writeFrame(t, ws, ack(1))
	if cursor := <-backend.acks; cursor != 1 {
		t.Fatalf("first ack %d, want 1", cursor)
	}
	for cursor := uint64(2); cursor <= 20; cursor++ {
		writeFrame(t, ws, ack(cursor))
	}
	// The pong shows the read loop has taken every ack above.
	writeFrame(t, ws, &pb.Frame{Kind: &pb.Frame_Ping{Ping: &pb.Ping{}}})
	if readFrame(t, ws).GetPong() == nil {
		t.Fatal("no pong")
	}
	backend.ackRelease <- struct{}{}
	if cursor := <-backend.acks; cursor != 20 {
		t.Fatalf("coalesced ack %d, want 20", cursor)
	}
	backend.ackRelease <- struct{}{}
	// Nothing pending: the next ack is written as it is.
	writeFrame(t, ws, ack(21))
	if cursor := <-backend.acks; cursor != 21 {
		t.Fatalf("ack after the writer stopped %d, want 21", cursor)
	}
	backend.ackRelease <- struct{}{}
}

func TestConnClosesSlowConsumer(t *testing.T) {
	s, url := newTestServer(t, newFakeBackend())
	ws := dialTest(t, url)
	connect(t, ws)
	c := s.openConns()[0]

	// Large events, far beyond what the socket buffers hold while the
	// device reads nothing: the writer blocks, the queue fills, and the
	// connection is closed rather than kept waiting.
	event, _ := proto.Marshal(&pb.Event{Cursor: 1, Body: &pb.Event_Message{Message: &pb.MessageEvent{
		Message: &pb.Message{Content: &pb.Message_Sealed{Sealed: &pb.Sealed{Ciphertext: make([]byte, 60<<10)}}},
	}}})
	push, _ := proto.Marshal(&backendv1.Push{Kind: &backendv1.Push_Event{Event: event}})
	for range 400 {
		s.deliver(user1, push)
	}
	for deadline := time.Now().Add(testTimeout); ; time.Sleep(time.Millisecond) {
		c.mu.Lock()
		closing := c.closing
		c.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connection not closing with a full queue")
		}
	}
	if code := readClose(t, ws); code != websocket.StatusTryAgainLater {
		t.Fatalf("closed with %v, want %v", code, websocket.StatusTryAgainLater)
	}
}

func TestConnLetsRequestsFinishDuringDrain(t *testing.T) {
	backend := newFakeBackend()
	backend.hold = make(chan struct{})
	s, url := newTestServer(t, backend)
	ws := dialTest(t, url)
	connect(t, ws)
	writeFrame(t, ws, request(2, &pb.ListConversationsRequest{}))
	<-backend.calls

	var wg sync.WaitGroup
	wg.Go(func() { s.Drain(context.Background()) })
	if code := readClose(t, ws); code != websocket.StatusGoingAway {
		t.Fatalf("closed with %v, want %v", code, websocket.StatusGoingAway)
	}
	// The request runs on after its connection is gone, uncanceled.
	close(backend.hold)
	if err := <-backend.completed; err != nil {
		t.Fatalf("request canceled by the drain: %v", err)
	}
	wg.Wait()

	resp, err := http.Get(strings.Replace(url, "ws", "http", 1))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("upgrade while draining: %d, want 503", resp.StatusCode)
	}
}
