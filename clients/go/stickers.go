package wrinkleclient

import (
	"context"
	"fmt"
	"net/http"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func (c *Client) CreateSticker(ctx context.Context, blobID []byte) (*pb.Sticker, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_CreateSticker{
		CreateSticker: &pb.CreateStickerRequest{BlobId: blobID}}})
	if err != nil {
		return nil, err
	}
	return resp.GetCreateSticker().GetSticker(), nil
}

// NewSticker uploads an image as a sticker blob and makes it a sticker,
// which also saves it to the palette.
func (c *Client) NewSticker(ctx context.Context, blobAddress, mediaType string, image []byte) (*pb.Sticker, error) {
	upload, err := c.CreateUpload(ctx, pb.BlobPurpose_BLOB_PURPOSE_STICKER, mediaType, uint64(len(image)))
	if err != nil {
		return nil, err
	}
	status, err := Upload(ctx, blobAddress, upload.GetUploadPath(), mediaType, image)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("sticker upload: HTTP %d", status)
	}
	return c.CreateSticker(ctx, upload.GetBlobId())
}

// GetStickers returns the known stickers among up to 100 ids, in the order
// asked.
func (c *Client) GetStickers(ctx context.Context, stickerIDs [][]byte) ([]*pb.Sticker, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_GetStickers{
		GetStickers: &pb.GetStickersRequest{StickerIds: stickerIDs}}})
	if err != nil {
		return nil, err
	}
	return resp.GetGetStickers().GetStickers(), nil
}

func (c *Client) GetPalette(ctx context.Context) ([]*pb.Sticker, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_GetPalette{GetPalette: &pb.GetPaletteRequest{}}})
	if err != nil {
		return nil, err
	}
	return resp.GetGetPalette().GetStickers(), nil
}

func (c *Client) SaveSticker(ctx context.Context, stickerID []byte) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_SaveSticker{
		SaveSticker: &pb.SaveStickerRequest{StickerId: stickerID}}})
	return err
}

func (c *Client) RemoveSticker(ctx context.Context, stickerID []byte) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_RemoveSticker{
		RemoveSticker: &pb.RemoveStickerRequest{StickerId: stickerID}}})
	return err
}

// MoveSticker moves a saved sticker before another; nil moves it to the
// end.
func (c *Client) MoveSticker(ctx context.Context, stickerID, beforeStickerID []byte) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_MoveSticker{
		MoveSticker: &pb.MoveStickerRequest{StickerId: stickerID, BeforeStickerId: beforeStickerID}}})
	return err
}

// SendSticker seals a sticker as the message's content and sends it.
func (c *Client) SendSticker(ctx context.Context, conversationID []byte, clientMessageID string, stickerID []byte) (*pb.Message, error) {
	return c.SendContent(ctx, conversationID, clientMessageID,
		&pb.Content{Body: &pb.Content_Sticker{Sticker: &pb.StickerRef{StickerId: stickerID}}})
}
