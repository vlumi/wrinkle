// Command wrinkle is the Go backend: one binary whose roles are chosen by
// configuration. The gateway role serves WebSocket connections, with the
// conversation and sync services in the same process unless it forwards to
// a services process; the services role serves them over the internal RPC;
// the fanout role consumes the outbox from Kafka.
package main

import (
	"context"
	"database/sql"
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
	"github.com/vlumi/wrinkle/backends/go/push"
	"github.com/vlumi/wrinkle/backends/go/services"
)

func main() {
	if err := run(); err != nil {
		slog.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run() error {
	dotenv := loadDotEnv()
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
		apnsKey = flag.String("apns-key", env("WRINKLE_APNS_KEY", ""), "fanout: APNs .p8 key file; push notifications are off without it")
		apnsKID = flag.String("apns-key-id", env("WRINKLE_APNS_KEY_ID", ""), "fanout: APNs key id; defaults to the id in the key's file name")
		apnsTID = flag.String("apns-team-id", env("WRINKLE_TEAM_ID", ""), "fanout: Apple developer team id")
		apnsTop = flag.String("apns-topic", env("WRINKLE_APNS_TOPIC", "fi.misaki.wrinkle"), "fanout: the iOS app's bundle id")
		debug   = flag.Bool("debug", false, "debug logging")
	)
	flag.Parse()

	log := newLogger(*debug)
	if dotenv != "" {
		log.Info("settings read", "file", dotenv)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	want, err := parseRoles(*roles)
	if err != nil {
		return err
	}

	// The database is needed by the services (in-process or as a role) and
	// by fan-out; a gateway forwarding to a services process does without.
	var local *services.Local
	var userDB *sql.DB
	if want.services || want.fanout || (want.gateway && *svcAddr == "") {
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

	mux := newMux()
	if want.services {
		srv := services.NewServer(local)
		log.Info("services listening", "addr", *svcLsn)
		start("services", func(ctx context.Context) error { return srv.Serve(ctx, *svcLsn) })
	}
	if want.gateway {
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
	if want.fanout {
		var notifier fanout.Notifier
		if *apnsKey != "" {
			n, err := newNotifier(push.APNsConfig{KeyFile: *apnsKey, KeyID: *apnsKID, TeamID: *apnsTID, Topic: *apnsTop},
				userDB, pres, log.With("role", "push"))
			if err != nil {
				return err
			}
			notifier = n
			log.Info("push notifications on", "topic", *apnsTop)
		}
		worker, err := fanout.New(ctx, strings.Split(*brokers, ","), "wrinkle-fanout", *fanoutN, userDB, pres, notifier, log.With("role", "fanout"))
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

func newLogger(debug bool) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)
	return log
}

type roleSet struct {
	gateway, fanout, services bool
}

func parseRoles(list string) (roleSet, error) {
	var want roleSet
	for _, role := range strings.Split(list, ",") {
		switch strings.TrimSpace(role) {
		case "gateway":
			want.gateway = true
		case "fanout":
			want.fanout = true
		case "services":
			want.services = true
		default:
			return want, fmt.Errorf("unknown role %q", role)
		}
	}
	return want, nil
}
