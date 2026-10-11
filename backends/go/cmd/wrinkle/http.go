package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"runtime"
	rtdebug "runtime/debug"
	"time"

	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
)

// newMux serves the endpoints every role has: metrics, health and
// profiles. The gateway role adds its WebSocket endpoint.
func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	// Profiles for tuning; a prototype has no reason to hide them.
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.Handle("GET /debug/pprof/heap", pprof.Handler("heap"))
	mux.Handle("GET /debug/pprof/goroutine", pprof.Handler("goroutine"))
	mux.Handle("GET /debug/pprof/allocs", pprof.Handler("allocs"))
	// Lets a measurement collect garbage before sampling memory.
	mux.HandleFunc("POST /debug/gc", func(w http.ResponseWriter, r *http.Request) {
		runtime.GC()
		rtdebug.FreeOSMemory()
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	return mux
}

// serveHTTP serves until the context ends. WebSocket connections are
// hijacked, so shutdown does not wait for them; the registry drains them.
func serveHTTP(ctx context.Context, addr string, handler http.Handler, log *slog.Logger) error {
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", addr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
