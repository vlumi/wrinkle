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
				go func() {
					actx, cancel := context.WithTimeout(ctx, requestTimeout)
					defer cancel()
					if err := c.server.backend.Ack(actx, userID, deviceID, kind.Ack.GetCursor()); err != nil {
						c.log.Warn("ack failed", "err", err)
					}
				}()
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
		var data []byte
		select {
		case data = <-c.out:
		default:
			c.mu.Lock()
			if len(c.out) == 0 {
				c.writing = false
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
			continue
		}
		wctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
		err := c.ws.Write(wctx, websocket.MessageBinary, data)
		cancel()
		if err != nil {
			c.ws.Close(websocket.StatusInternalError, "write failed")
			c.mu.Lock()
			c.writing = false
			c.mu.Unlock()
			return
		}
	}
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

// pushEvent wraps an already serialized Event in a Frame without decoding
// it: Frame.event is field 3.
func (c *conn) pushEvent(payload []byte) {
	data := make([]byte, 0, len(payload)+8)
	data = protowire.AppendTag(data, 3, protowire.BytesType)
	data = protowire.AppendBytes(data, payload)
	c.send(data)
	metrics.EventsPushed.Inc()
}

func (c *conn) handleRequest(ctx context.Context, req *pb.Request) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	start := time.Now()
	resp := c.dispatch(ctx, req)
	c.sendFrame(&pb.Frame{Kind: &pb.Frame_Response{Response: resp}})
	kind := requestType(req)
	code := "ok"
	if e := resp.GetError(); e != nil {
		code = e.GetCode().String()
	}
	metrics.Requests.WithLabelValues(kind, code).Inc()
	metrics.RequestDuration.WithLabelValues(kind).Observe(time.Since(start).Seconds())
}

// dispatch applies the connection's state rules, then hands the request to
// the backend.
func (c *conn) dispatch(ctx context.Context, req *pb.Request) *pb.Response {
	userID, deviceID, connected := c.identity()
	var err error
	switch {
	case req.GetConnect() != nil && connected:
		err = apierror.FailedPrecondition("already connected")
	case req.GetConnect() == nil && !connected:
		err = apierror.Unauthenticated("connect first")
	}
	if err != nil {
		return &pb.Response{RequestId: req.GetId(), Body: &pb.Response_Error{Error: apierror.Proto(err)}}
	}
	resp, err := c.server.backend.Call(ctx, userID, deviceID, req)
	if err != nil {
		c.log.Error("backend call failed", "request", req.GetId(), "err", err)
		return &pb.Response{RequestId: req.GetId(), Body: &pb.Response_Error{Error: &pb.Error{
			Code: pb.ErrorCode_ERROR_CODE_UNAVAILABLE, Message: "services unavailable",
		}}}
	}
	if connectResp := resp.GetConnect(); connectResp != nil {
		if err := c.attached(ctx, connectResp); err != nil {
			return &pb.Response{RequestId: req.GetId(), Body: &pb.Response_Error{Error: apierror.Proto(err)}}
		}
	}
	return resp
}

// connected_ records the identity Connect established and attaches the
// connection to its user.
func (c *conn) attached(ctx context.Context, resp *pb.ConnectResponse) error {
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

func requestType(req *pb.Request) string {
	switch req.GetBody().(type) {
	case *pb.Request_Connect:
		return "connect"
	case *pb.Request_OpenConversation:
		return "open_conversation"
	case *pb.Request_SendMessage:
		return "send_message"
	case *pb.Request_MarkRead:
		return "mark_read"
	case *pb.Request_Sync:
		return "sync"
	case *pb.Request_ListConversations:
		return "list_conversations"
	case *pb.Request_GetMessages:
		return "get_messages"
	case *pb.Request_RegisterPushToken:
		return "register_push_token"
	default:
		return "unknown"
	}
}
