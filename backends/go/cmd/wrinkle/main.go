// Command wrinkle is the Go backend: one binary whose roles are chosen by
// configuration. The gateway role serves WebSocket connections, with the
// conversation and sync services in the same process unless it forwards to
// a services process; the services role serves them over the internal RPC;
// the fanout role consumes the outbox from Kafka, deletes unused blobs,
// trims the user logs and moves old history to the cold tier.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/vlumi/wrinkle/backends/go/accounts"
	"github.com/vlumi/wrinkle/backends/go/contacts"
	"github.com/vlumi/wrinkle/backends/go/conversation"
	"github.com/vlumi/wrinkle/backends/go/devicesync"
	"github.com/vlumi/wrinkle/backends/go/fanout"
	"github.com/vlumi/wrinkle/backends/go/gateway"
	"github.com/vlumi/wrinkle/backends/go/internal/blobs"
	"github.com/vlumi/wrinkle/backends/go/internal/shardwork"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	"github.com/vlumi/wrinkle/backends/go/presence"
	"github.com/vlumi/wrinkle/backends/go/push"
	"github.com/vlumi/wrinkle/backends/go/services"
	"github.com/vlumi/wrinkle/backends/go/stickers"
	"github.com/vlumi/wrinkle/backends/go/uploads"
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
		listen  = flag.String("listen", env("WRINKLE_LISTEN", "127.0.0.1:8080"), "HTTP listen address: /v1/ws (gateway role), /metrics, /healthz")
		mysql   = flag.String("mysql", env("WRINKLE_MYSQL", "127.0.0.1:15003"), "vtgate MySQL address")
		brokers = flag.String("kafka", env("WRINKLE_KAFKA", "localhost:19092"), "comma-separated Kafka brokers")
		valkey  = flag.String("valkey", env("WRINKLE_VALKEY", "localhost:6379"), "Valkey address")
		hbase   = flag.String("hbase", env("WRINKLE_HBASE", "hbase.localhost:2181"), "the cold tier's HBase, by its ZooKeeper quorum")
		svcAddr = flag.String("services", env("WRINKLE_SERVICES", ""), "gateway: forward requests to a services process at this address instead of running the services in-process")
		svcLsn  = flag.String("services-listen", env("WRINKLE_SERVICES_LISTEN", "127.0.0.1:9000"), "services role: gRPC listen address")
		node    = flag.String("node", env("WRINKLE_NODE", hostname), "this node's name in presence")
		dbConns = flag.Int("db-conns", envInt("WRINKLE_DB_CONNS", 64), "connections per keyspace")
		fanoutN = flag.Int("fanout-concurrency", envInt("WRINKLE_FANOUT_CONCURRENCY", 64), "fanout: conversations delivered at a time")
		ackMS   = flag.Int("ack-interval-ms", envInt("WRINKLE_ACK_INTERVAL_MS", 60000), "gateway: least time between two ack writes of a connection, in milliseconds")
		logKeep = flag.Int("log-retention-s", envInt("WRINKLE_LOG_RETENTION_S", 30*24*3600), "fanout: how long the user logs keep events, in seconds")
		trimS   = flag.Int("log-trim-interval-s", envInt("WRINKLE_LOG_TRIM_INTERVAL_S", 60), "fanout: time between two trims of each shard of the user logs, in seconds")
		hotS    = flag.Int("history-hot-s", envInt("WRINKLE_HISTORY_HOT_S", 30*24*3600), "fanout: how long messages stay in MySQL before moving to the cold tier, in seconds")
		moveS   = flag.Int("cold-move-interval-s", envInt("WRINKLE_COLD_MOVE_INTERVAL_S", 60), "fanout: time between two moves of each shard to the cold tier, in seconds")
		apnsKey = flag.String("apns-key", env("WRINKLE_APNS_KEY", ""), "fanout: APNs .p8 key file; push notifications are off without it")
		apnsKID = flag.String("apns-key-id", env("WRINKLE_APNS_KEY_ID", ""), "fanout: APNs key id; defaults to the id in the key's file name")
		apnsTID = flag.String("apns-team-id", env("WRINKLE_TEAM_ID", ""), "fanout: Apple developer team id")
		apnsTop = flag.String("apns-topic", env("WRINKLE_APNS_TOPIC", "fi.misaki.wrinkle"), "fanout: the iOS app's bundle id")
		blobURL = flag.String("blob-url", env("WRINKLE_BLOB_URL", "http://127.0.0.1:9200"), "the blob address, for checking and deleting blobs")
		blobKey = flag.String("blob-key-id", env("WRINKLE_BLOB_KEY_ID", ""), "blob storage access key id; make infra-up writes the local one to .env")
		blobSec = flag.String("blob-secret", env("WRINKLE_BLOB_SECRET", ""), "blob storage secret key")
		blobReg = flag.String("blob-region", env("WRINKLE_BLOB_REGION", "garage"), "blob storage region")
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
	var userDB, conversationDB *sql.DB
	var cold *conversation.Cold
	store := blobs.New(blobs.Config{URL: *blobURL, KeyID: *blobKey, Secret: *blobSec, Region: *blobReg})
	if want.services || want.fanout || (want.gateway && *svcAddr == "") {
		if *blobKey == "" || *blobSec == "" {
			return errors.New("no blob storage key: set WRINKLE_BLOB_KEY_ID and WRINKLE_BLOB_SECRET (make infra-up writes the local one to .env)")
		}
		conversationDB, err = vtdb.Open(ctx, *mysql, "conversation", *dbConns)
		if err != nil {
			return err
		}
		defer conversationDB.Close()
		userDB, err = vtdb.Open(ctx, *mysql, "user", *dbConns)
		if err != nil {
			return err
		}
		defer userDB.Close()
		cold = conversation.NewCold(*hbase)
		defer cold.Close()
		servicesLog := log.With("role", "services")
		accts := accounts.New(userDB, store, servicesLog)
		uploadService := uploads.New(userDB, store)
		contactService := contacts.New(userDB, accts)
		conversations := conversation.New(conversationDB, accts, accts, uploadService, contactService, cold)
		users := devicesync.New(userDB, accts, conversations, contactService)
		local = services.NewLocal(accts, conversations, users, uploadService, stickers.New(userDB, store), contactService, servicesLog)
	}
	// Valkey carries presence and pushes, from fan-out to gateways.
	var pres *presence.Client
	if want.gateway || want.fanout {
		pres, err = presence.New(ctx, *valkey, *node)
		if err != nil {
			return err
		}
		defer pres.Close()
	}

	// Roles run until the signal context ends, except the HTTP server, which
	// keeps answering (health with 503) until the gateway has drained.
	var started []role
	add := func(name string, run func() error) { started = append(started, role{name, run}) }
	httpCtx, stopHTTP := context.WithCancel(context.Background())
	defer stopHTTP()
	var draining atomic.Bool
	mux := newMux(&draining)
	if want.services {
		srv := services.NewServer(local)
		log.Info("services listening", "addr", *svcLsn)
		add("services", func() error { return srv.Serve(ctx, *svcLsn) })
	}
	var gw *gateway.Server
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
		gw, err = gateway.New(ctx, backend, pres, time.Duration(*ackMS)*time.Millisecond, log.With("role", "gateway"))
		if err != nil {
			return err
		}
		gw.Register(mux)
		add("gateway push", func() error { return gw.Run(ctx) })
	}
	add("http", func() error { return serveHTTP(httpCtx, *listen, mux, log) })
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
		worker, err := fanout.New(ctx, fanout.Config{
			Brokers: strings.Split(*brokers, ","), Group: "wrinkle-fanout", Concurrency: *fanoutN,
			DB: userDB, Conversations: conversationDB, UserKeyspace: "user", Publisher: pres, Notifier: notifier, Log: log.With("role", "fanout"),
		})
		if err != nil {
			return err
		}
		defer worker.Close()
		log.Info("fanout consuming", "topics", fanout.Topics)
		add("fanout", func() error { return worker.Run(ctx) })
		sweeper := uploads.NewSweeper(userDB, store, log.With("role", "sweeper"))
		add("upload sweeper", func() error { return sweeper.Run(ctx) })
		// Each shard's trim and move is taken by one instance at a time.
		trimmer, err := devicesync.NewTrimmer(ctx, *mysql, "user", userDB, time.Duration(*logKeep)*time.Second)
		if err != nil {
			return err
		}
		defer trimmer.Close()
		trims := shardwork.Job{
			Name: "trimming the user logs", Metric: "log_trim", Keyspace: "user", Shards: trimmer.Shards(), Interval: time.Duration(*trimS) * time.Second,
			Unit: "events", Round: trimmer.TrimShard, Leases: pres, Log: log.With("role", "trimmer"),
		}
		add("log trimmer", func() error { return trims.Run(ctx) })
		mover, err := conversation.NewMover(ctx, *mysql, "conversation", conversationDB, cold, time.Duration(*hotS)*time.Second)
		if err != nil {
			return err
		}
		defer mover.Close()
		moves := shardwork.Job{
			Name: "moving history to the cold tier", Metric: "history_move", Keyspace: "conversation", Shards: mover.Shards(), Interval: time.Duration(*moveS) * time.Second,
			Unit: "messages", Round: mover.MoveShard, Leases: pres, Log: log.With("role", "mover"),
		}
		add("cold mover", func() error { return moves.Run(ctx) })
	}

	// One slot per role, so every failing role can report without blocking.
	errc := make(chan error, len(started))
	var wg sync.WaitGroup
	for _, r := range started {
		wg.Go(func() {
			if err := r.run(); err != nil && ctx.Err() == nil {
				errc <- fmt.Errorf("%s: %w", r.name, err)
			}
		})
	}

	var runErr error
	select {
	case runErr = <-errc:
		stop()
	case <-ctx.Done():
	}
	log.Info("shutting down")
	draining.Store(true)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if gw != nil {
		gw.Drain(shutdownCtx)
	}
	stopHTTP()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		log.Warn("roles still running at the shutdown deadline")
	}
	return runErr
}

// shutdownTimeout bounds the whole shutdown: the gateway's drain, then the
// other roles.
const shutdownTimeout = 9500 * time.Millisecond

type role struct {
	name string
	run  func() error
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
