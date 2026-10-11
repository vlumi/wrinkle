package main

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
	"github.com/vlumi/wrinkle/clients/go/e2ee"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const callTimeout = 10 * time.Second

// sendSeq keeps client message ids unique among sends in the same
// nanosecond.
var sendSeq atomic.Uint64

type device struct {
	user            int
	userName        string
	peerName        string
	stats           *stats
	markRead        bool
	client          *wrinkleclient.Client
	token           string
	userID          []byte
	deviceID        uint64
	conversationID  []byte
	directReceivers int64
	// The device's group, if any, and the share of sends that go there.
	groupID        []byte
	groupReceivers int64
	groupShare     float64
	// The share of sends with an attachment, its size, and where blobs go.
	attachShare float64
	attachSize  int
	blobAddress string
}

// maxAttachmentSize keeps the encrypted blob within the 25 MiB limit.
const maxAttachmentSize = 25<<20 - e2ee.NonceSize - e2ee.TagSize

// connect connects the device with the token it signed in with and gives
// it a key pair.
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
	_, err = c.Connect(cctx, d.token)
	if err == nil {
		_, err = c.EnableKeys(cctx, d.userID, d.deviceID)
	}
	if err != nil {
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

// send seals and sends one message, to the device's group for its share of
// the sends and to its direct conversation otherwise, whose client id
// carries the send time, so any receiving device can compute the end-to-end
// latency. For its share of the sends, a random file is encrypted and
// uploaded first, and the message carries it; the send time is taken after
// the upload.
func (d *device) send(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	content := &pb.Content{Body: &pb.Content_Text{Text: "load"}}
	if d.attachShare > 0 && rand.Float64() < d.attachShare {
		at := time.Now()
		file := make([]byte, d.attachSize)
		crand.Read(file)
		a, err := d.client.UploadAttachment(cctx, d.blobAddress, wrinkleclient.File{
			Name: "load.bin", MediaType: "application/octet-stream", Data: file,
		})
		if err != nil {
			if d.stats.recordUpload(at, false) <= 3 {
				fmt.Fprintf(os.Stderr, "upload from %s: %v\n", d.userName, err)
			}
			return
		}
		d.stats.recordUpload(at, true)
		content.Body = &pb.Content_Attachments{Attachments: &pb.Attachments{Items: []*pb.Attachment{a}}}
	}
	now := time.Now()
	id := fmt.Sprintf("%d-%d", now.UnixNano(), sendSeq.Add(1))
	conversationID, receivers, group := d.conversationID, d.directReceivers, false
	if d.groupID != nil && rand.Float64() < d.groupShare {
		conversationID, receivers, group = d.groupID, d.groupReceivers, true
	}
	d.stats.recordSent(now, receivers, group)
	if _, err := d.client.SendContent(cctx, conversationID, id, content); err != nil {
		if d.stats.sendErrors.Add(1) <= 3 {
			fmt.Fprintf(os.Stderr, "send from %s: %v\n", d.userName, err)
		}
		return
	}
	d.stats.recordSendRTT(now, time.Since(now))
}

// receive drains events, opening the peers' messages and recording their
// latency, and acks at most once a second.
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
			if msg.GetSealed() == nil || bytes.Equal(msg.GetSenderUserId(), d.userID) {
				continue
			}
			content, err := d.client.OpenMessage(ctx, msg)
			if err != nil {
				if d.stats.decryptErrors.Add(1) <= 3 {
					fmt.Fprintf(os.Stderr, "open message by %s: %v\n", d.userName, err)
				}
				continue
			}
			sentAt, ok := sentTime(msg.GetClientMessageId())
			if ok {
				d.stats.recordReceipt(sentAt, time.Since(sentAt))
			}
			for _, a := range content.GetAttachments().GetItems() {
				d.stats.downloading.Add(1)
				go d.download(ctx, a, sentAt)
			}
			if d.markRead {
				go func() {
					cctx, cancel := context.WithTimeout(ctx, callTimeout)
					defer cancel()
					if err := d.client.MarkRead(cctx, msg.GetConversationId(), msg.GetSeq()); err != nil {
						if d.stats.sendErrors.Add(1) <= 3 {
							fmt.Fprintf(os.Stderr, "mark read by %s: %v\n", d.userName, err)
						}
					}
				}()
			}
		}
	}
}

// download fetches an attachment, checks its digest and decrypts it.
func (d *device) download(ctx context.Context, a *pb.Attachment, sentAt time.Time) {
	defer d.stats.downloading.Add(-1)
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	file, err := wrinkleclient.DownloadAttachment(cctx, d.blobAddress, a)
	if err == nil && uint64(len(file)) != a.GetSize() {
		err = fmt.Errorf("%d bytes, not %d", len(file), a.GetSize())
	}
	if d.stats.recordDownload(sentAt, err == nil) <= 3 && err != nil {
		fmt.Fprintf(os.Stderr, "download by %s: %v\n", d.userName, err)
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
