package shardwork

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
)

// fakeLeases holds leases for one instance; others take them by holding
// the name.
type fakeLeases struct {
	mu      sync.Mutex
	held    map[string]string
	me      string
	renewed int
}

func (f *fakeLeases) TakeLease(_ context.Context, name string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, taken := f.held[name]; taken {
		return false, nil
	}
	f.held[name] = f.me
	return true, nil
}

func (f *fakeLeases) RenewLease(_ context.Context, name string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewed++
	return f.held[name] == f.me, nil
}

func (f *fakeLeases) DropLease(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held[name] == f.me {
		delete(f.held, name)
	}
	return nil
}

func job(leases Leases, round func(context.Context, vtdb.Shard) (int, error)) Job {
	return Job{Name: "work", Keyspace: "ks", Interval: time.Hour, Round: round, Leases: leases, Log: slog.New(slog.DiscardHandler)}
}

func TestLeasedRoundRunsOnceAndDropsTheLease(t *testing.T) {
	leases := &fakeLeases{held: map[string]string{}, me: "a"}
	rounds := 0
	j := job(leases, func(context.Context, vtdb.Shard) (int, error) { rounds++; return 3, nil })
	n, err := j.leased(t.Context(), vtdb.Shard{Name: "-80"})
	if err != nil || n != 3 || rounds != 1 {
		t.Fatalf("round: %d %v, %d rounds", n, err, rounds)
	}
	if len(leases.held) != 0 {
		t.Fatalf("leases left: %v", leases.held)
	}
}

func TestShardLeasedElsewhereIsSkipped(t *testing.T) {
	leases := &fakeLeases{held: map[string]string{"work:ks/-80": "b"}, me: "a"}
	j := job(leases, func(_ context.Context, shard vtdb.Shard) (int, error) {
		if shard.Name == "-80" {
			t.Error("ran a shard another instance holds")
		}
		return 0, nil
	})
	if n, err := j.leased(t.Context(), vtdb.Shard{Name: "-80"}); n != 0 || err != nil {
		t.Fatalf("skipped round: %d %v", n, err)
	}
	if n, err := j.leased(t.Context(), vtdb.Shard{Name: "80-"}); n != 0 || err != nil || leases.held["work:ks/80-"] != "" {
		t.Fatalf("free shard: %d %v %v", n, err, leases.held)
	}
}

func TestLosingTheLeaseStopsTheRound(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a renewal")
	}
	leases := &fakeLeases{held: map[string]string{}, me: "a"}
	j := job(leases, func(ctx context.Context, shard vtdb.Shard) (int, error) {
		leases.mu.Lock()
		leases.held["work:ks/-"] = "b"
		leases.mu.Unlock()
		select {
		case <-ctx.Done():
			return 1, nil
		case <-time.After(leaseTTL):
			t.Error("round went on after its lease was lost")
			return 0, nil
		}
	})
	if n, err := j.leased(t.Context(), vtdb.Shard{Name: "-"}); n != 1 || err != nil {
		t.Fatalf("round: %d %v", n, err)
	}
	if leases.held["work:ks/-"] != "b" {
		t.Fatalf("dropped another instance's lease: %v", leases.held)
	}
}
