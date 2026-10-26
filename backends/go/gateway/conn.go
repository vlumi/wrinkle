package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	requestTimeout = 10 * time.Second
	writeTimeout   = 10 * time.Second
	connectTimeout = 5 * time.Second
	// maxSignIns bounds the Register and SignIn requests of a connection.
	maxSignIns = 5
	// How long Connect waits for the user's push channel to be subscribed.
	subscribeTimeout = 5 * time.Second
	maxFrameBytes    = 64 * 1024
	outboundQueue    = 32
)

// frameEvent is the number of Frame's event field, for wrapping an already
// serialized Event without decoding it.
var frameEvent = (&pb.Frame{}).ProtoReflect().Descriptor().Fields().ByName("event").Number()

// conn is one device's WebSocket connection. Idle, it is one goroutine (the
// read loop) and a small queue: the writer runs only while there is
// something to write.
type conn struct {
	server *Server
	ws     *websocket.Conn
	// log gains the identity once connected, from the request goroutine,
	// while the read loop and the registry log through it too.
	log atomic.Pointer[slog.Logger]

	ctx    context.Context
	cancel context.CancelFunc
	out    chan []byte

	// lastSeen is when anything, a pong included, last arrived, in Unix
	// nanoseconds; the keepalive sweep closes connections idle too long.
	lastSeen atomic.Int64
	inFlight atomic.Int32
	bucket   tokenBucket
	acks     ackCursor

	mu           sync.Mutex
	connected    bool
	closing      bool
	writing      bool
	userID       ids.UUID
	deviceID     uint64
	connectTimer *time.Timer
	// signIns counts Register and SignIn requests, which may cost a
	// password hash each; those refused as invalid, before hashing, do not
	// count.
	signIns int
}

func newConn(s *Server, remoteAddr string) *conn {
	// Own context rather than the request's, so the request and its headers
	// are not kept alive for the life of the connection.
	ctx, cancel := context.WithCancel(s.ctx)
	now := time.Now()
	c := &conn{
		server: s,
		ctx:    ctx,
		cancel: cancel,
		out:    make(chan []byte, outboundQueue),
		bucket: newTokenBucket(now),
	}
	c.log.Store(s.log.With("remote", remoteAddr))
	c.lastSeen.Store(now.UnixNano())
	return c
}

func (c *conn) logger() *slog.Logger { return c.log.Load() }

func (c *conn) identity() (userID ids.UUID, deviceID uint64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.userID, c.deviceID, c.connected
}

func (c *conn) touch() { c.lastSeen.Store(time.Now().UnixNano()) }

func (c *conn) idleSince() time.Time { return time.Unix(0, c.lastSeen.Load()) }

// close starts the closing handshake with the code, once, without waiting
// for it.
func (c *conn) close(code websocket.StatusCode, reason string) {
	if c.startClosing() {
		if code == websocket.StatusTryAgainLater {
			c.logger().Warn("outbound queue full, closing")
		}
		go func() { _ = c.ws.Close(code, reason) }()
	}
}

// fail is close for the read loop, which has nothing more to read and so
// runs the handshake itself.
func (c *conn) fail(code websocket.StatusCode, reason string) {
	if c.startClosing() {
		c.ws.Close(code, reason)
	}
}

// startClosing marks the connection closing and reports whether the caller
// is the first to.
func (c *conn) startClosing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	closing := c.closing
	c.closing = true
	return !closing
}

// serve runs the read loop until the connection ends.
func (c *conn) serve() {
	ctx := c.ctx
	c.startConnectTimer()
	defer func() {
		// Cancel before detaching: a Connect finishing concurrently sees the
		// cancellation and detaches itself (see identify).
		c.cancel()
		c.stopConnectTimer()
		if userID, deviceID, ok := c.identity(); ok {
			c.server.detach(c)
			if c.acks.close() {
				c.writeAcks(userID, deviceID)
			}
		}
	}()

	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			if status := websocket.CloseStatus(err); status == -1 && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
				c.logger().Debug("read ended", "err", err)
			}
			return
		}
		now := time.Now()
		c.lastSeen.Store(now.UnixNano())
		if typ != websocket.MessageBinary {
			c.fail(websocket.StatusUnsupportedData, "frames are binary protobuf")
			return
		}
		frame := &pb.Frame{}
		if err := proto.Unmarshal(data, frame); err != nil {
			c.fail(websocket.StatusPolicyViolation, "malformed frame")
			return
		}
		allowed := c.bucket.take(now)
		switch kind := frame.GetKind().(type) {
		case *pb.Frame_Request:
			c.startRequest(kind.Request, allowed)
		case *pb.Frame_Ack:
			if userID, deviceID, ok := c.identity(); ok && allowed && c.acks.offer(kind.Ack.GetCursor()) {
				go c.writeAcks(userID, deviceID)
			}
		case *pb.Frame_Ping:
			if allowed {
				c.reply(&pb.Frame{Kind: &pb.Frame_Pong{Pong: &pb.Pong{}}})
			}
		case *pb.Frame_Pong:
		default:
			c.fail(websocket.StatusPolicyViolation, "unexpected frame")
			return
		}
	}
}

