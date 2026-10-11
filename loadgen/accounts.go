package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const (
	// password is every load user's.
	password = "loadgen password"
	// signInWorkers bounds concurrent registrations and sign-ins, which
	// cost the backend a password hash each.
	signInWorkers = 16
	// connectionReuse is how long a worker sends sign-ins on one
	// connection, well within the backend's deadline for Connect.
	connectionReuse = 3 * time.Second
)

// signInAll gets every device its credentials before anything connects:
// each user's first device registers the user, or signs in if an earlier
// run did, and the others sign in. A device that fails is left without a
// token.
func signInAll(ctx context.Context, devices []*device, url string, devicesPerUser int, stats *stats) {
	users := make(chan []*device)
	var wg sync.WaitGroup
	for range signInWorkers {
		wg.Go(func() {
			w := &signInWorker{url: url}
			defer w.close()
			for user := range users {
				for i, d := range user {
					if err := w.signIn(ctx, d, i == 0); err != nil {
						stats.connectErrors.Add(1)
						fmt.Fprintf(os.Stderr, "sign in %s: %v\n", d.userName, err)
					}
				}
			}
		})
	}
	for i := 0; i < len(devices); i += devicesPerUser {
		select {
		case users <- devices[i : i+devicesPerUser]:
		case <-ctx.Done():
		}
	}
	close(users)
	wg.Wait()
}

// signInWorker holds a connection that has not connected, replacing it
// before the backend would close it.
type signInWorker struct {
	url    string
	client *wrinkleclient.Client
	opened time.Time
}

func (w *signInWorker) close() {
	if w.client != nil {
		w.client.Close()
		w.client = nil
	}
}

func (w *signInWorker) conn(ctx context.Context) (*wrinkleclient.Client, error) {
	if w.client != nil && (time.Since(w.opened) > connectionReuse || w.client.Err() != nil) {
		w.close()
	}
	if w.client == nil {
		c, err := wrinkleclient.Dial(ctx, w.url)
		if err != nil {
			return nil, err
		}
		w.client, w.opened = c, time.Now()
	}
	return w.client, nil
}

func (w *signInWorker) signIn(ctx context.Context, d *device, first bool) error {
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	c, err := w.conn(cctx)
	if err != nil {
		return err
	}
	var creds *pb.DeviceCredentials
	if first {
		creds, err = c.Register(cctx, d.userName, password, "loadgen")
		if wrinkleclient.Code(err) == pb.ErrorCode_ERROR_CODE_ALREADY_EXISTS {
			creds, err = c.SignIn(cctx, d.userName, password, "loadgen")
		}
	} else {
		creds, err = c.SignIn(cctx, d.userName, password, "loadgen")
	}
	if err != nil {
		w.close()
		return err
	}
	d.token = creds.GetDeviceToken()
	d.userID = creds.GetUserId()
	d.deviceID = creds.GetDeviceId()
	return nil
}
