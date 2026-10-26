package conformance

import (
	"net/http"
	"testing"
	"time"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
)

// unusedFor is how long the backend keeps an unused upload, as run.sh
// starts it.
const unusedFor = 20 * time.Second

// blobStatus is the status of a GET of the blob.
func blobStatus(t *testing.T, blobID []byte) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx(t), http.MethodGet, wrinkleclient.BlobURL(blobAddress(), blobID), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestSweeperKeepsWhatMessagesUse(t *testing.T) {
	// Parallel, so the wait overlaps the other tests'.
	t.Parallel()
	a, b, conv := pair(t)
	unused := attachmentUpload(t, a, randomBytes(t, 100))
	attached := attachmentUpload(t, a, randomBytes(t, 100))
	epoch, err := a.PrepareKeys(ctx(t), conv)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := a.SendMessage(ctx(t), conv, "kept", junkSealed(epoch), attached)
	if err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, conv, msg.GetSeq())
	// The message stays with the conversation, and its attachment with it.
	if err := a.DeleteAccount(ctx(t), password); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2*unusedFor + eventTimeout)
	for blobStatus(t, unused) != http.StatusNotFound {
		if time.Now().After(deadline) {
			t.Fatalf("an unused upload is still there %s later", 2*unusedFor+eventTimeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if status := blobStatus(t, attached); status != http.StatusOK {
		t.Fatalf("the attachment of a deleted account's message: %d", status)
	}
}