// startConnectTimer closes the connection if Connect has not completed
// within connectTimeout. A connection that has ended gets no timer: serve
// stops the timer on its way out, and a request finishing later must not
// start another.
func (c *conn) startConnectTimer() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		return
	}
	c.connectTimer = time.AfterFunc(connectTimeout, func() {
		if _, _, ok := c.identity(); !ok {
			c.close(websocket.StatusPolicyViolation, "connect first")
		}
	})
}

func (c *conn) stopConnectTimer() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connectTimer != nil {
		c.connectTimer.Stop()
		c.connectTimer = nil
	}
}

// startRequest handles a request in the background, unless the connection
// is over its limits.
func (c *conn) startRequest(req *pb.Request, allowed bool) {
	if !allowed || c.inFlight.Load() >= maxInFlight {
		resp := apierror.Response(req.GetId(), apierror.ResourceExhausted("too many requests"))
		c.reply(&pb.Frame{Kind: &pb.Frame_Response{Response: resp}})
		observeRequest(req, resp, time.Now())
		return
	}
	c.inFlight.Add(1)
	c.server.requests.Add(1)
	go func() {
		defer c.server.requests.Done()
		defer c.inFlight.Add(-1)
		c.handleRequest(req)
	}()
}

// writeLoop drains the queue and exits when it is empty; send starts it
// again when needed.
func (c *conn) writeLoop() {
	for {
		select {
		case data := <-c.out:
			if err := c.write(data); err != nil {
				c.close(websocket.StatusInternalError, "write failed")
				c.mu.Lock()
				c.writing = false
				c.mu.Unlock()
				return
			}
		default:
			if c.stopWritingIfIdle() {
				return
			}
		}
	}
}

// stopWritingIfIdle clears the writing flag if the queue is still empty,
// under the lock send takes to decide whether to start a writer.
func (c *conn) stopWritingIfIdle() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.out) > 0 {
		return false
	}
	c.writing = false
	return true
}

func (c *conn) write(data []byte) error {
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageBinary, data)
}

// send queues bytes for the writer, starting one if none is running, and
// reports whether they were queued. A device that does not keep up is
// disconnected; it syncs on reconnect. Pushed events never wait for room in
// the queue; replies to the device's own frames wait up to writeTimeout,
// which holds back its read loop.
func (c *conn) send(data []byte, reply bool) bool {
	select {
	case c.out <- data:
	case <-c.ctx.Done():
		return false
	default:
		if !reply || !c.sendWaiting(data) {
			c.close(websocket.StatusTryAgainLater, "too slow")
			return false
		}
	}
	c.mu.Lock()
	start := !c.writing
	c.writing = true
	c.mu.Unlock()
	if start {
		go c.writeLoop()
	}
	return true
}

func (c *conn) sendWaiting(data []byte) bool {
	timer := time.NewTimer(writeTimeout)
	defer timer.Stop()
	select {
	case c.out <- data:
		return true
	case <-c.ctx.Done():
	case <-timer.C:
	}
	return false
}

// reply sends a frame answering one of the device's own.
func (c *conn) reply(frame *pb.Frame) {
	data, err := proto.Marshal(frame)
	if err != nil {
		c.logger().Error("marshal frame", "err", err)
		return
	}
	c.send(data, true)
}

func (c *conn) pushEvent(payload []byte) {
	data := make([]byte, 0, len(payload)+8)
	data = protowire.AppendTag(data, frameEvent, protowire.BytesType)
	data = protowire.AppendBytes(data, payload)
	if c.send(data, false) {
		metrics.EventsPushed.Inc()
	}
}

