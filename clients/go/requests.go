package wrinkleclient

import (
	"context"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func (c *Client) Connect(ctx context.Context, req *pb.ConnectRequest) (*pb.ConnectResponse, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_Connect{Connect: req}})
	if err != nil {
		return nil, err
	}
	return resp.GetConnect(), nil
}

func (c *Client) OpenConversation(ctx context.Context, peerUserName string) (*pb.Conversation, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_OpenConversation{
		OpenConversation: &pb.OpenConversationRequest{PeerUserName: peerUserName}}})
	if err != nil {
		return nil, err
	}
	return resp.GetOpenConversation().GetConversation(), nil
}

func (c *Client) SendMessage(ctx context.Context, conversationID uint64, clientMessageID, text string) (*pb.Message, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_SendMessage{
		SendMessage: &pb.SendMessageRequest{ConversationId: conversationID, ClientMessageId: clientMessageID, Text: text}}})
	if err != nil {
		return nil, err
	}
	return resp.GetSendMessage().GetMessage(), nil
}

func (c *Client) MarkRead(ctx context.Context, conversationID, seq uint64) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_MarkRead{
		MarkRead: &pb.MarkReadRequest{ConversationId: conversationID, Seq: seq}}})
	return err
}

func (c *Client) Sync(ctx context.Context, cursor uint64, limit uint32) (*pb.SyncResponse, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_Sync{
		Sync: &pb.SyncRequest{Cursor: cursor, Limit: limit}}})
	if err != nil {
		return nil, err
	}
	return resp.GetSync(), nil
}

func (c *Client) ListConversations(ctx context.Context) ([]*pb.Conversation, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_ListConversations{
		ListConversations: &pb.ListConversationsRequest{}}})
	if err != nil {
		return nil, err
	}
	return resp.GetListConversations().GetConversations(), nil
}

func (c *Client) GetMessages(ctx context.Context, conversationID, beforeSeq uint64, limit uint32) ([]*pb.Message, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_GetMessages{
		GetMessages: &pb.GetMessagesRequest{ConversationId: conversationID, BeforeSeq: beforeSeq, Limit: limit}}})
	if err != nil {
		return nil, err
	}
	return resp.GetGetMessages().GetMessages(), nil
}

func (c *Client) Ack(ctx context.Context, cursor uint64) error {
	return c.Send(ctx, &pb.Frame{Kind: &pb.Frame_Ack{Ack: &pb.Ack{Cursor: cursor}}})
}

func (c *Client) Ping(ctx context.Context) error {
	return c.Send(ctx, &pb.Frame{Kind: &pb.Frame_Ping{Ping: &pb.Ping{}}})
}

func (c *Client) RegisterPushToken(ctx context.Context, service pb.PushService, token string) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_RegisterPushToken{
		RegisterPushToken: &pb.RegisterPushTokenRequest{Service: service, Token: token}}})
	return err
}
