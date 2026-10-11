package wrinkleclient

import (
	"context"
	"fmt"
	"net/http"

	"github.com/vlumi/wrinkle/clients/go/e2ee"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// blobMediaType is how every attachment blob is uploaded: the services
// only see ciphertext.
const blobMediaType = "application/octet-stream"

// File is a file to attach; images also carry their size in pixels and a
// small JPEG thumbnail.
type File struct {
	Name      string
	MediaType string
	Data      []byte
	Width     uint32
	Height    uint32
	Thumbnail []byte
}

// UploadAttachment encrypts a file with a new key, uploads the blob and
// returns the attachment that refers to it.
func (c *Client) UploadAttachment(ctx context.Context, blobAddress string, f File) (*pb.Attachment, error) {
	key, blob, digest, err := e2ee.EncryptBlob(f.Data)
	if err != nil {
		return nil, err
	}
	upload, err := c.CreateUpload(ctx, pb.BlobPurpose_BLOB_PURPOSE_ATTACHMENT, blobMediaType, uint64(len(blob)))
	if err != nil {
		return nil, err
	}
	status, err := Upload(ctx, blobAddress, upload.GetUploadPath(), blobMediaType, blob)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("attachment upload: HTTP %d", status)
	}
	return &pb.Attachment{
		BlobId:    upload.GetBlobId(),
		Key:       key,
		Digest:    digest,
		MediaType: f.MediaType,
		Size:      uint64(len(f.Data)),
		Name:      f.Name,
		Width:     f.Width,
		Height:    f.Height,
		Thumbnail: f.Thumbnail,
	}, nil
}

// SendAttachments uploads 1 to 10 files and sends them, with an optional
// caption, as one message.
func (c *Client) SendAttachments(ctx context.Context, blobAddress string, conversationID []byte, clientMessageID, caption string, files []File) (*pb.Message, error) {
	items := make([]*pb.Attachment, 0, len(files))
	for _, f := range files {
		a, err := c.UploadAttachment(ctx, blobAddress, f)
		if err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return c.SendContent(ctx, conversationID, clientMessageID, &pb.Content{
		Body: &pb.Content_Attachments{Attachments: &pb.Attachments{Items: items, Caption: caption}},
	})
}

// DownloadAttachment downloads an attachment's blob, checks its digest and
// decrypts the file.
func DownloadAttachment(ctx context.Context, blobAddress string, a *pb.Attachment) ([]byte, error) {
	blob, err := Download(ctx, blobAddress, a.GetBlobId())
	if err != nil {
		return nil, err
	}
	return e2ee.DecryptBlob(a.GetKey(), a.GetDigest(), blob)
}
