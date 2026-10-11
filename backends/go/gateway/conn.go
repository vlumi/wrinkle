package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	requestTimeout = 10 * time.Second
	writeTimeout   = 10 * time.Second
	maxFrameBytes  = 64 * 1024
	outboundQueue  = 32
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
	log    *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	out    chan []byte

	mu        sync.Mutex
	connected bool
	userID    uint64
	deviceID  uint64
	writing   bool
}

func newConn(s *Server, ws *websocket.Conn, remoteAddr string) *conn {
	// Own context rather than the request's, so the request and its headers
	// are not kept alive for the life of the connection.
	ctx, cancel := context.WithCancel(s.ctx)
	return &conn{
		server: s,
		ws:     ws,
		log:    s.log.With("remote", remoteAddr),
		ctx:    ctx,
		cancel: cancel,
		out:    make(chan []byte, outboundQueue),
	}
}

func (c *conn) identity() (userID, deviceID uint64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.userID, c.deviceID, c.connected
}

// serve runs the read loop until the connection ends.
func (c *conn) serve() {
	ctx := c.ctx
	defer c.cancel()
	defer func() {
		if _, _, ok := c.identity(); ok {
			c.server.detach(c)
		}
	}()

	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			if status := websocket.CloseStatus(err); status == -1 && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
				c.log.Debug("read ended", "err", err)
			}
			return
		}
		if typ != websocket.MessageBinary {
			c.ws.Close(websocket.StatusUnsupportedData, "frames are binary protobuf")
			return
		}
		frame := &pb.Frame{}
		if err := proto.Unmarshal(data, frame); err != nil {
			c.ws.Close(websocket.StatusPolicyViolation, "malformed frame")
			return
		}
		switch kind := frame.GetKind().(type) {
		case *pb.Frame_Request:
			go c.handleRequest(ctx, kind.Request)
		case *pb.Frame_Ack:
			if userID, deviceID, ok := c.identity(); ok {
				go c.ack(ctx, userID, deviceID, kind.Ack.GetCursor())
			}
		case *pb.Frame_Ping:
			c.sendFrame(&pb.Frame{Kind: &pb.Frame_Pong{Pong: &pb.Pong{}}})
		case *pb.Frame_Pong:
		default:
			c.ws.Close(websocket.StatusPolicyViolation, "unexpected frame")
			return
		}
	}
}

// writeLoop drains the queue and exits when it is empty; send starts it
// again when needed.
func (c *conn) writeLoop() {
	for {
		select {
		case data := <-c.out:
			if err := c.write(data); err != nil {
				c.ws.Close(websocket.StatusInternalError, "write failed")
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

// send queues bytes for the writer, starting one if none is running. A
// device that does not keep up is disconnected; it syncs on reconnect.
func (c *conn) send(data []byte) {
	select {
	case c.out <- data:
	case <-c.ctx.Done():
		return
	default:
		c.log.Warn("outbound queue full, closing")
		c.ws.Close(websocket.StatusTryAgainLater, "too slow")
		return
	}
	c.mu.Lock()
	start := !c.writing
	c.writing = true
	c.mu.Unlock()
	if start {
		go c.writeLoop()
	}
}

func (c *conn) sendFrame(frame *pb.Frame) {
	data, err := proto.Marshal(frame)
	if err != nil {
		c.log.Error("marshal frame", "err", err)
		return
	}
	c.send(data)
}

func (c *conn) pushEvent(payload []byte) {
	data := make([]byte, 0, len(payload)+8)
	data = protowire.AppendTag(data, frameEvent, protowire.BytesType)
	data = protowire.AppendBytes(data, payload)
	c.send(data)
	metrics.EventsPushed.Inc()
}

func (c *conn) ack(ctx context.Context, userID, deviceID, cursor uint64) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := c.server.backend.Ack(ctx, userID, deviceID, cursor); err != nil {
		c.log.Warn("ack failed", "err", err)
	}
}

func (c *conn) handleRequest(ctx context.Context, req *pb.Request) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	start := time.Now()
	resp := c.dispatch(ctx, req)
	c.sendFrame(&pb.Frame{Kind: &pb.Frame_Response{Response: resp}})
	observeRequest(req, resp, start)
}

// dispatch applies the connection's state rules, then hands the request to
// the backend.
func (c *conn) dispatch(ctx context.Context, req *pb.Request) *pb.Response {
	userID, deviceID, connected := c.identity()
	switch {
	case req.GetConnect() != nil && connected:
		return apierror.Response(req.GetId(), apierror.FailedPrecondition("already connected"))
	case req.GetConnect() == nil && !connected:
		return apierror.Response(req.GetId(), apierror.Unauthenticated("connect first"))
	}
	resp, err := c.server.backend.Call(ctx, userID, deviceID, req)
	if err != nil {
		c.log.Error("backend call failed", "request", req.GetId(), "err", err)
		return apierror.Response(req.GetId(), apierror.Unavailable("services unavailable"))
	}
	if connectResp := resp.GetConnect(); connectResp != nil {
		if err := c.identify(ctx, connectResp); err != nil {
			return apierror.Response(req.GetId(), err)
		}
	}
	return resp
}

// identify records the identity Connect established and attaches the
// connection to its user.
func (c *conn) identify(ctx context.Context, resp *pb.ConnectResponse) error {
	c.mu.Lock()
	if c.connected {
		c.mu.Unlock()
		return apierror.FailedPrecondition("already connected")
	}
	c.connected = true
	c.userID = resp.GetUser().GetUserId()
	c.deviceID = resp.GetDeviceId()
	c.mu.Unlock()
	if err := c.server.attach(ctx, c); err != nil {
		return err
	}
	c.log = c.log.With("user", c.userID, "device", c.deviceID)
	return nil
}
