// Package presence tracks which device is connected to which gateway
// instance, and carries pushes from fan-out workers to gateways, through
// Valkey.
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

func channel(userID uint64) string {
	return channelPrefix + strconv.FormatUint(userID, 10)
}

// UserOf returns the user a push channel belongs to.
func UserOf(channel string) (uint64, bool) {
	id, err := strconv.ParseUint(strings.TrimPrefix(channel, channelPrefix), 10, 64)
	return id, err == nil
}

// Publish pushes a serialized event to every gateway holding one of the
// user's devices.
func (c *Client) Publish(ctx context.Context, userID uint64, payload []byte) error {
	return c.rdb.Publish(ctx, channel(userID), payload).Err()
}

// Subscribe returns a subscription that gateways add users to as devices
// connect.
func (c *Client) Subscribe(ctx context.Context) *Subscription {
	return &Subscription{ps: c.rdb.Subscribe(ctx)}
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

func (c *Client) DeviceConnected(ctx context.Context, userID, deviceID uint64) error {
	return c.rdb.HSet(ctx, presenceKey(userID), strconv.FormatUint(deviceID, 10), c.instance).Err()
}

func (c *Client) DeviceDisconnected(ctx context.Context, userID, deviceID uint64) error {
	return release.Run(ctx, c.rdb, []string{presenceKey(userID)}, strconv.FormatUint(deviceID, 10), c.instance).Err()
}

// ConnectedDevices returns the user's devices held by a live gateway
// instance. Entries of instances that are gone are removed on the way.
func (c *Client) ConnectedDevices(ctx context.Context, userID uint64) (map[uint64]bool, error) {
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

func presenceKey(userID uint64) string {
	return "presence:" + strconv.FormatUint(userID, 10)
}

func instanceKey(instance string) string {
	return "gateway:" + instance
}

type Subscription struct {
	ps *redis.PubSub
}

func (s *Subscription) Add(ctx context.Context, userID uint64) error {
	return s.ps.Subscribe(ctx, channel(userID))
}

func (s *Subscription) Remove(ctx context.Context, userID uint64) error {
	return s.ps.Unsubscribe(ctx, channel(userID))
}

// Messages delivers pushes; each carries the channel and the payload.
func (s *Subscription) Messages() <-chan *redis.Message {
	return s.ps.Channel(redis.WithChannelSize(4096))
}

func (s *Subscription) Close() error { return s.ps.Close() }
