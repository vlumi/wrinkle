package main

import (
	"context"
	"sync"
	"time"
)

// startSenders spreads the rate over up to 64 senders, each with its share
// of the devices. Wait on the result after ending scheduleCtx.
func startSenders(ctx, scheduleCtx context.Context, devices []*device, rate float64) *sync.WaitGroup {
	var senders sync.WaitGroup
	if rate <= 0 {
		return &senders
	}
	n := min(len(devices), 64)
	for s := range n {
		var share []*device
		for i := s; i < len(devices); i += n {
			share = append(share, devices[i])
		}
		senders.Add(1)
		go func() {
			defer senders.Done()
			sendLoop(ctx, scheduleCtx, share, rate/float64(n))
		}()
	}
	return &senders
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
