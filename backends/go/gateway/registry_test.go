package gateway

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/presence"
)

var user1 = ids.UUID{1}

// attach adds the connection in the background and returns its result.
func attach(r *registry, c *conn) <-chan error {
	done := make(chan error, 1)
	go func() { done <- r.add(context.Background(), c) }()
	return done
}

// waitQueued waits until the background attach has queued its user.
func waitQueued(t *testing.T, r *registry) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		r.mu.Lock()
		queued := len(r.queue)
		r.mu.Unlock()
		if queued == 1 {
			return
		}
	}
	t.Fatal("the user was not queued")
}

func wantNext(t *testing.T, r *registry, add, remove []ids.UUID) {
	t.Helper()
	_, gotAdd, gotRemove := r.next()
	if !slices.Equal(gotAdd, add) || !slices.Equal(gotRemove, remove) {
		t.Fatalf("got subscribe %v unsubscribe %v, want %v %v", gotAdd, gotRemove, add, remove)
	}
}

func wantAttached(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("attach did not return")
	}
}

// wantWaiting waits until the background attach is parked on the user's
// ready channel, then checks that it has not returned.
func wantWaiting(t *testing.T, r *registry, done <-chan error) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		r.mu.Lock()
		st := r.users[user1]
		parked := st != nil && st.ready != nil
		r.mu.Unlock()
		if !parked {
			continue
		}
		select {
		case err := <-done:
			t.Fatalf("attach returned before its channel was confirmed: %v", err)
		default:
		}
		return
	}
	t.Fatal("attach is not waiting for confirmation")
}

func TestRegistryWaitsForConfirmation(t *testing.T) {
	r := newRegistry(nil, slog.Default())
	r.install(&presence.Subscription{})
	a := &conn{userID: user1}
	done := attach(r, a)
	waitQueued(t, r)
	wantNext(t, r, []ids.UUID{user1}, nil)
	wantWaiting(t, r, done)
	r.confirm(user1)
	wantAttached(t, done)

	// A second connection of the user needs no request.
	wantAttached(t, attach(r, &conn{userID: user1}))
	wantNext(t, r, nil, nil)
}

func TestRegistryDetachRacingAttach(t *testing.T) {
	r := newRegistry(nil, slog.Default())
	r.install(&presence.Subscription{})
	a, b, c := &conn{userID: user1}, &conn{userID: user1}, &conn{userID: user1}
	done := attach(r, a)
	waitQueued(t, r)
	wantNext(t, r, []ids.UUID{user1}, nil)
	r.confirm(user1)
	wantAttached(t, done)

	// The last connection leaves and another arrives before the worker runs:
	// nothing to do.
	r.remove(a)
	wantAttached(t, attach(r, b))
	wantNext(t, r, nil, nil)

	// It leaves again, the unsubscribe goes out, and a connection arrives
	// before it is confirmed: it waits for the subscribe that follows.
	r.remove(b)
	wantNext(t, r, nil, []ids.UUID{user1})
	done = attach(r, c)
	waitQueued(t, r)
	r.confirm(user1)
	wantWaiting(t, r, done)
	wantNext(t, r, []ids.UUID{user1}, nil)
	r.confirm(user1)
	wantAttached(t, done)
}

func TestRegistryResubscribesOnNewSubscription(t *testing.T) {
	r := newRegistry(nil, slog.Default())
	r.install(&presence.Subscription{})
	done := attach(r, &conn{userID: user1})
	waitQueued(t, r)
	wantNext(t, r, []ids.UUID{user1}, nil)
	r.confirm(user1)
	wantAttached(t, done)

	r.install(&presence.Subscription{})
	wantNext(t, r, []ids.UUID{user1}, nil)
	done = attach(r, &conn{userID: user1})
	wantWaiting(t, r, done)
	r.confirm(user1)
	wantAttached(t, done)
}

func TestRegistryForgetsDetachedUsers(t *testing.T) {
	r := newRegistry(nil, slog.Default())
	r.install(&presence.Subscription{})
	a := &conn{userID: user1}
	done := attach(r, a)
	waitQueued(t, r)
	wantNext(t, r, []ids.UUID{user1}, nil)
	r.confirm(user1)
	wantAttached(t, done)
	r.remove(a)
	r.remove(a)
	wantNext(t, r, nil, []ids.UUID{user1})
	r.confirm(user1)
	if len(r.users) != 0 {
		t.Fatalf("users left: %v", r.users)
	}
}
