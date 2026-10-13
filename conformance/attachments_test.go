package conformance

import (
	"bytes"
	"crypto/sha256"
	"net/http"
	"testing"

	"google.golang.org/protobuf/proto"

	wrinkleclient "github.com/vlumi/wrinkle/clients/go"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// attachmentUpload uploads bytes as an attachment blob, returning its id.
func attachmentUpload(t *testing.T, d *device, data []byte) []byte {
	t.Helper()
	return upload(t, d, pb.BlobPurpose_BLOB_PURPOSE_ATTACHMENT, "application/octet-stream", data)
}

func TestSendAttachmentValidation(t *testing.T) {
	a, b, conv := pair(t)
	epoch, err := a.PrepareKeys(ctx(t), conv)
	if err != nil {
		t.Fatal(err)
	}
	send := func(clientMessageID string, blobIDs ...[]byte) (*pb.Message, error) {
		return a.SendMessage(ctx(t), conv, clientMessageID, junkSealed(epoch), blobIDs...)
	}
	mine := attachmentUpload(t, a, randomBytes(t, 100))

	// At most ten distinct 16-byte ids.
	_, err = send("short id", []byte{1, 2, 3})
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = send("long id", append(bytes.Clone(mine), 0))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	_, err = send("duplicates", mine, mine)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	eleven := make([][]byte, 11)
	for i := range eleven {
		eleven[i] = randomID(t)
	}
	_, err = send("eleven", eleven...)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	// Each the sender's attachment upload, uploaded.
	_, err = send("unknown", randomID(t))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	_, err = send("theirs", attachmentUpload(t, b, randomBytes(t, 100)))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	_, err = send("sticker", upload(t, a, pb.BlobPurpose_BLOB_PURPOSE_STICKER, "image/png", randomBytes(t, 100)))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	_, err = send("avatar", upload(t, a, pb.BlobPurpose_BLOB_PURPOSE_AVATAR, "image/png", randomBytes(t, 100)))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	slot, err := a.CreateUpload(ctx(t), pb.BlobPurpose_BLOB_PURPOSE_ATTACHMENT, "application/octet-stream", 100)
	if err != nil {
		t.Fatal(err)
	}
	_, err = send("not uploaded", mine, slot.GetBlobId())
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	// Nothing was sent.
	if msgs, err := a.GetMessages(ctx(t), conv, 0, 0); err != nil || len(msgs) != 0 {
		t.Fatalf("history after refused sends: %v %v", msgs, err)
	}

	ten := [][]byte{mine}
	for len(ten) < 10 {
		ten = append(ten, attachmentUpload(t, a, randomBytes(t, 50)))
	}
	msg, err := send("ten", ten...)
	if err != nil {
		t.Fatalf("ten attachments: %v", err)
	}
	if !equalIDs(msg.GetBlobIds(), ten) {
		t.Fatalf("blob ids %x, want %x", msg.GetBlobIds(), ten)
	}
	// A blob goes with one message only.
	_, err = send("again", mine)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	_, err = send("again with a new one", attachmentUpload(t, a, randomBytes(t, 50)), ten[1])
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION)
	// Resending the message still returns it.
	if again, err := send("ten", ten...); err != nil || again.GetSeq() != msg.GetSeq() {
		t.Fatalf("resend: %v %v", again, err)
	}
}

