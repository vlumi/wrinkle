package conformance

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
)

// TestMain waits until the backend delivers events end to end, so a backend
// that is still joining its consumer group does not fail the first tests.
// The probe that succeeds is kept for the retention test.
func TestMain(m *testing.M) {
	if err := waitForDelivery(90 * time.Second); err != nil {
		fmt.Fprintln(os.Stderr, "backend not ready:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func waitForDelivery(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if last = probe(); last == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return last
}

// probed is the receiving device of the probe that succeeded: its token,
// the conversation and the cursor before the probe's message, which the
// log's retention expires while the other tests run.
var probed struct {
	token          string
	conversationID []byte
	cursor         uint64
}

// probe sends a message between two new users and waits for its event.
func probe() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	a, _, err := probeUser(ctx, "probe-a-"+suffix)
	if err != nil {
		return err
	}
	defer a.Close()
	b, token, err := probeUser(ctx, "probe-b-"+suffix)
	if err != nil {
		return err
	}
	defer b.Close()
	conv, err := a.OpenConversation(ctx, "probe-b-"+suffix)
	if err != nil {
		return err
	}
	if _, err := a.SendText(ctx, conv.GetConversationId(), "probe", "probe"); err != nil {
		return err
	}
	for {
		select {
		case ev, ok := <-b.Events:
			if !ok {
				return fmt.Errorf("connection closed: %w", b.Err())
			}
			if isMessage(ev) {
				probed.token, probed.conversationID, probed.cursor = token, conv.GetConversationId(), ev.GetCursor()-1
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("no event delivered: %w", ctx.Err())
		}
	}
}

// probeUser registers and connects a user and returns its device token.
func probeUser(ctx context.Context, name string) (*wrinkleclient.Client, string, error) {
	c, err := wrinkleclient.Dial(ctx, gatewayURL())
	if err != nil {
		return nil, "", err
	}
	creds, err := c.Register(ctx, name, password, "probe")
	if err == nil {
		_, err = c.Connect(ctx, creds.GetDeviceToken())
	}
	if err == nil {
		_, err = c.EnableKeys(ctx, creds.GetUserId(), creds.GetDeviceId())
	}
	if err != nil {
		c.Close()
		return nil, "", err
	}
	return c, creds.GetDeviceToken(), nil
}
