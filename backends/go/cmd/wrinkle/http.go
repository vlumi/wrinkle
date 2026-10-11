package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	rtdebug "runtime/debug"
	"sync/atomic"
	"time"

	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
)

// httpShutdown bounds the HTTP server's shutdown, after the gateway drained.
const httpShutdown = time.Second

// newMux serves the endpoints every role has: metrics, health and
// profiles. The gateway role adds its WebSocket endpoint. Health answers
// 503 once draining is set.
func newMux(draining *atomic.Bool) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	// Profiles for tuning, from the machine itself only.
	mux.Handle("GET /debug/pprof/", loopbackOnly(http.HandlerFunc(pprof.Index)))
	mux.Handle("GET /debug/pprof/profile", loopbackOnly(http.HandlerFunc(pprof.Profile)))
	mux.Handle("GET /debug/pprof/heap", loopbackOnly(pprof.Handler("heap")))
	mux.Handle("GET /debug/pprof/goroutine", loopbackOnly(pprof.Handler("goroutine")))
	mux.Handle("GET /debug/pprof/allocs", loopbackOnly(pprof.Handler("allocs")))
	// Lets a measurement collect garbage before sampling memory.
	mux.Handle("POST /debug/gc", loopbackOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runtime.GC()
		rtdebug.FreeOSMemory()
		fmt.Fprintln(w, "ok")
	})))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if draining.Load() {
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	return mux
}

// loopbackOnly refuses requests that do not come from a loopback address.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r.RemoteAddr) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// serveHTTP serves until the context ends. WebSocket connections are
// hijacked, so shutdown does not wait for them: the gateway drains them
// before the context ends.
func serveHTTP(ctx context.Context, addr string, handler http.Handler, log *slog.Logger) error {
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", addr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdown)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return nil
	}
}