func TestAttachments(t *testing.T) {
	a, b, conv := pair(t)
	a2 := otherDevice(t, a)
	image := wrinkleclient.File{
		Name: "photo.jpg", MediaType: "image/jpeg", Data: randomBytes(t, 70_000),
		Width: 2048, Height: 1536, Thumbnail: randomBytes(t, 4096),
	}
	file := wrinkleclient.File{Name: "notes.txt", MediaType: "text/plain", Data: []byte("attached notes")}
	sent, err := a.SendAttachments(ctx(t), blobAddress(), conv, "with files", "a caption", []wrinkleclient.File{image, file})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent.GetBlobIds()) != 2 {
		t.Fatalf("sent blob ids %x", sent.GetBlobIds())
	}

	for _, d := range []*device{a2, b} {
		m := messageEvent(t, d, conv, sent.GetSeq())
		if !equalIDs(m.GetBlobIds(), sent.GetBlobIds()) {
			t.Fatalf("%s's blob ids %x, want %x", d.user.GetName(), m.GetBlobIds(), sent.GetBlobIds())
		}
	}
	m := messageEvent(t, a, conv, sent.GetSeq())
	if !equalIDs(m.GetBlobIds(), sent.GetBlobIds()) {
		t.Fatalf("sender's blob ids %x", m.GetBlobIds())
	}

	// The recipient opens the content to the attachments and decrypts each
	// file from its blob.
	msgs, err := b.GetMessages(ctx(t), conv, 0, 0)
	if err != nil || len(msgs) != 1 || !equalIDs(msgs[0].GetBlobIds(), sent.GetBlobIds()) {
		t.Fatalf("history: %v %v", msgs, err)
	}
	content, err := b.OpenMessage(ctx(t), msgs[0])
	if err != nil {
		t.Fatal(err)
	}
	att := content.GetAttachments()
	if att.GetCaption() != "a caption" || len(att.GetItems()) != 2 {
		t.Fatalf("attachments %v", att)
	}
	for i, f := range []wrinkleclient.File{image, file} {
		item := att.GetItems()[i]
		if !bytes.Equal(item.GetBlobId(), sent.GetBlobIds()[i]) || item.GetName() != f.Name || item.GetMediaType() != f.MediaType ||
			item.GetSize() != uint64(len(f.Data)) || item.GetWidth() != f.Width || item.GetHeight() != f.Height ||
			!bytes.Equal(item.GetThumbnail(), f.Thumbnail) {
			t.Fatalf("attachment %d: %v", i, item)
		}
		status, blob := getBlob(t, item.GetBlobId())
		if status != http.StatusOK {
			t.Fatalf("GET attachment %d: %d", i, status)
		}
		if sum := sha256.Sum256(blob); !bytes.Equal(sum[:], item.GetDigest()) || bytes.Contains(blob, f.Data) {
			t.Fatalf("attachment %d's blob is not its ciphertext", i)
		}
		data, err := wrinkleclient.DownloadAttachment(ctx(t), blobAddress(), item)
		if err != nil || !bytes.Equal(data, f.Data) {
			t.Fatalf("attachment %d decrypts to %d bytes, %v", i, len(data), err)
		}
	}
	if last := findConversation(t, b, conv).GetLastMessage(); !equalIDs(last.GetBlobIds(), sent.GetBlobIds()) {
		t.Fatalf("listed last message %v", last)
	}

	// Resending returns the message as stored.
	epoch, err := a.PrepareKeys(ctx(t), conv)
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.SendMessage(ctx(t), conv, "with files", junkSealed(epoch), sent.GetBlobIds()...)
	if err != nil {
		t.Fatal(err)
	}
	if again.GetSeq() != sent.GetSeq() || !equalIDs(again.GetBlobIds(), sent.GetBlobIds()) ||
		!proto.Equal(again.GetSealed(), sent.GetSealed()) {
		t.Fatalf("resend %v, want %v", again, sent)
	}

	// Deleting the message drops its attachments with the content.
	if err := a.DeleteMessage(ctx(t), conv, sent.GetSeq()); err != nil {
		t.Fatal(err)
	}
	deleted := deletedEvent(t, b, conv, sent.GetSeq())
	msgs, err = b.GetMessages(ctx(t), conv, 0, 0)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("history: %v %v", msgs, err)
	}
	tombstone(t, "history", msgs[0], deleted)
	if len(msgs[0].GetBlobIds()) != 0 {
		t.Fatalf("deleted message keeps blob ids %x", msgs[0].GetBlobIds())
	}
	if last := findConversation(t, b, conv).GetLastMessage(); last.GetContent() != nil || len(last.GetBlobIds()) != 0 {
		t.Fatalf("listed deleted message %v", last)
	}
	for _, ev := range syncAll(t, b, 0) {
		if m := ev.GetMessage().GetMessage(); bytes.Equal(m.GetConversationId(), conv) && m.GetSeq() == sent.GetSeq() {
			tombstone(t, "synced message", m, deleted)
			if len(m.GetBlobIds()) != 0 {
				t.Fatalf("synced deleted message keeps blob ids %x", m.GetBlobIds())
			}
		}
	}
}
