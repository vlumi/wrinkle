// Package gateway holds the WebSocket connections: it dispatches requests
// to the services and pushes events to connected devices.
package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/vlumi/wrinkle/backends/go/conversation"
	"github.com/vlumi/wrinkle/backends/go/devicesync"
	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	"github.com/vlumi/wrinkle/backends/go/presence"
)

type Server struct {
	ctx           context.Context
	conversations *conversation.Service
	users         *devicesync.Service
	presence      *presence.Client
	sub           *presence.Subscription
	registry      *registry
	log           *slog.Logger
}

func New(ctx context.Context, conversations *conversation.Service, users *devicesync.Service, pres *presence.Client, log *slog.Logger) *Server {
	sub := pres.Subscribe(ctx)
	return &Server{
		ctx:           ctx,
		conversations: conversations,
		users:         users,
		presence:      pres,
		sub:           sub,
		registry:      newRegistry(sub),
		log:           log,
	}
}

// Register adds the WebSocket endpoint to a mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/ws", s.serveWS)
}

// Run delivers pushes to local connections until the context ends.
func (s *Server) Run(ctx context.Context) error {
	defer s.sub.Close()
	messages := s.sub.Messages()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-messages:
			if !ok {
				return nil
			}
			userID, ok := presence.UserOf(msg.Channel)
			if !ok {
				continue
			}
			payload := []byte(msg.Payload)
			for _, c := range s.registry.conns(userID) {
				c.pushEvent(payload)
			}
		}
	}
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
		// Any origin: the web client is served from elsewhere during development.
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		s.log.Debug("accept failed", "err", err)
		return
	}
	ws.SetReadLimit(maxFrameBytes)
	metrics.Connections.Inc()
	defer metrics.Connections.Dec()
	// Own context rather than the request's, so the request and its headers
	// are not kept alive for the life of the connection.
	ctx, cancel := context.WithCancel(s.ctx)
	c := &conn{
		server: s,
		ws:     ws,
		log:    s.log.With("remote", r.RemoteAddr),
		ctx:    ctx,
		cancel: cancel,
		out:    make(chan []byte, outboundQueue),
	}
	c.serve()
	ws.Close(websocket.StatusNormalClosure, "")
}

func (s *Server) attach(ctx context.Context, c *conn) error {
	if err := s.registry.add(ctx, c); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	if err := s.presence.DeviceConnected(ctx, c.userID, c.deviceID); err != nil {
		s.log.Warn("presence update failed", "err", err)
	}
	return nil
}

func (s *Server) detach(c *conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.registry.remove(ctx, c); err != nil {
		s.log.Warn("unsubscribe failed", "err", err)
	}
	if err := s.presence.DeviceDisconnected(ctx, c.userID, c.deviceID); err != nil {
		s.log.Warn("presence update failed", "err", err)
	}
}
