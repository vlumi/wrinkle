// Package shardwork runs background work over every shard of a keyspace,
// each shard on its own goroutine, and shares the shards among the
// instances that run it: a shard's round needs its lease, so one instance
// works it at a time and the others take the rest.
package shardwork

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
)

const (
	// leaseTTL bounds how long a stopped instance keeps its shards; a
	// round renews its lease at a third of it.
	leaseTTL     = 30 * time.Second
	dropDeadline = 5 * time.Second
)

// Leases are held by name, for this instance.
type Leases interface {
	TakeLease(ctx context.Context, name string, ttl time.Duration) (bool, error)
	RenewLease(ctx context.Context, name string, ttl time.Duration) (bool, error)
	DropLease(ctx context.Context, name string) error
}

// Job is one kind of work: Round does what is due on a shard and returns
// how much it did, in Unit.
type Job struct {
	Name     string
	Keyspace string
	Shards   []vtdb.Shard
	Interval time.Duration
	Unit     string
	Round    func(ctx context.Context, shard vtdb.Shard) (int, error)
	Leases   Leases
	Log      *slog.Logger
}

// Run starts every shard's loop and waits for them until the context ends.
// A shard's first round is after a minute, or the interval when shorter.
func (j Job) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, shard := range j.Shards {
		wg.Go(func() { j.loop(ctx, shard) })
	}
	wg.Wait()
	return nil
}

func (j Job) loop(ctx context.Context, shard vtdb.Shard) {
	log := j.Log.With("shard", shard.Name)
	timer := time.NewTimer(min(time.Minute, j.Interval))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if n, err := j.leased(ctx, shard); err != nil && ctx.Err() == nil {
			log.Warn(j.Name+" failed", "err", err)
		} else if n > 0 {
			log.Info(j.Name, j.Unit, n)
		}
		timer.Reset(j.Interval)
	}
}

// leased runs a round under the shard's lease, if this instance gets it.
// Losing the lease midway stops the round; whoever took it goes on.
func (j Job) leased(ctx context.Context, shard vtdb.Shard) (int, error) {
	name := j.Name + ":" + j.Keyspace + "/" + shard.Name
	ok, err := j.Leases.TakeLease(ctx, name, leaseTTL)
	if err != nil || !ok {
		return 0, err
	}
	roundCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		ticker := time.NewTicker(leaseTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-roundCtx.Done():
				return
			case <-ticker.C:
			}
			if held, err := j.Leases.RenewLease(roundCtx, name, leaseTTL); err == nil && !held {
				cancel()
				return
			}
		}
	}()
	n, err := j.Round(roundCtx, shard)
	cancel()
	<-renewed
	dropCtx, done := context.WithTimeout(context.WithoutCancel(ctx), dropDeadline)
	defer done()
	_ = j.Leases.DropLease(dropCtx, name)
	return n, err
}
