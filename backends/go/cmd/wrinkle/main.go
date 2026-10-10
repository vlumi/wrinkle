// Command wrinkle is the Go backend: one binary whose roles are chosen by
// configuration. The gateway role serves WebSocket connections with the
// conversation and sync services in the same process; the fanout role
// consumes the outbox from Kafka.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/vlumi/wrinkle/backends/go/conversation"
	"github.com/vlumi/wrinkle/backends/go/devicesync"
	"github.com/vlumi/wrinkle/backends/go/fanout"
	"github.com/vlumi/wrinkle/backends/go/gateway"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	"github.com/vlumi/wrinkle/backends/go/presence"
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
		roles   = flag.String("roles", env("WRINKLE_ROLES", "gateway,fanout"), "comma-separated roles: gateway, fanout")
		listen  = flag.String("listen", env("WRINKLE_LISTEN", ":8080"), "gateway listen address")
		mysql   = flag.String("mysql", env("WRINKLE_MYSQL", "127.0.0.1:15003"), "vtgate MySQL address")
		brokers = flag.String("kafka", env("WRINKLE_KAFKA", "localhost:19092"), "comma-separated Kafka brokers")
		valkey  = flag.String("valkey", env("WRINKLE_VALKEY", "localhost:6379"), "Valkey address")
		node    = flag.String("node", env("WRINKLE_NODE", hostname), "this node's name in presence")
		dbConns = flag.Int("db-conns", 32, "connections per keyspace")
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

	wantGateway, wantFanout := false, false
	for _, role := range strings.Split(*roles, ",") {
		switch strings.TrimSpace(role) {
		case "gateway":
			wantGateway = true
		case "fanout":
			wantFanout = true
		default:
			return fmt.Errorf("unknown role %q", role)
		}
	}

	conversationDB, err := vtdb.Open(ctx, *mysql, "conversation", *dbConns)
	if err != nil {
		return err
	}
	defer conversationDB.Close()
	userDB, err := vtdb.Open(ctx, *mysql, "user", *dbConns)
	if err != nil {
		return err
	}
	defer userDB.Close()
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

	if wantGateway {
		conversations := conversation.New(conversationDB)
		users := devicesync.New(userDB, conversations)
		gw := gateway.New(ctx, conversations, users, pres, log.With("role", "gateway"))
		start("gateway push", gw.Run)
		start("gateway", func(ctx context.Context) error { return gw.ListenAndServe(ctx, *listen) })
	}
	if wantFanout {
		worker, err := fanout.New(ctx, strings.Split(*brokers, ","), "wrinkle-fanout", userDB, pres, log.With("role", "fanout"))
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

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
