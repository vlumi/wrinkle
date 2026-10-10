// Package presence tracks which device is connected to which gateway node,
// and carries pushes from fan-out workers to gateways, through Valkey.
package presence

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

const channelPrefix = "push:"

type Client struct {
	rdb  *redis.Client
	node string
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
	return &Client{rdb: rdb, node: node}, nil
}

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

func (c *Client) DeviceConnected(ctx context.Context, userID, deviceID uint64) error {
	return c.rdb.HSet(ctx, presenceKey(userID), strconv.FormatUint(deviceID, 10), c.node).Err()
}

func (c *Client) DeviceDisconnected(ctx context.Context, userID, deviceID uint64) error {
	return c.rdb.HDel(ctx, presenceKey(userID), strconv.FormatUint(deviceID, 10)).Err()
}

func presenceKey(userID uint64) string {
	return "presence:" + strconv.FormatUint(userID, 10)
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
