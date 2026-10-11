package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/vlumi/wrinkle/backends/go/presence"
)

const (
	// subscribeBatch caps the channels in one SUBSCRIBE or UNSUBSCRIBE.
	subscribeBatch = 1000
	// pingAfter is how long the subscription may stay silent before it is
	// pinged; a ping unanswered for as long again counts as a failure.
	pingAfter = 15 * time.Second
	// The subscription is replaced after a failure with a pause doubling
	// from minResubscribe to maxResubscribe.
	minResubscribe = 100 * time.Millisecond
	maxResubscribe = 5 * time.Second
)

var errDetached = errors.New("detached while subscribing")

// registry tracks the connections of each user on this gateway and keeps
// the push subscription in step: a user's channel is subscribed while the
// user has a connection here. Attaching and detaching change the registry
// and queue the user under a short lock; one worker brings the
// subscription to match, so no lock is held across a Valkey round trip and
// interleaved attaches and detaches settle on the right state.
type registry struct {
	pres *presence.Client
	log  *slog.Logger
	wake chan struct{}

	mu     sync.Mutex
	users  map[uint64]*userState
	queue  []uint64
	sub    *presence.Subscription // nil while there is none
	closed bool
}

type userState struct {
	conns map[*conn]struct{}
	// subscribed is what was last requested for the user's channel on the
	// current subscription; pending counts requests not yet confirmed.
	subscribed bool
	pending    int
	queued     bool
	// ready is closed when the channel is confirmed subscribed, or when a
	// connection is removed, for waiters to look again.
	ready chan struct{}
}

func (st *userState) confirmed() bool { return st.subscribed && st.pending == 0 }

func newRegistry(pres *presence.Client, log *slog.Logger) *registry {
	return &registry{pres: pres, log: log, wake: make(chan struct{}, 1), users: map[uint64]*userState{}}
}

// add registers the connection and returns once its user's channel is
// subscribed, so no push after it is missed.
func (r *registry) add(ctx context.Context, c *conn) error {
	r.mu.Lock()
	st := r.users[c.userID]
	if st == nil {
		st = &userState{conns: map[*conn]struct{}{}}
		r.users[c.userID] = st
	}
	st.conns[c] = struct{}{}
	if !st.subscribed {
		r.enqueue(c.userID, st)
	}
	for !st.confirmed() {
		if st.ready == nil {
			st.ready = make(chan struct{})
		}
		ready := st.ready
		r.mu.Unlock()
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		}
		r.mu.Lock()
		st = r.users[c.userID]
		if st == nil {
			r.mu.Unlock()
			return errDetached
		}
		if _, ok := st.conns[c]; !ok {
			r.mu.Unlock()
			return errDetached
		}
	}
	r.mu.Unlock()
	return nil
}

// remove unregisters the connection; removing it twice is harmless.
func (r *registry) remove(c *conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.users[c.userID]
	if st == nil {
		return
	}
	if _, ok := st.conns[c]; !ok {
		return
	}
	delete(st.conns, c)
	if st.ready != nil {
		close(st.ready)
		st.ready = nil
	}
	if len(st.conns) > 0 {
		return
	}
	if st.subscribed {
		r.enqueue(c.userID, st)
	} else {
		r.forget(c.userID, st)
	}
}

func (r *registry) conns(userID uint64) []*conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.users[userID]
	if st == nil {
		return nil
	}
	list := make([]*conn, 0, len(st.conns))
	for c := range st.conns {
		list = append(list, c)
	}
	return list
}

// enqueue queues the user for the worker; the caller holds the lock.
func (r *registry) enqueue(userID uint64, st *userState) {
	if st.queued {
		return
	}
	st.queued = true
	r.queue = append(r.queue, userID)
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// forget drops a user with no connections once nothing is in flight for
// it; the caller holds the lock.
func (r *registry) forget(userID uint64, st *userState) {
	if len(st.conns) == 0 && !st.subscribed && st.pending == 0 && !st.queued {
		delete(r.users, userID)
	}
}

// reconcile is the worker: it subscribes and unsubscribes queued users
// until the subscription matches the registry.
func (r *registry) reconcile(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		}
		for {
			sub, add, remove := r.next()
			if len(add) == 0 && len(remove) == 0 {
				break
			}
			err := r.apply(ctx, sub, add, remove)
			if err != nil {
				if ctx.Err() == nil {
					r.log.Warn("push subscription request failed", "err", err)
				}
				// The receiving loop replaces it and subscribes everyone again.
				r.drop(sub)
				break
			}
		}
	}
}