// writeAcks writes the pending ack cursor until none is left or the next
// write has to wait for the interval, when a timer starts it again.
func (c *conn) writeAcks(userID ids.UUID, deviceID uint64) {
	schedule := func(wait time.Duration) *time.Timer {
		return time.AfterFunc(wait, func() { c.writeAcks(userID, deviceID) })
	}
	for {
		cursor := c.acks.next(time.Now(), c.server.ackInterval, schedule)
		if cursor == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(c.server.requestCtx, requestTimeout)
		err := c.server.backend.Ack(ctx, userID, deviceID, cursor)
		cancel()
		if err != nil {
			c.logger().Warn("ack failed", "err", err)
		}
	}
}

// handleRequest runs a request under the server's request context rather
// than the connection's, so a drain lets it finish after the connection
// closes.
func (c *conn) handleRequest(req *pb.Request) {
	ctx, cancel := context.WithTimeout(c.server.requestCtx, requestTimeout)
	defer cancel()
	start := time.Now()
	resp := c.dispatch(ctx, req)
	c.reply(&pb.Frame{Kind: &pb.Frame_Response{Response: resp}})
	observeRequest(req, resp, start)
}

// dispatch applies the connection's state rules, then hands the request to
// the backend.
func (c *conn) dispatch(ctx context.Context, req *pb.Request) *pb.Response {
	userID, deviceID, connected := c.identity()
	switch req.GetBody().(type) {
	case *pb.Request_Register, *pb.Request_SignIn, *pb.Request_Connect:
		if connected {
			return apierror.Response(req.GetId(), apierror.FailedPrecondition(pb.ErrorReason_ERROR_REASON_ALREADY_CONNECTED, "already connected"))
		}
		if req.GetConnect() == nil && !c.countSignIn() {
			return apierror.Response(req.GetId(), apierror.ResourceExhausted("at most %d sign-ins per connection", maxSignIns))
		}
	default:
		if !connected {
			return apierror.Response(req.GetId(), apierror.Unauthenticated("connect first"))
		}
	}
	resp, err := c.server.backend.Call(ctx, userID, deviceID, req)
	if err == nil && resp.GetError() != nil && ctx.Err() != nil {
		// The in-process services report a deadline as an internal error.
		err = ctx.Err()
	}
	if err != nil {
		c.logger().Error("backend call failed", "request", req.GetId(), "err", err)
		return apierror.Response(req.GetId(), apierror.Unavailable("services unavailable"))
	}
	if connectResp := resp.GetConnect(); connectResp != nil {
		if err := c.identify(ctx, connectResp); err != nil {
			return apierror.Response(req.GetId(), err)
		}
	}
	if (req.GetRegister() != nil || req.GetSignIn() != nil) && resp.GetError().GetCode() == pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		c.uncountSignIn()
	}
	if (req.GetRegister() != nil || req.GetSignIn() != nil) && resp.GetError() == nil {
		// Hashing the password may have taken a while; Connect gets a fresh
		// deadline. A failed attempt does not, so it cannot hold the
		// connection open.
		c.stopConnectTimer()
		c.startConnectTimer()
	}
	return resp
}

// countSignIn counts a Register or SignIn and reports whether it is within
// the connection's limit.
func (c *conn) countSignIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.signIns >= maxSignIns {
		return false
	}
	c.signIns++
	return true
}

func (c *conn) uncountSignIn() {
	c.mu.Lock()
	c.signIns--
	c.mu.Unlock()
}

// identify records the identity Connect established and attaches the
// connection to its user.
func (c *conn) identify(ctx context.Context, resp *pb.ConnectResponse) error {
	c.mu.Lock()
	if c.connected {
		c.mu.Unlock()
		return apierror.FailedPrecondition(pb.ErrorReason_ERROR_REASON_ALREADY_CONNECTED, "already connected")
	}
	userID, ok := ids.Parse(resp.GetUser().GetUserId())
	if !ok {
		c.mu.Unlock()
		return apierror.New(pb.ErrorCode_ERROR_CODE_INTERNAL, "connect returned no user id")
	}
	c.connected = true
	c.userID = userID
	c.deviceID = resp.GetDeviceId()
	c.mu.Unlock()
	c.stopConnectTimer()
	if err := c.server.attach(ctx, c); err != nil {
		// Not connected after all: Connect may be retried, within a fresh deadline.
		c.mu.Lock()
		c.connected, c.userID, c.deviceID = false, ids.UUID{}, 0
		c.mu.Unlock()
		c.startConnectTimer()
		return err
	}
	if c.ctx.Err() != nil {
		// The connection ended while connecting; serve may have missed the
		// attachment.
		c.server.detach(c)
		return apierror.Unavailable("connection closed")
	}
	c.log.Store(c.logger().With("user", userID, "device", resp.GetDeviceId()))
	return nil
}
