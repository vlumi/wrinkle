package wrinkleclient

import (
	"context"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Register creates an account and returns this device's credentials; the
// connection still has to Connect.
func (c *Client) Register(ctx context.Context, name, password, deviceName string) (*pb.DeviceCredentials, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_Register{
		Register: &pb.RegisterRequest{Name: name, Password: password, DeviceName: deviceName}}})
	if err != nil {
		return nil, err
	}
	return resp.GetRegister().GetCredentials(), nil
}

// SignIn signs a new device in to an account; the connection still has to
// Connect.
func (c *Client) SignIn(ctx context.Context, name, password, deviceName string) (*pb.DeviceCredentials, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_SignIn{
		SignIn: &pb.SignInRequest{Name: name, Password: password, DeviceName: deviceName}}})
	if err != nil {
		return nil, err
	}
	return resp.GetSignIn().GetCredentials(), nil
}

// Connect binds the connection to the device the token belongs to.
func (c *Client) Connect(ctx context.Context, deviceToken string) (*pb.ConnectResponse, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_Connect{
		Connect: &pb.ConnectRequest{DeviceToken: deviceToken}}})
	if err != nil {
		return nil, err
	}
	return resp.GetConnect(), nil
}

func (c *Client) ChangePassword(ctx context.Context, currentPassword, newPassword string) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_ChangePassword{
		ChangePassword: &pb.ChangePasswordRequest{CurrentPassword: currentPassword, NewPassword: newPassword}}})
	return err
}

// UpdateProfile changes the fields given; nil keeps a field as it is.
func (c *Client) UpdateProfile(ctx context.Context, name, icon *string) (*pb.User, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_UpdateProfile{
		UpdateProfile: &pb.UpdateProfileRequest{Name: name, Icon: icon}}})
	if err != nil {
		return nil, err
	}
	return resp.GetUpdateProfile().GetUser(), nil
}

// SignOut invalidates this device's token; close the connection afterwards.
func (c *Client) SignOut(ctx context.Context) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_SignOut{SignOut: &pb.SignOutRequest{}}})
	return err
}

func (c *Client) OpenConversation(ctx context.Context, peerName string) (*pb.Conversation, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_OpenConversation{
		OpenConversation: &pb.OpenConversationRequest{PeerName: peerName}}})
	if err != nil {
		return nil, err
	}
	return resp.GetOpenConversation().GetConversation(), nil
}

func (c *Client) SendMessage(ctx context.Context, conversationID []byte, clientMessageID, text string) (*pb.Message, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_SendMessage{
		SendMessage: &pb.SendMessageRequest{ConversationId: conversationID, ClientMessageId: clientMessageID, Text: text}}})
	if err != nil {
		return nil, err
	}
	return resp.GetSendMessage().GetMessage(), nil
}

func (c *Client) MarkRead(ctx context.Context, conversationID []byte, seq uint64) error {
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

func (c *Client) GetMessages(ctx context.Context, conversationID []byte, beforeSeq uint64, limit uint32) ([]*pb.Message, error) {
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