// next takes a batch from the queue and records the requests about to be
// made for it.
func (r *registry) next() (sub *presence.Subscription, add, remove []uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sub == nil {
		return nil, nil, nil
	}
	n := min(len(r.queue), subscribeBatch)
	for _, userID := range r.queue[:n] {
		st := r.users[userID]
		if st == nil {
			continue
		}
		st.queued = false
		want := len(st.conns) > 0
		switch {
		case want == st.subscribed:
			r.forget(userID, st)
			continue
		case want:
			add = append(add, userID)
		default:
			remove = append(remove, userID)
		}
		st.subscribed = want
		st.pending++
	}
	r.queue = r.queue[n:]
	if len(r.queue) == 0 {
		r.queue = nil
	}
	return r.sub, add, remove
}

func (r *registry) apply(ctx context.Context, sub *presence.Subscription, add, remove []uint64) error {
	if len(add) > 0 {
		if err := sub.Add(ctx, add); err != nil {
			return err
		}
	}
	if len(remove) > 0 {
		return sub.Remove(ctx, remove)
	}
	return nil
}

// confirm counts a confirmation for the user's channel; confirmations come
// in the order of the requests.
func (r *registry) confirm(userID uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.users[userID]
	if st == nil || st.pending == 0 {
		return
	}
	st.pending--
	if st.pending > 0 {
		return
	}
	if st.subscribed {
		if st.ready != nil {
			close(st.ready)
			st.ready = nil
		}
		return
	}
	r.forget(userID, st)
}

// install makes the subscription current: nothing is subscribed on it yet,
// so every user with a connection is queued.
func (r *registry) install(sub *presence.Subscription) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.sub = sub
	r.queue = nil
	for userID, st := range r.users {
		st.subscribed, st.pending, st.queued = false, 0, false
		if len(st.conns) == 0 {
			delete(r.users, userID)
			continue
		}
		r.enqueue(userID, st)
	}
	return true
}

// drop closes the subscription if it is still current, which ends its
// receiving loop.
func (r *registry) drop(sub *presence.Subscription) {
	r.mu.Lock()
	current := r.sub == sub
	if current {
		r.sub = nil
	}
	r.mu.Unlock()
	if current {
		_ = sub.Close()
	}
}

// close ends the subscription for good.
func (r *registry) close() {
	r.mu.Lock()
	r.closed = true
	sub := r.sub
	r.sub = nil
	r.mu.Unlock()
	if sub != nil {
		_ = sub.Close()
	}
}

func (r *registry) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// run receives on a subscription, handing pushes to deliver, and replaces
// the subscription when it fails, until the context ends or the registry
// is closed.
func (r *registry) run(ctx context.Context, deliver func(userID uint64, payload []byte)) {
	pause := minResubscribe
	for {
		sub := r.pres.Subscribe()
		if !r.install(sub) {
			_ = sub.Close()
			return
		}
		heard, err := r.receive(ctx, sub, deliver)
		r.drop(sub)
		if ctx.Err() != nil || r.isClosed() {
			return
		}
		if heard {
			pause = minResubscribe
		}
		r.log.Warn("push subscription failed, resubscribing", "err", err, "after", pause)
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		pause = min(2*pause, maxResubscribe)
	}
}

// receive handles what arrives on the subscription until it fails, and
// reports whether anything arrived.
func (r *registry) receive(ctx context.Context, sub *presence.Subscription, deliver func(uint64, []byte)) (heard bool, err error) {
	pinged := false
	for {
		notice, err := sub.Receive(ctx, pingAfter)
		var netErr net.Error
		switch {
		case err == nil:
		case !pinged && errors.As(err, &netErr) && netErr.Timeout():
			pinged = true
			if err := sub.Ping(ctx); err != nil {
				return heard, err
			}
			continue
		default:
			return heard, err
		}
		heard, pinged = true, false
		switch notice.Kind {
		case presence.Pushed:
			deliver(notice.UserID, []byte(notice.Payload))
		case presence.Confirmed:
			r.confirm(notice.UserID)
		}
	}
}
