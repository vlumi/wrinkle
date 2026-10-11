package gateway

import (
	"context"
	"time"

	"github.com/coder/websocket"

	"github.com/vlumi/wrinkle/backends/go/presence"
)

// idleTimeout closes a connection nothing has arrived on, pongs included,
// for this long. Pings go out every presence.HeartbeatInterval, so a live
// client answers several times within it.
const idleTimeout = 90 * time.Second

// keepalive refreshes the instance's liveness in presence and sweeps the
// connections on one ticker: one goroutine per gateway rather than a timer
// per connection.
func (s *Server) keepalive(ctx context.Context) {
	ticker := time.NewTicker(presence.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.presence.Announce(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("presence heartbeat failed", "err", err)
			}
			s.sweep(time.Now())
		}
	}
}

// sweep closes idle connections and pings the others; the pongs, seen by
// the read loop, keep them from going idle.
func (s *Server) sweep(now time.Time) {
	cutoff := now.Add(-idleTimeout)
	for _, c := range s.openConns() {
		if c.idleSince().Before(cutoff) {
			c.close(websocket.StatusGoingAway, "idle")
			continue
		}
		go c.ping()
	}
}

// ping sends a WebSocket ping. The pong is recorded by the connection's
// pong callback; waiting for it here only bounds the goroutine.
func (c *conn) ping() {
	ctx, cancel := context.WithTimeout(c.ctx, presence.HeartbeatInterval)
	defer cancel()
	_ = c.ws.Ping(ctx)
}
