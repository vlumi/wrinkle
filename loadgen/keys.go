package main

import (
	"context"
	"fmt"
	"os"
	"sync"
)

// keyWorkers bounds concurrent key requests before the run.
const keyWorkers = 64

// signOutStale signs out the devices earlier runs left signed in, so the
// conversations' keys are wrapped only for this run's devices.
func signOutStale(ctx context.Context, devices []*device, stats *stats) {
	byUser := map[int][]*device{}
	for _, d := range devices {
		byUser[d.user] = append(byUser[d.user], d)
	}
	var signer []*device
	for _, user := range byUser {
		for _, d := range user {
			if d.client != nil {
				signer = append(signer, d)
				break
			}
		}
	}
	parallel(ctx, len(signer), func(i int) {
		d := signer[i]
		current := map[uint64]bool{}
		for _, other := range byUser[d.user] {
			current[other.deviceID] = true
		}
		cctx, cancel := context.WithTimeout(ctx, 4*callTimeout)
		defer cancel()
		list, err := d.client.ListDevices(cctx)
		if err == nil {
			for _, dev := range list {
				if !current[dev.GetDeviceId()] {
					if err = d.client.SignOutDevice(cctx, dev.GetDeviceId()); err != nil {
						break
					}
				}
			}
		}
		if err != nil {
			stats.connectErrors.Add(1)
			fmt.Fprintf(os.Stderr, "sign out earlier devices of %s: %v\n", d.userName, err)
		}
	})
}

// prepareKeys gives every conversation a key epoch before anything is
// measured: one device of each publishes it, then every device checks it
// and fetches its key, so sends and receipts in the window need no key
// requests.
func prepareKeys(ctx context.Context, devices []*device, stats *stats) {
	type task struct {
		d              *device
		conversationID []byte
	}
	var all, first []task
	seen := map[string]bool{}
	for _, d := range devices {
		for _, id := range [][]byte{d.conversationID, d.groupID} {
			if id == nil {
				continue
			}
			t := task{d, id}
			all = append(all, t)
			if !seen[string(id)] {
				seen[string(id)] = true
				first = append(first, t)
			}
		}
	}
	for _, tasks := range [][]task{first, all} {
		parallel(ctx, len(tasks), func(i int) {
			t := tasks[i]
			cctx, cancel := context.WithTimeout(ctx, callTimeout)
			defer cancel()
			if _, err := t.d.client.PrepareKeys(cctx, t.conversationID); err != nil {
				if stats.connectErrors.Add(1) <= 3 {
					fmt.Fprintf(os.Stderr, "keys of %s: %v\n", t.d.userName, err)
				}
			}
		})
	}
}

// parallel runs f for 0 to n-1 on keyWorkers goroutines.
func parallel(ctx context.Context, n int, f func(int)) {
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(n, keyWorkers) {
		wg.Go(func() {
			for i := range next {
				f(i)
			}
		})
	}
	for i := range n {
		select {
		case next <- i:
		case <-ctx.Done():
		}
	}
	close(next)
	wg.Wait()
}
