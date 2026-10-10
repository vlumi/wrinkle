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
	outboundQueue  = 256
)

// conn is one device's WebSocket connection.
type conn struct {
	server *Server
	ws     *websocket.Conn
	log    *slog.Logger

	out  chan []byte
	done chan struct{} // closed when the connection is going away

	mu        sync.Mutex
	connected bool
	userID    uint64
	deviceID  uint64
}

func (c *conn) identity() (userID, deviceID uint64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.userID, c.deviceID, c.connected
}

// serve runs the read loop until the connection ends.
func (c *conn) serve(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go c.writeLoop(ctx)
	defer func() {
		close(c.done)
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
					if err := c.server.users.Ack(actx, userID, deviceID, kind.Ack.GetCursor()); err != nil {
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

func (c *conn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-c.out:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Write(wctx, websocket.MessageBinary, data)
			cancel()
			if err != nil {
				c.ws.Close(websocket.StatusInternalError, "write failed")
				return
			}
		}
	}
}

// send queues bytes for the writer. A device that does not keep up is
// disconnected; it syncs on reconnect.
func (c *conn) send(data []byte) {
	select {
	case c.out <- data:
	case <-c.done:
	default:
		c.log.Warn("outbound queue full, closing")
		c.ws.Close(websocket.StatusTryAgainLater, "too slow")
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
	resp := &pb.Response{RequestId: req.GetId()}
	code := "ok"
	if err := c.dispatch(ctx, req, resp); err != nil {
		if apierror.Code(err) == pb.ErrorCode_ERROR_CODE_INTERNAL {
			c.log.Error("request failed", "request", req.GetId(), "err", err)
		}
		resp.Body = &pb.Response_Error{Error: apierror.Proto(err)}
		code = apierror.Code(err).String()
	}
	c.sendFrame(&pb.Frame{Kind: &pb.Frame_Response{Response: resp}})
	kind := requestType(req)
	metrics.Requests.WithLabelValues(kind, code).Inc()
	metrics.RequestDuration.WithLabelValues(kind).Observe(time.Since(start).Seconds())
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
	default:
		return "unknown"
	}
}

// dispatch handles one request, setting the response body on success.
func (c *conn) dispatch(ctx context.Context, req *pb.Request, resp *pb.Response) error {
	if connect := req.GetConnect(); connect != nil {
		return c.connect(ctx, connect, resp)
	}
	userID, _, ok := c.identity()
	if !ok {
		return apierror.Unauthenticated("connect first")
	}
	s := c.server
	switch r := req.GetBody().(type) {
	case *pb.Request_OpenConversation:
		conv, err := s.users.Open(ctx, userID, r.OpenConversation.GetPeerUserName())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_OpenConversation{OpenConversation: &pb.OpenConversationResponse{Conversation: conv}}
	case *pb.Request_SendMessage:
		msg, err := s.conversations.Send(ctx, userID, r.SendMessage.GetConversationId(),
			r.SendMessage.GetClientMessageId(), r.SendMessage.GetText())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_SendMessage{SendMessage: &pb.SendMessageResponse{Message: msg}}
	case *pb.Request_MarkRead:
		if err := s.conversations.MarkRead(ctx, userID, r.MarkRead.GetConversationId(), r.MarkRead.GetSeq()); err != nil {
			return err
		}
		resp.Body = &pb.Response_MarkRead{MarkRead: &pb.MarkReadResponse{}}
	case *pb.Request_Sync:
		sync, err := s.users.Sync(ctx, userID, r.Sync.GetCursor(), r.Sync.GetLimit())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_Sync{Sync: sync}
	case *pb.Request_ListConversations:
		list, err := s.users.ListConversations(ctx, userID)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_ListConversations{ListConversations: &pb.ListConversationsResponse{Conversations: list}}
	case *pb.Request_GetMessages:
		msgs, err := s.conversations.Messages(ctx, userID, r.GetMessages.GetConversationId(),
			r.GetMessages.GetBeforeSeq(), r.GetMessages.GetLimit())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_GetMessages{GetMessages: &pb.GetMessagesResponse{Messages: msgs}}
	default:
		return apierror.InvalidArgument("request has no body")
	}
	return nil
}

func (c *conn) connect(ctx context.Context, req *pb.ConnectRequest, resp *pb.Response) error {
	if _, _, ok := c.identity(); ok {
		return apierror.FailedPrecondition("already connected")
	}
	connected, err := c.server.users.Connect(ctx, req)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.connected {
		c.mu.Unlock()
		return apierror.FailedPrecondition("already connected")
	}
	c.connected = true
	c.userID = connected.GetUser().GetUserId()
	c.deviceID = connected.GetDeviceId()
	c.mu.Unlock()
	if err := c.server.attach(ctx, c); err != nil {
		return err
	}
	c.log = c.log.With("user", c.userID, "device", c.deviceID)
	resp.Body = &pb.Response_Connect{Connect: connected}
	return nil
}
