package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
)

const callTimeout = 10 * time.Second

// sendSeq keeps client message ids unique among sends in the same
// nanosecond.
var sendSeq atomic.Uint64

type device struct {
	userName       string
	peerName       string
	stats          *stats
	markRead       bool
	client         *wrinkleclient.Client
	token          string
	userID         []byte
	conversationID []byte
}

// connect connects the device with the token it signed in with.
func (d *device) connect(ctx context.Context, url string) error {
	if d.token == "" {
		return fmt.Errorf("not signed in")
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	c, err := wrinkleclient.Dial(cctx, url)
	if err != nil {
		return err
	}
	if _, err := c.Connect(cctx, d.token); err != nil {
		c.Close()
		return err
	}
	d.client = c
	return nil
}

func (d *device) open(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	conv, err := d.client.OpenConversation(cctx, d.peerName)
	if err != nil {
		return err
	}
	d.conversationID = conv.GetConversationId()
	return nil
}

// send sends one message whose client id carries the send time, so any
// receiving device can compute the end-to-end latency.
func (d *device) send(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	now := time.Now()
	id := fmt.Sprintf("%d-%d", now.UnixNano(), sendSeq.Add(1))
	d.stats.recordSent(now)
	if _, err := d.client.SendMessage(cctx, d.conversationID, id, "load"); err != nil {
		if d.stats.sendErrors.Add(1) <= 3 {
			fmt.Fprintf(os.Stderr, "send from %s: %v\n", d.userName, err)
		}
		return
	}
	d.stats.recordSendRTT(now, time.Since(now))
}

// receive drains events, recording the latency of the peer's messages and
// acking at most once a second.
func (d *device) receive(ctx context.Context) {
	var cursor, acked uint64
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if cursor > acked {
				if err := d.client.Ack(ctx, cursor); err == nil {
					acked = cursor
				}
			}
		case ev, ok := <-d.client.Events:
			if !ok {
				return
			}
			cursor = ev.GetCursor()
			msg := ev.GetMessage().GetMessage()
			if msg == nil || bytes.Equal(msg.GetSenderUserId(), d.userID) {
				continue
			}
			if sentAt, ok := sentTime(msg.GetClientMessageId()); ok {
				d.stats.recordReceipt(sentAt, time.Since(sentAt))
			}
			if d.markRead {
				go func() {
					cctx, cancel := context.WithTimeout(ctx, callTimeout)
					defer cancel()
					if err := d.client.MarkRead(cctx, msg.GetConversationId(), msg.GetSeq()); err != nil {
						d.stats.sendErrors.Add(1)
					}
				}()
			}
		}
	}
}

func sentTime(clientMessageID string) (time.Time, bool) {
	nanos, _, _ := strings.Cut(clientMessageID, "-")
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, n), true
}
