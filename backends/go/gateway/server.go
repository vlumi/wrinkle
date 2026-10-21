// Package gateway holds the WebSocket connections: it dispatches requests
// to the services and pushes events to connected devices.
package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	"github.com/vlumi/wrinkle/backends/go/presence"
	"github.com/vlumi/wrinkle/backends/go/services"
)

const (
	// requestGrace is how long a drain waits for requests in flight.
	requestGrace = 5 * time.Second
	// closeGrace is how long a drain waits, after requestGrace, for closing
	// handshakes before dropping the connections that remain.
	closeGrace = 3 * time.Second
	// detachGrace is how long dropped connections get to leave presence.
	detachGrace = time.Second
)

// presenceClient is what the server needs from presence; the registry
// holds the client itself, for its subscription.
type presenceClient interface {
	Announce(ctx context.Context) error
	Withdraw(ctx context.Context) error
	Instance() string
	DeviceConnected(ctx context.Context, userID ids.UUID, deviceID uint64) error
	DeviceDisconnected(ctx context.Context, userID ids.UUID, deviceID uint64) error
}

type Server struct {
	// ctx is the connections' base context, ended once the gateway drained;
	// requestCtx is the requests', ended once the drain stops waiting for
	// them.
	ctx            context.Context
	cancel         context.CancelFunc
	requestCtx     context.Context
	cancelRequests context.CancelFunc

	backend  services.Backend
	presence presenceClient
	// ackInterval is the least time between two ack writes of a connection.
	ackInterval time.Duration
	registry    *registry
	log         *slog.Logger

	requests sync.WaitGroup
	open     sync.WaitGroup
	draining atomic.Bool
	connsMu  sync.Mutex
	conns    map[*conn]struct{}
}

// New announces the gateway instance in presence and returns its server.
func New(ctx context.Context, backend services.Backend, pres *presence.Client, ackInterval time.Duration, log *slog.Logger) (*Server, error) {
	if err := pres.Announce(ctx); err != nil {
		return nil, fmt.Errorf("announce gateway instance: %w", err)
	}
	base, cancel := context.WithCancel(context.Background())
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	log.Info("gateway instance", "instance", pres.Instance())
	return &Server{
		ctx:            base,
		cancel:         cancel,
		requestCtx:     requestCtx,
		cancelRequests: cancelRequests,
		backend:        backend,
		presence:       pres,
		ackInterval:    ackInterval,
		registry:       newRegistry(pres, log),
		log:            log,
		conns:          map[*conn]struct{}{},
	}, nil
}

// Register adds the WebSocket endpoint to a mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/ws", s.serveWS)
}

// Run keeps the instance and its connections alive until the context ends,
// and delivers pushes to local connections until the gateway has drained.
func (s *Server) Run(ctx context.Context) error {
	go s.keepalive(ctx)
	go s.registry.reconcile(s.ctx)
	s.registry.run(s.ctx, s.deliver)
	return nil
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	if s.draining.Load() {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	c := newConn(s, r.RemoteAddr)
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
		// Any origin: the web client is served from elsewhere during development.
		OriginPatterns: []string{"*"},
		OnPongReceived: func(context.Context, []byte) { c.touch() },
	})
	if err != nil {
		s.log.Debug("accept failed", "err", err)
		return
	}
	c.ws = ws
	ws.SetReadLimit(maxFrameBytes)
	metrics.Connections.Inc()
	defer metrics.Connections.Dec()
	s.track(c)
	defer s.untrack(c)
	c.serve()
	ws.Close(websocket.StatusNormalClosure, "")
}

// track records an open connection; one opened as the drain began is
// closed at once. A connection may be added while Drain already waits on
// open, which is fine: it sees draining under the same lock and closes, so
// the wait only grows by one closing handshake.
func (s *Server) track(c *conn) {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	s.open.Add(1)
	s.conns[c] = struct{}{}
	if s.draining.Load() {
		c.close(websocket.StatusGoingAway, "shutting down")
	}
}

func (s *Server) untrack(c *conn) {
	s.connsMu.Lock()
	delete(s.conns, c)
	s.connsMu.Unlock()
	s.open.Done()
}

func (s *Server) openConns() []*conn {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	list := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		list = append(list, c)
	}
	return list
}

func (s *Server) attach(ctx context.Context, c *conn) error {
	subCtx, cancel := context.WithTimeout(ctx, subscribeTimeout)
	defer cancel()
	if err := s.registry.add(subCtx, c); err != nil {
		s.registry.remove(c)
		s.log.Warn("subscribing failed", "user", c.userID, "err", err)
		return apierror.Unavailable("push subscription unavailable")
	}
	if err := s.presence.DeviceConnected(ctx, c.userID, c.deviceID); err != nil {
		s.log.Warn("presence update failed", "err", err)
	}
	return nil
}

func (s *Server) detach(c *conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.registry.remove(c)
	if err := s.presence.DeviceDisconnected(ctx, c.userID, c.deviceID); err != nil {
		s.log.Warn("presence update failed", "err", err)
	}
}

// Drain refuses new connections, closes the open ones, gives requests in
// flight up to requestGrace and withdraws the instance from presence. It
// returns within requestGrace+closeGrace+detachGrace, sooner if the
// context ends.
func (s *Server) Drain(ctx context.Context) {
	s.connsMu.Lock()
	s.draining.Store(true)
	s.connsMu.Unlock()
	conns := s.openConns()
	s.log.Info("draining", "connections", len(conns))
	for _, c := range conns {
		c.close(websocket.StatusGoingAway, "shutting down")
	}

	if !waitFor(ctx, requestGrace, &s.requests) {
		s.log.Warn("requests still in flight after the grace period")
	}
	s.cancelRequests()
	if !waitFor(ctx, closeGrace, &s.open) {
		// Canceling the read closes the socket; CloseNow would wait for the
		// closing handshake already under way.
		for _, c := range s.openConns() {
			c.cancel()
		}
		waitFor(ctx, detachGrace, &s.open)
	}
	s.cancel()

	withdrawCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), detachGrace)
	defer cancel()
	if err := s.presence.Withdraw(withdrawCtx); err != nil {
		s.log.Warn("withdrawing the instance failed", "err", err)
	}
	s.registry.close()
}

// waitFor waits for the group up to the timeout or the context's end, and
// reports whether it finished.
func waitFor(ctx context.Context, timeout time.Duration, wg *sync.WaitGroup) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
	case <-ctx.Done():
	}
	return false
}
