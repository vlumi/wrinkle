// Package gateway holds the WebSocket connections: it dispatches requests
// to the services and pushes events to connected devices.
package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/vlumi/wrinkle/backends/go/conversation"
	"github.com/vlumi/wrinkle/backends/go/devicesync"
	"github.com/vlumi/wrinkle/backends/go/presence"
)

type Server struct {
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
		conversations: conversations,
		users:         users,
		presence:      pres,
		sub:           sub,
		registry:      newRegistry(sub),
		log:           log,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ws", s.serveWS)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok connections=%d\n", s.registry.count())
	})
	return mux
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
	c := &conn{
		server: s,
		ws:     ws,
		log:    s.log.With("remote", r.RemoteAddr),
		out:    make(chan []byte, outboundQueue),
		done:   make(chan struct{}),
	}
	c.serve(r.Context())
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

// ListenAndServe serves until the context ends, then drains connections.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	s.log.Info("gateway listening", "addr", addr, "connections", strconv.Itoa(s.registry.count()))
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
