// Package presence tracks which device is connected to which gateway
// instance, carries pushes from fan-out workers to gateways, and holds the
// leases that share background work among instances, through Valkey.
package presence

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
)

const (
	channelPrefix = "push:"
	// A gateway refreshes its liveness key every HeartbeatInterval; the key
	// expires after instanceTTL without one.
	HeartbeatInterval = 30 * time.Second
	instanceTTL       = 90 * time.Second
)

// release deletes a device's presence field only if it still names the
// given instance, so a device that reconnected elsewhere keeps its entry.
var release = redis.NewScript(`if redis.call('HGET', KEYS[1], ARGV[1]) == ARGV[2] then return redis.call('HDEL', KEYS[1], ARGV[1]) end return 0`)

// renewLease and dropLease change a lease only while this instance holds it.
var (
	renewLease = redis.NewScript(`if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('PEXPIRE', KEYS[1], ARGV[2]) end return 0`)
	dropLease  = redis.NewScript(`if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end return 0`)
)

type Client struct {
	rdb *redis.Client
	// instance identifies this process in presence: the node name and a
	// random suffix, so a restarted gateway's stale entries are told apart.
	instance string
}

func New(ctx context.Context, addr, node string) (*Client, error) {
	// The defaults (10 connections per CPU, 32 KiB buffers each) are a fixed
	// cost of tens of megabytes that would be read as per-connection memory.
	rdb := redis.NewClient(&redis.Options{
		Addr:            addr,
		PoolSize:        32,
		ReadBufferSize:  4 * 1024,
		WriteBufferSize: 4 * 1024,
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("valkey at %s: %w", addr, err)
	}
	suffix := make([]byte, 8)
	_, _ = rand.Read(suffix)
	return &Client{rdb: rdb, instance: node + "/" + hex.EncodeToString(suffix)}, nil
}

func (c *Client) Instance() string { return c.instance }

func (c *Client) Close() error { return c.rdb.Close() }

// Keys and channels name a user by the lowercase hex of the id.
func channel(userID ids.UUID) string {
	return channelPrefix + userID.String()
}

// userOf returns the user a push channel belongs to.
func userOf(channel string) (ids.UUID, bool) {
	suffix, ok := strings.CutPrefix(channel, channelPrefix)
	if !ok {
		return ids.UUID{}, false
	}
	return ids.FromHex(suffix)
}

// Publish pushes a serialized event to every gateway holding one of the
// user's devices.
func (c *Client) Publish(ctx context.Context, userID ids.UUID, payload []byte) error {
	return c.rdb.Publish(ctx, channel(userID), payload).Err()
}

// Subscribe opens a pub/sub connection for a gateway to subscribe its
// users' channels on. It connects on first use.
func (c *Client) Subscribe() *Subscription {
	return &Subscription{ps: c.rdb.Subscribe(context.Background())}
}

// Announce marks this instance alive; a gateway calls it at startup and
// every HeartbeatInterval.
func (c *Client) Announce(ctx context.Context) error {
	return c.rdb.Set(ctx, instanceKey(c.instance), 1, instanceTTL).Err()
}

// Withdraw marks this instance gone, when its gateway drains.
func (c *Client) Withdraw(ctx context.Context) error {
	return c.rdb.Del(ctx, instanceKey(c.instance)).Err()
}

func (c *Client) DeviceConnected(ctx context.Context, userID ids.UUID, deviceID uint64) error {
	return c.rdb.HSet(ctx, presenceKey(userID), strconv.FormatUint(deviceID, 10), c.instance).Err()
}

func (c *Client) DeviceDisconnected(ctx context.Context, userID ids.UUID, deviceID uint64) error {
	return release.Run(ctx, c.rdb, []string{presenceKey(userID)}, strconv.FormatUint(deviceID, 10), c.instance).Err()
}

// ConnectedDevices returns the user's devices held by a live gateway
// instance. Entries of instances that are gone are removed on the way.
func (c *Client) ConnectedDevices(ctx context.Context, userID ids.UUID) (map[uint64]bool, error) {
	key := presenceKey(userID)
	entries, err := c.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	alive, err := c.alive(ctx, entries)
	if err != nil {
		return nil, err
	}
	out := make(map[uint64]bool, len(entries))
	for field, instance := range entries {
		if !alive[instance] {
			_ = release.Run(ctx, c.rdb, []string{key}, field, instance).Err()
			continue
		}
		if id, err := strconv.ParseUint(field, 10, 64); err == nil {
			out[id] = true
		}
	}
	return out, nil
}

// alive tells which of the entries' instances have a liveness key.
func (c *Client) alive(ctx context.Context, entries map[string]string) (map[string]bool, error) {
	checks := map[string]*redis.IntCmd{}
	pipe := c.rdb.Pipeline()
	for _, instance := range entries {
		if _, ok := checks[instance]; !ok {
			checks[instance] = pipe.Exists(ctx, instanceKey(instance))
		}
	}
	if len(checks) == 0 {
		return nil, nil
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	alive := make(map[string]bool, len(checks))
	for instance, cmd := range checks {
		alive[instance] = cmd.Val() > 0
	}
	return alive, nil
}

// TakeLease takes the named lease for this instance for ttl, unless another
// instance holds it.
func (c *Client) TakeLease(ctx context.Context, name string, ttl time.Duration) (bool, error) {
	return c.rdb.SetNX(ctx, leaseKey(name), c.instance, ttl).Result()
}

// RenewLease extends a lease this instance holds, and reports whether it
// still did.
func (c *Client) RenewLease(ctx context.Context, name string, ttl time.Duration) (bool, error) {
	n, err := renewLease.Run(ctx, c.rdb, []string{leaseKey(name)}, c.instance, ttl.Milliseconds()).Int()
	return n == 1, err
}

// DropLease gives up a lease this instance holds.
func (c *Client) DropLease(ctx context.Context, name string) error {
	return dropLease.Run(ctx, c.rdb, []string{leaseKey(name)}, c.instance).Err()
}

func leaseKey(name string) string {
	return "lease:" + name
}

func presenceKey(userID ids.UUID) string {
	return "presence:" + userID.String()
}

func instanceKey(instance string) string {
	return "gateway:" + instance
}

// Subscription is one pub/sub connection. A gateway replaces it, rather than
// let it reconnect by itself, when it fails, and subscribes its channels
// again on the new one.
type Subscription struct {
	ps *redis.PubSub
}

// NoticeKind tells what arrived on a subscription.
type NoticeKind int

const (
	// Other is anything else: a pong, or a notice for a channel that is
	// not a user's.
	Other NoticeKind = iota
	// Pushed is an event for a user.
	Pushed
	// Confirmed answers one channel of a Subscribe or Unsubscribe call.
	Confirmed
)

type Notice struct {
	Kind    NoticeKind
	UserID  ids.UUID
	Payload string
}

// Add subscribes the users' channels. It returns once the request is
// written; Receive confirms each channel.
func (s *Subscription) Add(ctx context.Context, userIDs []ids.UUID) error {
	return s.ps.Subscribe(ctx, channels(userIDs)...)
}

// Remove unsubscribes the users' channels; Receive confirms each.
func (s *Subscription) Remove(ctx context.Context, userIDs []ids.UUID) error {
	return s.ps.Unsubscribe(ctx, channels(userIDs)...)
}

// Ping asks for a pong, which Receive returns as Other.
func (s *Subscription) Ping(ctx context.Context) error {
	return s.ps.Ping(ctx)
}

// Receive waits up to the timeout for what arrives next. Only one goroutine
// may receive.
func (s *Subscription) Receive(ctx context.Context, timeout time.Duration) (Notice, error) {
	msg, err := s.ps.ReceiveTimeout(ctx, timeout)
	if err != nil {
		return Notice{}, err
	}
	switch msg := msg.(type) {
	case *redis.Message:
		if userID, ok := userOf(msg.Channel); ok {
			return Notice{Kind: Pushed, UserID: userID, Payload: msg.Payload}, nil
		}
	case *redis.Subscription:
		if msg.Kind == "subscribe" || msg.Kind == "unsubscribe" {
			if userID, ok := userOf(msg.Channel); ok {
				return Notice{Kind: Confirmed, UserID: userID}, nil
			}
		}
	}
	return Notice{Kind: Other}, nil
}

// Close ends the subscription; one never opened, as tests make, has nothing
// to close.
func (s *Subscription) Close() error {
	if s.ps == nil {
		return nil
	}
	return s.ps.Close()
}

func channels(userIDs []ids.UUID) []string {
	list := make([]string, len(userIDs))
	for i, id := range userIDs {
		list[i] = channel(id)
	}
	return list
}
