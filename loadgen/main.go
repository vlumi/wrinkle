// Command loadgen simulates devices against a backend and reports latency,
// throughput and the backend's resource use. Devices belong to users, users
// are paired into conversations and optionally put in groups, and messages
// flow at a fixed rate; see docs/measuring.md for how runs are combined
// into results. Users are lg-0, lg-1, ... with a fixed password: registered
// on first use, signed in to on later runs, before anything is measured.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
)

type config struct {
	url            string
	metricsURL     string
	connections    int
	devicesPerUser int
	rate           float64
	duration       time.Duration
	warmup         time.Duration
	ramp           int
	markRead       bool
	groupSize      int
	groupShare     float64
	attachShare    float64
	attachSize     int
	label          string
	commit         string
	out            string
}

func main() {
	var cfg config
	flag.StringVar(&cfg.url, "url", "ws://localhost:8080/v1/ws", "gateway URL")
	flag.StringVar(&cfg.metricsURL, "metrics", "http://localhost:8080/metrics", "backend metrics URL, empty to skip")
	flag.IntVar(&cfg.connections, "connections", 100, "simulated devices")
	flag.IntVar(&cfg.devicesPerUser, "devices-per-user", 1, "devices per user")
	flag.Float64Var(&cfg.rate, "rate", 10, "messages per second across all devices; 0 holds idle connections")
	flag.DurationVar(&cfg.duration, "duration", 60*time.Second, "measurement window after warm-up")
	flag.DurationVar(&cfg.warmup, "warmup", 10*time.Second, "warm-up before measuring")
	flag.IntVar(&cfg.ramp, "ramp", 200, "connections opened per second")
	flag.BoolVar(&cfg.markRead, "mark-read", false, "receivers mark every message read")
	flag.IntVar(&cfg.groupSize, "group-size", 8, "users per group, with --group-share")
	flag.Float64Var(&cfg.groupShare, "group-share", 0, "share of sends that go to the sender's group, 0 to 1; 0 forms no groups")
	flag.Float64Var(&cfg.attachShare, "attachment-share", 0, "share of sends that carry an attachment, 0 to 1")
	flag.IntVar(&cfg.attachSize, "attachment-size", 50<<10, "attachment size in bytes, with --attachment-share")
	flag.StringVar(&cfg.label, "label", "", "free text recorded with the result, such as the backend")
	flag.StringVar(&cfg.commit, "commit", "", "commit recorded with the result")
	flag.StringVar(&cfg.out, "out", "", "append the result as a JSON line to this file")
	flag.Parse()
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

func run(cfg config) error {
	if cfg.connections%cfg.devicesPerUser != 0 || (cfg.connections/cfg.devicesPerUser)%2 != 0 {
		return fmt.Errorf("connections must be a multiple of devices-per-user giving an even number of users")
	}
	if cfg.groupShare < 0 || cfg.groupShare > 1 {
		return fmt.Errorf("group-share must be between 0 and 1")
	}
	if cfg.groupShare > 0 && (cfg.groupSize < 2 || cfg.groupSize > 256 || cfg.groupSize > cfg.connections/cfg.devicesPerUser) {
		return fmt.Errorf("group-size must be 2 to 256 and at most the number of users")
	}
	if cfg.attachShare < 0 || cfg.attachShare > 1 {
		return fmt.Errorf("attachment-share must be between 0 and 1")
	}
	if cfg.attachShare > 0 && (cfg.attachSize < 1 || cfg.attachSize > maxAttachmentSize) {
		return fmt.Errorf("attachment-size must be 1 to %d bytes", maxAttachmentSize)
	}
	blobAddress, err := wrinkleclient.BlobAddress(cfg.url)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	stats := &stats{}
	devices := newDevices(cfg, blobAddress, stats)
	fmt.Fprintf(os.Stderr, "signing in %d devices (%d users)\n", cfg.connections, cfg.connections/cfg.devicesPerUser)
	start := time.Now()
	signInAll(ctx, devices, cfg.url, cfg.devicesPerUser, stats)
	fmt.Fprintf(os.Stderr, "signed in in %s, %d failed\n", time.Since(start).Round(time.Millisecond), stats.connectErrors.Load())
	fmt.Fprintf(os.Stderr, "connecting %d devices at %d/s\n", cfg.connections, cfg.ramp)
	start = time.Now()
	connectAll(ctx, devices, cfg.url, cfg.ramp, stats)
	fmt.Fprintf(os.Stderr, "connected in %s, %d failed\n", time.Since(start).Round(time.Millisecond), stats.connectErrors.Load())
	signOutStale(ctx, devices, stats)
	ready := openAll(ctx, devices, stats)
	if len(ready) == 0 {
		return fmt.Errorf("no device is ready")
	}
	if cfg.groupShare > 0 {
		start = time.Now()
		formGroups(ctx, ready, cfg.groupSize, cfg.devicesPerUser, stats)
		fmt.Fprintf(os.Stderr, "formed groups of %d in %s\n", cfg.groupSize, time.Since(start).Round(time.Millisecond))
	}
	start = time.Now()
	prepareKeys(ctx, ready, stats)
	fmt.Fprintf(os.Stderr, "published conversation keys in %s, %d failed\n", time.Since(start).Round(time.Millisecond), stats.connectErrors.Load())
	for _, d := range ready {
		go d.receive(ctx)
	}

	fmt.Fprintf(os.Stderr, "warm-up %s, then measuring %s at %.0f msg/s\n", cfg.warmup, cfg.duration, cfg.rate)
	sendCtx, stopSending := context.WithCancel(ctx)
	senders := startSenders(ctx, sendCtx, ready, cfg.rate)
	sleepCtx(ctx, cfg.warmup)
	before := scrape(cfg.metricsURL)
	stats.startWindow()
	sleepCtx(ctx, cfg.duration)
	stats.endWindow()
	after := scrape(cfg.metricsURL)
	stopSending()
	senders.Wait()

	waitForReceipts(ctx, stats, 10*time.Second)
	if cfg.attachShare > 0 {
		deleteAttached(ctx, ready)
	}
	for _, d := range ready {
		d.client.Close()
	}

	r := stats.result(cfg, before, after)
	fmt.Println(r.String())
	if cfg.out != "" {
		return appendResult(cfg.out, r)
	}
	return nil
}

// newDevices makes the devices of users paired into conversations: user
// 2n talks to user 2n+1.
func newDevices(cfg config, blobAddress string, stats *stats) []*device {
	devices := make([]*device, 0, cfg.connections)
	for i := range cfg.connections {
		user := i / cfg.devicesPerUser
		devices = append(devices, &device{
			user:            user,
			userName:        fmt.Sprintf("lg-%d", user),
			peerName:        fmt.Sprintf("lg-%d", user^1),
			stats:           stats,
			markRead:        cfg.markRead,
			directReceivers: int64(cfg.devicesPerUser),
			groupShare:      cfg.groupShare,
			attachShare:     cfg.attachShare,
			attachSize:      cfg.attachSize,
			blobAddress:     blobAddress,
		})
	}
	return devices
}

// connectAll connects the signed-in devices, ramp per second, and waits
// for all of them; a device that fails is left without a client.
func connectAll(ctx context.Context, devices []*device, url string, ramp int, stats *stats) {
	var wg sync.WaitGroup
	for i, d := range devices {
		if d.token == "" {
			continue
		}
		if i > 0 {
			time.Sleep(time.Second / time.Duration(ramp))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.connect(ctx, url); err != nil {
				stats.connectErrors.Add(1)
				fmt.Fprintf(os.Stderr, "connect %s: %v\n", d.userName, err)
			}
		}()
	}
	wg.Wait()
}

// openAll opens every connected device's conversation, which needs every
// peer connected first, and returns the devices that are ready.
func openAll(ctx context.Context, devices []*device, stats *stats) []*device {
	var wg sync.WaitGroup
	for _, d := range devices {
		if d.client == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.open(ctx); err != nil {
				stats.connectErrors.Add(1)
				fmt.Fprintf(os.Stderr, "open %s: %v\n", d.userName, err)
			}
		}()
	}
	wg.Wait()
	var ready []*device
	for _, d := range devices {
		if d.client != nil && d.conversationID != nil {
			ready = append(ready, d)
		}
	}
	return ready
}

// waitForReceipts lets messages sent at the end of the window arrive, and
// their attachments download.
func waitForReceipts(ctx context.Context, stats *stats, grace time.Duration) {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) && (stats.received.Load() < stats.expectedReceipts() || stats.downloading.Load() > 0) {
		sleepCtx(ctx, 200*time.Millisecond)
	}
}

// deleteAttached deletes the run's messages with attachments, after the
// window, so blob storage does not grow with every run.
func deleteAttached(ctx context.Context, devices []*device) {
	start := time.Now()
	var deleted, failed atomic.Int64
	var wg sync.WaitGroup
	for _, d := range devices {
		wg.Go(func() {
			n, f := d.deleteAttached(ctx)
			deleted.Add(int64(n))
			failed.Add(int64(f))
		})
	}
	wg.Wait()
	fmt.Fprintf(os.Stderr, "deleted %d messages with attachments in %s, %d failed\n",
		deleted.Load(), time.Since(start).Round(time.Millisecond), failed.Load())
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
