// Command loadgen simulates devices against a backend and reports latency,
// throughput and the backend's resource use. Devices belong to users, users
// are paired into conversations, and messages flow at a fixed rate; see
// docs/measuring.md for how runs are combined into results.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runID := fmt.Sprintf("%06x", rand.IntN(1<<24))
	users := cfg.connections / cfg.devicesPerUser
	stats := newStats()
	devices := make([]*device, 0, cfg.connections)
	for i := range cfg.connections {
		user := i / cfg.devicesPerUser
		devices = append(devices, &device{
			userName: fmt.Sprintf("load-%s-u%d", runID, user),
			peerName: fmt.Sprintf("load-%s-u%d", runID, user^1),
			stats:    stats,
			markRead: cfg.markRead,
		})
	}

	fmt.Fprintf(os.Stderr, "connecting %d devices (%d users) at %d/s\n", cfg.connections, users, cfg.ramp)
	start := time.Now()
	var wg sync.WaitGroup
	for i, d := range devices {
		if i > 0 {
			time.Sleep(time.Second / time.Duration(cfg.ramp))
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.connect(ctx, cfg.url); err != nil {
				stats.connectErrors.Add(1)
				fmt.Fprintf(os.Stderr, "connect %s: %v\n", d.userName, err)
			}
		}()
	}
	wg.Wait()
	fmt.Fprintf(os.Stderr, "connected in %s, %d failed\n", time.Since(start).Round(time.Millisecond), stats.connectErrors.Load())

	// Every device needs its peer to exist before opening the conversation.
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
		if d.client != nil && d.conversationID != 0 {
			ready = append(ready, d)
			go d.receive(ctx)
		}
	}
	if len(ready) == 0 {
		return fmt.Errorf("no device is ready")
	}

	fmt.Fprintf(os.Stderr, "warm-up %s, then measuring %s at %.0f msg/s\n", cfg.warmup, cfg.duration, cfg.rate)
	sendCtx, stopSending := context.WithCancel(ctx)
	var senders sync.WaitGroup
	if cfg.rate > 0 {
		n := min(len(ready), 64)
		for s := range n {
			var share []*device
			for i := s; i < len(ready); i += n {
				share = append(share, ready[i])
			}
			senders.Add(1)
			go func() {
				defer senders.Done()
				sendLoop(ctx, sendCtx, share, cfg.rate/float64(n))
			}()
		}
	}
	sleepCtx(ctx, cfg.warmup)
	before := scrape(cfg.metricsURL)
	stats.startWindow()
	sleepCtx(ctx, cfg.duration)
	stats.endWindow()
	after := scrape(cfg.metricsURL)
	stopSending()
	senders.Wait()

	// Let messages sent at the end of the window arrive.
	grace := time.Now().Add(10 * time.Second)
	for time.Now().Before(grace) && stats.received.Load() < stats.expectedReceipts(cfg.devicesPerUser) {
		sleepCtx(ctx, 200*time.Millisecond)
	}
	for _, d := range ready {
		d.client.Close()
	}

	r := stats.result(cfg, before, after)
	r.Time = time.Now().UTC().Format(time.RFC3339)
	r.Label = cfg.label
	r.Commit = cfg.commit
	fmt.Println(r.String())
	if cfg.out != "" {
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(cfg.out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// sendLoop sends on a fixed schedule, round-robin over its devices, without
// waiting for responses: an open loop, so overload shows up as latency.
// The schedule stops with scheduleCtx; sends in flight finish under ctx.
func sendLoop(ctx, scheduleCtx context.Context, devices []*device, rate float64) {
	interval := time.Duration(float64(time.Second) / rate)
	next := time.Now()
	var wg sync.WaitGroup
	defer wg.Wait()
	for i := 0; ; i++ {
		next = next.Add(interval)
		if d := time.Until(next); d > 0 {
			select {
			case <-scheduleCtx.Done():
				return
			case <-time.After(d):
			}
		} else if scheduleCtx.Err() != nil {
			return
		}
		dev := devices[i%len(devices)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			dev.send(ctx)
		}()
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

var seq atomic.Uint64
