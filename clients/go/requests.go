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
	conv := resp.GetOpenConversation().GetConversation()
	c.Keys().learnConversation(conv)
	return conv, nil
}

// SendMessage sends sealed content as it is; SendText seals and sends.
func (c *Client) SendMessage(ctx context.Context, conversationID []byte, clientMessageID string, sealed *pb.Sealed) (*pb.Message, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_SendMessage{
		SendMessage: &pb.SendMessageRequest{ConversationId: conversationID, ClientMessageId: clientMessageID, Sealed: sealed}}})
	if err != nil {
		return nil, err
	}
	return resp.GetSendMessage().GetMessage(), nil
}

// DeleteMessage deletes one of the user's own messages for everyone.
func (c *Client) DeleteMessage(ctx context.Context, conversationID []byte, seq uint64) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_DeleteMessage{
		DeleteMessage: &pb.DeleteMessageRequest{ConversationId: conversationID, Seq: seq}}})
	return err
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
	convs := resp.GetListConversations().GetConversations()
	for _, conv := range convs {
		c.Keys().learnConversation(conv)
	}
	return convs, nil
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

// UpdateAvatar sets the profile picture to an uploaded blob; empty removes
// it.
func (c *Client) UpdateAvatar(ctx context.Context, blobID []byte) (*pb.User, error) {
	if blobID == nil {
		blobID = []byte{}
	}
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_UpdateProfile{
		UpdateProfile: &pb.UpdateProfileRequest{Avatar: blobID}}})
	if err != nil {
		return nil, err
	}
	return resp.GetUpdateProfile().GetUser(), nil
}

func (c *Client) ListDevices(ctx context.Context) ([]*pb.Device, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_ListDevices{ListDevices: &pb.ListDevicesRequest{}}})
	if err != nil {
		return nil, err
	}
	return resp.GetListDevices().GetDevices(), nil
}

func (c *Client) RenameDevice(ctx context.Context, deviceID uint64, name string) (*pb.Device, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_RenameDevice{
		RenameDevice: &pb.RenameDeviceRequest{DeviceId: deviceID, Name: name}}})
	if err != nil {
		return nil, err
	}
	return resp.GetRenameDevice().GetDevice(), nil
}

// SignOutDevice signs out another device of the account.
func (c *Client) SignOutDevice(ctx context.Context, deviceID uint64) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_SignOutDevice{
		SignOutDevice: &pb.SignOutDeviceRequest{DeviceId: deviceID}}})
	return err
}

func (c *Client) DeleteAccount(ctx context.Context, password string) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_DeleteAccount{
		DeleteAccount: &pb.DeleteAccountRequest{Password: password}}})
	return err
}

func (c *Client) CreateUpload(ctx context.Context, purpose pb.BlobPurpose, mediaType string, size uint64) (*pb.CreateUploadResponse, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_CreateUpload{
		CreateUpload: &pb.CreateUploadRequest{Purpose: purpose, MediaType: mediaType, Size: size}}})
	if err != nil {
		return nil, err
	}
	return resp.GetCreateUpload(), nil
}
