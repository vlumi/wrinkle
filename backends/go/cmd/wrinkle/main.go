// Command wrinkle is the Go backend: one binary whose roles are chosen by
// configuration. The gateway role serves WebSocket connections with the
// conversation and sync services in the same process; the fanout role
// consumes the outbox from Kafka.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	rtdebug "runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vlumi/wrinkle/backends/go/conversation"
	"github.com/vlumi/wrinkle/backends/go/devicesync"
	"github.com/vlumi/wrinkle/backends/go/fanout"
	"github.com/vlumi/wrinkle/backends/go/gateway"
	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	"github.com/vlumi/wrinkle/backends/go/presence"
	"github.com/vlumi/wrinkle/backends/go/services"
)

func main() {
	if err := run(); err != nil {
		slog.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run() error {
	hostname, _ := os.Hostname()
	var (
		roles   = flag.String("roles", env("WRINKLE_ROLES", "gateway,fanout"), "comma-separated roles: gateway, fanout, services")
		listen  = flag.String("listen", env("WRINKLE_LISTEN", ":8080"), "HTTP listen address: /v1/ws (gateway role), /metrics, /healthz")
		mysql   = flag.String("mysql", env("WRINKLE_MYSQL", "127.0.0.1:15003"), "vtgate MySQL address")
		brokers = flag.String("kafka", env("WRINKLE_KAFKA", "localhost:19092"), "comma-separated Kafka brokers")
		valkey  = flag.String("valkey", env("WRINKLE_VALKEY", "localhost:6379"), "Valkey address")
		svcAddr = flag.String("services", env("WRINKLE_SERVICES", ""), "gateway: forward requests to a services process at this address instead of running the services in-process")
		svcLsn  = flag.String("services-listen", env("WRINKLE_SERVICES_LISTEN", ":9000"), "services role: gRPC listen address")
		node    = flag.String("node", env("WRINKLE_NODE", hostname), "this node's name in presence")
		dbConns = flag.Int("db-conns", 64, "connections per keyspace")
		fanoutN = flag.Int("fanout-concurrency", 64, "fanout: conversations delivered at a time")
		debug   = flag.Bool("debug", false, "debug logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	wantGateway, wantFanout, wantServices := false, false, false
	for _, role := range strings.Split(*roles, ",") {
		switch strings.TrimSpace(role) {
		case "gateway":
			wantGateway = true
		case "fanout":
			wantFanout = true
		case "services":
			wantServices = true
		default:
			return fmt.Errorf("unknown role %q", role)
		}
	}

	// The database is needed by the services (in-process or as a role) and
	// by fan-out; a gateway forwarding to a services process does without.
	var local *services.Local
	var userDB *sql.DB
	if wantServices || wantFanout || (wantGateway && *svcAddr == "") {
		conversationDB, err := vtdb.Open(ctx, *mysql, "conversation", *dbConns)
		if err != nil {
			return err
		}
		defer conversationDB.Close()
		userDB, err = vtdb.Open(ctx, *mysql, "user", *dbConns)
		if err != nil {
			return err
		}
		defer userDB.Close()
		conversations := conversation.New(conversationDB)
		users := devicesync.New(userDB, conversations)
		local = services.NewLocal(conversations, users, log.With("role", "services"))
	}
	pres, err := presence.New(ctx, *valkey, *node)
	if err != nil {
		return err
	}
	defer pres.Close()

	var wg sync.WaitGroup
	errc := make(chan error, 2)
	start := func(name string, f func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(ctx); err != nil && ctx.Err() == nil {
				errc <- fmt.Errorf("%s: %w", name, err)
			}
		}()
	}

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
	if wantServices {
		srv := services.NewServer(local)
		log.Info("services listening", "addr", *svcLsn)
		start("services", func(ctx context.Context) error { return srv.Serve(ctx, *svcLsn) })
	}
	if wantGateway {
		var backend services.Backend = local
		if *svcAddr != "" {
			client, err := services.Dial(*svcAddr)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			backend = client
			log.Info("gateway forwarding to services", "addr", *svcAddr)
		}
		gw := gateway.New(ctx, backend, pres, log.With("role", "gateway"))
		gw.Register(mux)
		start("gateway push", gw.Run)
	}
	start("http", func(ctx context.Context) error { return serveHTTP(ctx, *listen, mux, log) })
	if wantFanout {
		worker, err := fanout.New(ctx, strings.Split(*brokers, ","), "wrinkle-fanout", *fanoutN, userDB, pres, log.With("role", "fanout"))
		if err != nil {
			return err
		}
		defer worker.Close()
		log.Info("fanout consuming", "topic", fanout.Topic)
		start("fanout", worker.Run)
	}

	select {
	case err := <-errc:
		stop()
		wg.Wait()
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		wg.Wait()
		return nil
	}
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

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
