package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// historyPage is the page size of the history reads, as the clients ask.
const historyPage = 50

// filled is a conversation's history: seqs from lo to hi were moved to the
// cold tier, and the hot ones follow up to top.
type filled struct {
	device      *device
	lo, hi, top uint64
}

// historyStats are the history reads of the window, by where the page lies.
type historyStats struct {
	mu                sync.Mutex
	hot, cold, across []time.Duration
	errors            int64
	moved             float64
	moveSeconds       float64
}

// runHistory measures history reads across the watermark. One device of
// each conversation sends perConversation messages, as fast as they are
// answered; once the backend has moved them to the cold tier, it sends a
// page of hot ones after them. Then devices read pages at the rate for the
// warm-up and the window: the newest page, all hot; a page at random among
// the cold messages; and the page across the watermark, half of each.
// The backend's hot window must outlast the hot messages' sending and the
// reads, and its mover's interval be short; with metrics, the wait ends
// when the mover reports the messages moved, without, after hotWindow.
func runHistory(ctx context.Context, cfg config, devices []*device, stats *stats) (*historyStats, error) {
	var senders []*device
	seen := map[string]bool{}
	for _, d := range devices {
		if key := string(d.conversationID); !seen[key] {
			seen[key] = true
			senders = append(senders, d)
		}
	}
	metrics := &http.Client{Timeout: 10 * time.Second}
	if moved, took := settle(ctx, metrics, cfg.moverMetrics); moved > 0 {
		fmt.Fprintf(os.Stderr, "the mover first moved %.0f older messages, in %s\n", moved, took.Round(time.Second))
	}

	before := read(metrics, cfg.moverMetrics)
	start := time.Now()
	convs, err := fill(ctx, senders, cfg.history)
	if err != nil {
		return nil, err
	}
	filledAt := time.Now()
	total := cfg.history * len(convs)
	fmt.Fprintf(os.Stderr, "sent %d messages to %d conversations in %s; waiting for the cold tier\n",
		total, len(convs), time.Since(start).Round(time.Millisecond))
	h := &historyStats{}
	if before.mover {
		deadline := time.Now().Add(cfg.hotWindow + 5*time.Minute)
		for {
			now := read(metrics, cfg.moverMetrics)
			if now.moved-before.moved >= float64(total) {
				h.moved, h.moveSeconds = now.moved-before.moved, now.moveSeconds-before.moveSeconds
				break
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("the mover moved %.0f of %d messages", now.moved-before.moved, total)
			}
			sleepCtx(ctx, time.Second)
		}
	} else {
		sleepCtx(ctx, time.Until(filledAt.Add(cfg.hotWindow+15*time.Second)))
	}
	fmt.Fprintf(os.Stderr, "moved after %s\n", time.Since(start).Round(time.Second))
	hot, err := fill(ctx, senders, historyPage)
	if err != nil {
		return nil, err
	}
	for i := range convs {
		convs[i].top = hot[i].hi
	}

	readCtx, stopReading := context.WithCancel(ctx)
	var readers sync.WaitGroup
	n := min(len(convs), 64)
	for r := range n {
		readers.Go(func() { h.readLoop(ctx, readCtx, convs[r:], n, cfg.rate/float64(n), stats) })
	}
	sleepCtx(ctx, cfg.warmup)
	stats.startWindow()
	sleepCtx(ctx, cfg.duration)
	stats.endWindow()
	stopReading()
	readers.Wait()
	return h, nil
}

// settle waits until the mover has moved what earlier runs left older
// than the hot window, so the fill is all it moves: until every shard has
// finished two rounds since the last that moved anything. It returns what
// was moved meanwhile and how long it took.
func settle(ctx context.Context, metrics *http.Client, metricsURL string) (float64, time.Duration) {
	start := time.Now()
	first := read(metrics, metricsURL)
	if !first.mover {
		return 0, 0
	}
	since := first
	for ctx.Err() == nil {
		sleepCtx(ctx, time.Second)
		now := read(metrics, metricsURL)
		if !now.ok {
			continue
		}
		if now.moved != since.moved {
			since = now
			continue
		}
		settled := len(now.moveRounds) > 0
		for shard, n := range now.moveRounds {
			settled = settled && n >= since.moveRounds[shard]+2
		}
		if settled {
			return now.moved - first.moved, time.Since(start)
		}
	}
	return 0, 0
}

// fill sends n messages from each device to its direct conversation and
// returns each conversation's range of seqs.
func fill(ctx context.Context, senders []*device, n int) ([]filled, error) {
	convs := make([]filled, len(senders))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed error
	for i, d := range senders {
		wg.Go(func() {
			content := &pb.Content{Body: &pb.Content_Text{Text: "history"}}
			for j := range n {
				cctx, cancel := context.WithTimeout(ctx, callTimeout)
				id := fmt.Sprintf("h%d-%d", time.Now().UnixNano(), sendSeq.Add(1))
				msg, err := d.client.SendContent(cctx, d.conversationID, id, content)
				cancel()
				if err != nil {
					mu.Lock()
					failed = fmt.Errorf("history from %s: %w", d.userName, err)
					mu.Unlock()
					return
				}
				if j == 0 {
					convs[i].lo = msg.GetSeq()
				}
				convs[i].hi = msg.GetSeq()
			}
			convs[i].device = d
		})
	}
	wg.Wait()
	return convs, failed
}

// readLoop reads pages on a fixed schedule from every step-th conversation
// from the first, each read waiting for its answer.
func (h *historyStats) readLoop(ctx, scheduleCtx context.Context, convs []filled, step int, rate float64, stats *stats) {
	interval := time.Duration(float64(time.Second) / rate)
	next := time.Now()
	var wg sync.WaitGroup
	defer wg.Wait()
	for i := 0; ; i += step {
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
		c := convs[i%len(convs)]
		wg.Go(func() { h.read(ctx, c, stats) })
	}
}

// read reads one page, chosen at random among the three kinds.
func (h *historyStats) read(ctx context.Context, c filled, stats *stats) {
	var before uint64
	var into *[]time.Duration
	switch rand.IntN(3) {
	case 0:
		before, into = 0, &h.hot
	case 1:
		before, into = c.lo+historyPage+rand.Uint64N(c.hi-c.lo-historyPage+2), &h.cold
	default:
		before, into = c.hi+historyPage/2+1, &h.across
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	at := time.Now()
	msgs, err := c.device.client.GetMessages(cctx, c.device.conversationID, before, historyPage)
	took := time.Since(at)
	if !stats.inWindow(at) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil || len(msgs) != historyPage {
		h.errors++
		if h.errors <= 3 {
			if err == nil {
				err = fmt.Errorf("%d messages of %d", len(msgs), historyPage)
			}
			fmt.Fprintf(os.Stderr, "history before %d from %s: %v\n", before, c.device.userName, err)
		}
		return
	}
	*into = append(*into, took)
}

// summary is the reads' latencies, sorted, by kind.
func (h *historyStats) summary() (hot, cold, across []time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range [][]time.Duration{h.hot, h.cold, h.across} {
		slices.Sort(l)
	}
	return h.hot, h.cold, h.across
}
