package conformance

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// TestMain waits until the backend delivers events end to end, so a backend
// that is still joining its consumer group does not fail the first tests.
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
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		if last = probe(); last == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return last
}

func probe() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	a, err := wrinkleclient.Dial(ctx, gatewayURL())
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := wrinkleclient.Dial(ctx, gatewayURL())
	if err != nil {
		return err
	}
	defer b.Close()
	if _, err := a.Connect(ctx, &pb.ConnectRequest{UserName: "probe-a-" + suffix}); err != nil {
		return err
	}
	if _, err := b.Connect(ctx, &pb.ConnectRequest{UserName: "probe-b-" + suffix}); err != nil {
		return err
	}
	conv, err := a.OpenConversation(ctx, "probe-b-"+suffix)
	if err != nil {
		return err
	}
	if _, err := a.SendMessage(ctx, conv.GetConversationId(), "probe", "probe"); err != nil {
		return err
	}
	for {
		select {
		case ev, ok := <-b.Events:
			if !ok {
				return fmt.Errorf("connection closed: %w", b.Err())
			}
			if ev.GetMessage() != nil {
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("no event delivered: %w", ctx.Err())
		}
	}
}
