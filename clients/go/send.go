package wrinkleclient

import (
	"context"

	"github.com/vlumi/wrinkle/clients/go/e2ee"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// SendText seals text with the conversation's current epoch, publishing a
// new one first when needed, and sends it.
func (c *Client) SendText(ctx context.Context, conversationID []byte, clientMessageID, text string) (*pb.Message, error) {
	return c.SendContent(ctx, conversationID, clientMessageID, &pb.Content{Body: &pb.Content_Text{Text: text}})
}

// SendContent is SendText for any content; attachments' blob ids go with
// it. A send refused for its epoch is retried once after checking the keys
// again.
func (c *Client) SendContent(ctx context.Context, conversationID []byte, clientMessageID string, content *pb.Content) (*pb.Message, error) {
	k, err := c.requireKeys()
	if err != nil {
		return nil, err
	}
	var blobIDs [][]byte
	for _, a := range content.GetAttachments().GetItems() {
		blobIDs = append(blobIDs, a.GetBlobId())
	}
	for attempt := 0; ; attempt++ {
		epoch, key, err := k.sendKey(ctx, c, conversationID)
		if err != nil {
			return nil, err
		}
		sealed, err := e2ee.SealContent(key, e2ee.ContentContext{
			ConversationID: conversationID, Epoch: epoch, SenderUserID: k.UserID, ClientMessageID: clientMessageID,
		}, content)
		if err != nil {
			return nil, err
		}
		msg, err := c.SendMessage(ctx, conversationID, clientMessageID, sealed, blobIDs...)
		if Code(err) == pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION && attempt == 0 {
			k.Invalidate(conversationID)
			continue
		}
		return msg, err
	}
}

// PrepareKeys makes sure the conversation has an epoch this device can
// send with, publishing one if needed, and returns it.
func (c *Client) PrepareKeys(ctx context.Context, conversationID []byte) (uint32, error) {
	k, err := c.requireKeys()
	if err != nil {
		return 0, err
	}
	epoch, _, err := k.sendKey(ctx, c, conversationID)
	return epoch, err
}

// OpenMessage opens a message's sealed content, fetching the key of its
// epoch if this device does not have it yet.
func (c *Client) OpenMessage(ctx context.Context, m *pb.Message) (*pb.Content, error) {
	k, err := c.requireKeys()
	if err != nil {
		return nil, err
	}
	sealed := m.GetSealed()
	if sealed == nil {
		return nil, ErrNotSealed
	}
	key, err := k.epochKey(ctx, c, m.GetConversationId(), sealed.GetEpoch())
	if err != nil {
		return nil, err
	}
	return e2ee.OpenContent(key, e2ee.ContentContext{
		ConversationID:  m.GetConversationId(),
		Epoch:           sealed.GetEpoch(),
		SenderUserID:    m.GetSenderUserId(),
		ClientMessageID: m.GetClientMessageId(),
	}, sealed)
}

// MessageText opens a message and returns its text.
func (c *Client) MessageText(ctx context.Context, m *pb.Message) (string, error) {
	content, err := c.OpenMessage(ctx, m)
	if err != nil {
		return "", err
	}
	return content.GetText(), nil
}
