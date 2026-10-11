// Package wrinkleclient is a Go client for the contract, used by the
// conformance tests and the load generator. One Client is one device.
package wrinkleclient

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

type Client struct {
	ws     *websocket.Conn
	nextID atomic.Uint64

	// Events receives pushed events; the reader blocks while EventBuffer
	// of them wait. Pongs receives pongs, dropping any beyond a few unread.
	Events chan *pb.Event
	Pongs  chan struct{}

	mu      sync.Mutex
	pending map[uint64]chan *pb.Response
	err     error
}

const EventBuffer = 1024

// Dial opens a connection; the caller still has to Connect.
func Dial(ctx context.Context, url string) (*Client, error) {
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(1 << 20)
	c := &Client{
		ws:      ws,
		Events:  make(chan *pb.Event, EventBuffer),
		Pongs:   make(chan struct{}, 16),
		pending: map[uint64]chan *pb.Response{},
	}
	go c.readLoop()
	return c, nil
}

func (c *Client) Close() error {
	return c.ws.Close(websocket.StatusNormalClosure, "")
}

// Err reports why the connection ended, once it has.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) readLoop() {
	ctx := context.Background()
	var err error
	defer func() {
		c.mu.Lock()
		c.err = err
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
		close(c.Events)
	}()
	for {
		typ, data, rerr := c.ws.Read(ctx)
		if rerr != nil {
			err = rerr
			return
		}
		if typ != websocket.MessageBinary {
			err = fmt.Errorf("unexpected %v message", typ)
			return
		}
		frame := &pb.Frame{}
		if uerr := proto.Unmarshal(data, frame); uerr != nil {
			err = fmt.Errorf("malformed frame: %w", uerr)
			return
		}
		switch kind := frame.GetKind().(type) {
		case *pb.Frame_Response:
			if ch, ok := c.takePending(kind.Response.GetRequestId()); ok {
				ch <- kind.Response
			}
		case *pb.Frame_Event:
			c.Events <- kind.Event
		case *pb.Frame_Pong:
			select {
			case c.Pongs <- struct{}{}:
			default:
			}
		case *pb.Frame_Ping:
			_ = c.Send(ctx, &pb.Frame{Kind: &pb.Frame_Pong{Pong: &pb.Pong{}}})
		}
	}
}

// Send writes one frame.
func (c *Client) Send(ctx context.Context, frame *pb.Frame) error {
	data, err := proto.Marshal(frame)
	if err != nil {
		return err
	}
	return c.ws.Write(ctx, websocket.MessageBinary, data)
}

// Call sends a request and waits for its response. Error responses are
// returned as *Error.
func (c *Client) Call(ctx context.Context, req *pb.Request) (*pb.Response, error) {
	req.Id = c.nextID.Add(1)
	ch := make(chan *pb.Response, 1)
	c.mu.Lock()
	c.pending[req.Id] = ch
	c.mu.Unlock()
	if err := c.Send(ctx, &pb.Frame{Kind: &pb.Frame_Request{Request: req}}); err != nil {
		c.takePending(req.Id)
		return nil, err
	}
	select {
	case <-ctx.Done():
		c.takePending(req.Id)
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("connection closed: %w", c.Err())
		}
		if e := resp.GetError(); e != nil {
			return resp, &Error{Code: e.GetCode(), Message: e.GetMessage()}
		}
		return resp, nil
	}
}

// takePending removes the call waiting for a response, if there is one.
func (c *Client) takePending(id uint64) (chan *pb.Response, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	return ch, ok
}
