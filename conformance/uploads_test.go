package conformance

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// blobAddress is where blobs are uploaded and read: WRINKLE_BLOBS, by
// default the local blob proxy.
func blobAddress() string {
	if u := os.Getenv("WRINKLE_BLOBS"); u != "" {
		return u
	}
	return "http://127.0.0.1:9200"
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// upload makes a slot for the bytes and PUTs them there, returning the
// blob id.
func upload(t *testing.T, d *device, purpose pb.BlobPurpose, mediaType string, data []byte) []byte {
	t.Helper()
	slot, err := d.CreateUpload(ctx(t), purpose, mediaType, uint64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	status, err := wrinkleclient.Upload(ctx(t), blobAddress(), slot.GetUploadPath(), mediaType, data)
	if err != nil || status != http.StatusOK {
		t.Fatalf("PUT: %d %v", status, err)
	}
	return slot.GetBlobId()
}

func TestCreateUploadLimits(t *testing.T) {
	d := newUser(t, "limits")
	for _, c := range []struct {
		purpose   pb.BlobPurpose
		mediaType string
		size      uint64
	}{
		{pb.BlobPurpose_BLOB_PURPOSE_UNSPECIFIED, "image/png", 100},
		{pb.BlobPurpose_BLOB_PURPOSE_AVATAR, "image/gif", 100},
		{pb.BlobPurpose_BLOB_PURPOSE_AVATAR, "image/png", 512*1024 + 1},
		{pb.BlobPurpose_BLOB_PURPOSE_AVATAR, "image/png", 0},
		{pb.BlobPurpose_BLOB_PURPOSE_STICKER, "image/jpeg", 100},
		{pb.BlobPurpose_BLOB_PURPOSE_STICKER, "image/png", 256*1024 + 1},
		{pb.BlobPurpose_BLOB_PURPOSE_ATTACHMENT, "", 100},
		{pb.BlobPurpose_BLOB_PURPOSE_ATTACHMENT, "no type", 100},
		{pb.BlobPurpose_BLOB_PURPOSE_ATTACHMENT, "application/pdf", 100},
		{pb.BlobPurpose_BLOB_PURPOSE_ATTACHMENT, "application/octet-stream", 25*1024*1024 + 1},
	} {
		_, err := d.CreateUpload(ctx(t), c.purpose, c.mediaType, c.size)
		if wrinkleclient.Code(err) != pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
			t.Errorf("%v %q %d: %v", c.purpose, c.mediaType, c.size, err)
		}
	}
	for _, c := range []struct {
		purpose   pb.BlobPurpose
		mediaType string
		size      uint64
	}{
		{pb.BlobPurpose_BLOB_PURPOSE_AVATAR, "image/jpeg", 512 * 1024},
		{pb.BlobPurpose_BLOB_PURPOSE_STICKER, "image/gif", 1},
		{pb.BlobPurpose_BLOB_PURPOSE_ATTACHMENT, "application/octet-stream", 25 * 1024 * 1024},
	} {
		if _, err := d.CreateUpload(ctx(t), c.purpose, c.mediaType, c.size); err != nil {
			t.Errorf("%v %q %d: %v", c.purpose, c.mediaType, c.size, err)
		}
	}
}

func TestUploadAndRead(t *testing.T) {
	d := newUser(t, "uploader")
	data := randomBytes(t, 1000)
	before := time.Now()
	slot, err := d.CreateUpload(ctx(t), pb.BlobPurpose_BLOB_PURPOSE_ATTACHMENT, "application/octet-stream", uint64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(slot.GetBlobId()) != 16 || !strings.HasPrefix(slot.GetUploadPath(), fmt.Sprintf("/blobs/%x?", slot.GetBlobId())) {
		t.Fatalf("slot %v", slot)
	}
	if !slot.GetExpiresAt().AsTime().After(before) {
		t.Fatalf("slot expires at %v", slot.GetExpiresAt().AsTime())
	}

	// Only exactly what was asked for is accepted.
	status, err := wrinkleclient.Upload(ctx(t), blobAddress(), slot.GetUploadPath(), "application/octet-stream", data[:999])
	if err != nil || status/100 == 2 {
		t.Fatalf("PUT of another length: %d %v", status, err)
	}
	status, err = wrinkleclient.Upload(ctx(t), blobAddress(), slot.GetUploadPath(), "text/plain", data)
	if err != nil || status/100 == 2 {
		t.Fatalf("PUT of another type: %d %v", status, err)
	}
	status, err = wrinkleclient.Upload(ctx(t), blobAddress(), slot.GetUploadPath(), "application/octet-stream", data)
	if err != nil || status != http.StatusOK {
		t.Fatalf("PUT: %d %v", status, err)
	}

	req, err := http.NewRequestWithContext(ctx(t), http.MethodGet, wrinkleclient.BlobURL(blobAddress(), slot.GetBlobId()), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(got, data) {
		t.Fatalf("GET: %d, %d bytes, %v", resp.StatusCode, len(got), err)
	}
}

func avatarEvent(t *testing.T, d *device, userID, avatar []byte) {
	t.Helper()
	nextEvent(t, d, func(ev *pb.Event) bool {
		u := ev.GetProfile().GetUser()
		return u != nil && bytes.Equal(u.GetUserId(), userID) && bytes.Equal(u.GetAvatar(), avatar)
	})
}

func TestAvatar(t *testing.T) {
	a, b, conv := pair(t)
	if _, err := a.SendText(ctx(t), conv, "m1", "hi"); err != nil {
		t.Fatal(err)
	}
	messageEvent(t, b, conv, 1)

	data := randomBytes(t, 2000)
	slot, err := a.CreateUpload(ctx(t), pb.BlobPurpose_BLOB_PURPOSE_AVATAR, "image/png", uint64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.UpdateAvatar(ctx(t), slot.GetBlobId())
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	theirs := upload(t, b, pb.BlobPurpose_BLOB_PURPOSE_AVATAR, "image/png", randomBytes(t, 100))
	_, err = a.UpdateAvatar(ctx(t), theirs)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	sticker := upload(t, a, pb.BlobPurpose_BLOB_PURPOSE_STICKER, "image/png", randomBytes(t, 100))
	_, err = a.UpdateAvatar(ctx(t), sticker)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	_, err = a.UpdateAvatar(ctx(t), randomID(t))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	_, err = a.UpdateAvatar(ctx(t), []byte{1, 2, 3})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	status, err := wrinkleclient.Upload(ctx(t), blobAddress(), slot.GetUploadPath(), "image/png", data)
	if err != nil || status != http.StatusOK {
		t.Fatalf("PUT: %d %v", status, err)
	}
	user, err := a.UpdateAvatar(ctx(t), slot.GetBlobId())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(user.GetAvatar(), slot.GetBlobId()) || user.GetName() != a.user.GetName() {
		t.Fatalf("after setting the avatar: %v", user)
	}
	avatarEvent(t, b, a.user.GetUserId(), slot.GetBlobId())
	if c := findConversation(t, b, conv); !bytes.Equal(c.GetPeer().GetAvatar(), slot.GetBlobId()) {
		t.Fatalf("partner's list shows %v", c.GetPeer())
	}
	if again := reconnect(t, a); !bytes.Equal(again.user.GetAvatar(), slot.GetBlobId()) {
		t.Fatalf("connect says %v", again.user)
	}
	// A new conversation shows it too.
	c := newUser(t, "carol")
	if opened, err := c.OpenConversation(ctx(t), a.user.GetName()); err != nil || !bytes.Equal(opened.GetPeer().GetAvatar(), slot.GetBlobId()) {
		t.Fatalf("open: %v %v", opened, err)
	}

	user, err = a.UpdateAvatar(ctx(t), nil)
	if err != nil || len(user.GetAvatar()) != 0 {
		t.Fatalf("clear: %v %v", user, err)
	}
	avatarEvent(t, b, a.user.GetUserId(), nil)
	if c := findConversation(t, b, conv); len(c.GetPeer().GetAvatar()) != 0 {
		t.Fatalf("partner's list shows %v", c.GetPeer())
	}
}
