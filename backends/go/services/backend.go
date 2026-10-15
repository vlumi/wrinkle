// Package services handles a device's requests: in-process over the
// account, conversation and sync services, or forwarded to a services
// process over the internal RPC. The gateway owns the connection state and
// uses one of the two.
package services

import (
	"context"
	"log/slog"

	"github.com/vlumi/wrinkle/backends/go/accounts"
	"github.com/vlumi/wrinkle/backends/go/contacts"
	"github.com/vlumi/wrinkle/backends/go/conversation"
	"github.com/vlumi/wrinkle/backends/go/devicesync"
	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/stickers"
	"github.com/vlumi/wrinkle/backends/go/uploads"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Backend handles requests for a device; the user id is zero until the
// connection has completed Connect. Call returns an error only when the
// request could not be handled at all; a request's own failure is the
// response's error body.
type Backend interface {
	Call(ctx context.Context, userID ids.UUID, deviceID uint64, req *pb.Request) (*pb.Response, error)
	Ack(ctx context.Context, userID ids.UUID, deviceID, cursor uint64) error
}

// Local runs the services in-process.
type Local struct {
	accounts      *accounts.Service
	conversations *conversation.Service
	users         *devicesync.Service
	uploads       *uploads.Service
	stickers      *stickers.Service
	contacts      *contacts.Service
	log           *slog.Logger
}

func NewLocal(accounts *accounts.Service, conversations *conversation.Service, users *devicesync.Service, uploads *uploads.Service, stickers *stickers.Service, contacts *contacts.Service, log *slog.Logger) *Local {
	return &Local{accounts: accounts, conversations: conversations, users: users, uploads: uploads, stickers: stickers, contacts: contacts, log: log}
}

func (l *Local) Call(ctx context.Context, userID ids.UUID, deviceID uint64, req *pb.Request) (*pb.Response, error) {
	resp := &pb.Response{RequestId: req.GetId()}
	if err := l.dispatch(ctx, userID, deviceID, req, resp); err != nil {
		if apierror.Code(err) == pb.ErrorCode_ERROR_CODE_INTERNAL {
			l.log.Error("request failed", "request", req.GetId(), "err", err)
		}
		return apierror.Response(req.GetId(), err), nil
	}
	return resp, nil
}

func (l *Local) Ack(ctx context.Context, userID ids.UUID, deviceID, cursor uint64) error {
	return l.users.Ack(ctx, userID, deviceID, cursor)
}

// dispatch handles one request, setting the response body on success.
func (l *Local) dispatch(ctx context.Context, userID ids.UUID, deviceID uint64, req *pb.Request, resp *pb.Response) error {
	switch req.GetBody().(type) {
	case *pb.Request_Register, *pb.Request_SignIn, *pb.Request_Connect:
		if !userID.IsZero() {
			return apierror.FailedPrecondition("already connected")
		}
		return l.signIn(ctx, req, resp)
	case nil:
		return apierror.InvalidArgument("request has no body")
	}
	if userID.IsZero() {
		return apierror.Unauthenticated("connect first")
	}
	switch r := req.GetBody().(type) {
	case *pb.Request_ChangePassword:
		if err := l.accounts.ChangePassword(ctx, userID, deviceID, r.ChangePassword); err != nil {
			return err
		}
		resp.Body = &pb.Response_ChangePassword{ChangePassword: &pb.ChangePasswordResponse{}}
	case *pb.Request_UpdateProfile:
		user, err := l.accounts.UpdateProfile(ctx, userID, r.UpdateProfile)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_UpdateProfile{UpdateProfile: &pb.UpdateProfileResponse{User: user}}
	case *pb.Request_SignOut:
		if err := l.accounts.SignOut(ctx, userID, deviceID); err != nil {
			return err
		}
		resp.Body = &pb.Response_SignOut{SignOut: &pb.SignOutResponse{}}
	case *pb.Request_ListDevices:
		list, err := l.accounts.ListDevices(ctx, userID)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_ListDevices{ListDevices: &pb.ListDevicesResponse{Devices: list}}
	case *pb.Request_RenameDevice:
		device, err := l.accounts.RenameDevice(ctx, userID, r.RenameDevice)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_RenameDevice{RenameDevice: &pb.RenameDeviceResponse{Device: device}}
	case *pb.Request_SignOutDevice:
		if err := l.accounts.SignOutDevice(ctx, userID, deviceID, r.SignOutDevice.GetDeviceId()); err != nil {
			return err
		}
		resp.Body = &pb.Response_SignOutDevice{SignOutDevice: &pb.SignOutDeviceResponse{}}
	case *pb.Request_DeleteAccount:
		leave := func(ctx context.Context) error { return l.users.LeaveGroups(ctx, userID) }
		if err := l.accounts.DeleteAccount(ctx, userID, r.DeleteAccount, leave); err != nil {
			return err
		}
		resp.Body = &pb.Response_DeleteAccount{DeleteAccount: &pb.DeleteAccountResponse{}}
	case *pb.Request_CreateUpload:
		upload, err := l.uploads.CreateUpload(ctx, userID, r.CreateUpload)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_CreateUpload{CreateUpload: upload}
	case *pb.Request_CreateSticker:
		sticker, err := l.stickers.Create(ctx, userID, r.CreateSticker.GetBlobId())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_CreateSticker{CreateSticker: &pb.CreateStickerResponse{Sticker: sticker}}
	case *pb.Request_GetStickers:
		list, err := l.stickers.Get(ctx, r.GetStickers.GetStickerIds())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_GetStickers{GetStickers: &pb.GetStickersResponse{Stickers: list}}
	case *pb.Request_GetPalette:
		list, err := l.stickers.Palette(ctx, userID)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_GetPalette{GetPalette: &pb.GetPaletteResponse{Stickers: list}}
	case *pb.Request_SaveSticker:
		if err := l.stickers.Save(ctx, userID, r.SaveSticker.GetStickerId()); err != nil {
			return err
		}
		resp.Body = &pb.Response_SaveSticker{SaveSticker: &pb.SaveStickerResponse{}}
	case *pb.Request_RemoveSticker:
		if err := l.stickers.Remove(ctx, userID, r.RemoveSticker.GetStickerId()); err != nil {
			return err
		}
		resp.Body = &pb.Response_RemoveSticker{RemoveSticker: &pb.RemoveStickerResponse{}}
	case *pb.Request_MoveSticker:
		if err := l.stickers.Move(ctx, userID, r.MoveSticker.GetStickerId(), r.MoveSticker.GetBeforeStickerId()); err != nil {
			return err
		}
		resp.Body = &pb.Response_MoveSticker{MoveSticker: &pb.MoveStickerResponse{}}
	case *pb.Request_ListContacts:
		list, err := l.contacts.List(ctx, userID)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_ListContacts{ListContacts: &pb.ListContactsResponse{Contacts: list}}
	case *pb.Request_AddContact:
		contact, err := l.contacts.Add(ctx, userID, r.AddContact)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_AddContact{AddContact: &pb.AddContactResponse{Contact: contact}}
	case *pb.Request_RemoveContact:
		if err := l.contacts.Remove(ctx, userID, r.RemoveContact.GetUserId()); err != nil {
			return err
		}
		resp.Body = &pb.Response_RemoveContact{RemoveContact: &pb.RemoveContactResponse{}}
	case *pb.Request_BlockUser:
		if err := l.contacts.Block(ctx, userID, r.BlockUser.GetUserId()); err != nil {
			return err
		}
		resp.Body = &pb.Response_BlockUser{BlockUser: &pb.BlockUserResponse{}}
	case *pb.Request_UnblockUser:
		if err := l.contacts.Unblock(ctx, userID, r.UnblockUser.GetUserId()); err != nil {
			return err
		}
		resp.Body = &pb.Response_UnblockUser{UnblockUser: &pb.UnblockUserResponse{}}
	case *pb.Request_OpenConversation:
		conv, err := l.users.Open(ctx, userID, r.OpenConversation.GetPeerName())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_OpenConversation{OpenConversation: &pb.OpenConversationResponse{Conversation: conv}}
	case *pb.Request_OpenNotes:
		conv, err := l.users.OpenNotes(ctx, userID)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_OpenNotes{OpenNotes: &pb.OpenNotesResponse{Conversation: conv}}
	case *pb.Request_CreateGroup:
		conv, err := l.users.CreateGroup(ctx, userID, r.CreateGroup)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_CreateGroup{CreateGroup: &pb.CreateGroupResponse{Conversation: conv}}
	case *pb.Request_AddMembers:
		if err := l.users.AddMembers(ctx, userID, r.AddMembers); err != nil {
			return err
		}
		resp.Body = &pb.Response_AddMembers{AddMembers: &pb.AddMembersResponse{}}
	case *pb.Request_RemoveMember:
		conversationID, targetID, err := parseMemberIDs(r.RemoveMember.GetConversationId(), r.RemoveMember.GetUserId())
		if err != nil {
			return err
		}
		if err := l.conversations.RemoveMember(ctx, userID, conversationID, targetID); err != nil {
			return err
		}
		resp.Body = &pb.Response_RemoveMember{RemoveMember: &pb.RemoveMemberResponse{}}
	case *pb.Request_LeaveGroup:
		conversationID, err := parseConversationID(r.LeaveGroup.GetConversationId())
		if err != nil {
			return err
		}
		if err := l.conversations.Leave(ctx, userID, conversationID); err != nil {
			return err
		}
		resp.Body = &pb.Response_LeaveGroup{LeaveGroup: &pb.LeaveGroupResponse{}}
	case *pb.Request_SetTopic:
		conversationID, err := parseConversationID(r.SetTopic.GetConversationId())
		if err != nil {
			return err
		}
		if err := l.conversations.SetTopic(ctx, userID, conversationID, r.SetTopic.GetTopic()); err != nil {
			return err
		}
		resp.Body = &pb.Response_SetTopic{SetTopic: &pb.SetTopicResponse{}}
	case *pb.Request_SetAdmin:
		conversationID, targetID, err := parseMemberIDs(r.SetAdmin.GetConversationId(), r.SetAdmin.GetUserId())
		if err != nil {
			return err
		}
		if err := l.conversations.SetAdmin(ctx, userID, conversationID, targetID, r.SetAdmin.GetAdmin()); err != nil {
			return err
		}
		resp.Body = &pb.Response_SetAdmin{SetAdmin: &pb.SetAdminResponse{}}
	case *pb.Request_GetMembers:
		conversationID, err := parseConversationID(r.GetMembers.GetConversationId())
		if err != nil {
			return err
		}
		members, err := l.conversations.Members(ctx, userID, conversationID)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_GetMembers{GetMembers: &pb.GetMembersResponse{Members: members}}
	case *pb.Request_SendMessage:
		conversationID, err := parseConversationID(r.SendMessage.GetConversationId())
		if err != nil {
			return err
		}
		msg, err := l.conversations.Send(ctx, userID, conversationID,
			r.SendMessage.GetClientMessageId(), r.SendMessage.GetSealed(), r.SendMessage.GetBlobIds())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_SendMessage{SendMessage: &pb.SendMessageResponse{Message: msg}}
	case *pb.Request_DeleteMessage:
		conversationID, err := parseConversationID(r.DeleteMessage.GetConversationId())
		if err != nil {
			return err
		}
		if err := l.conversations.Delete(ctx, userID, conversationID, r.DeleteMessage.GetSeq()); err != nil {
			return err
		}
		resp.Body = &pb.Response_DeleteMessage{DeleteMessage: &pb.DeleteMessageResponse{}}
	case *pb.Request_SetReaction:
		conversationID, err := parseConversationID(r.SetReaction.GetConversationId())
		if err != nil {
			return err
		}
		if err := l.conversations.SetReaction(ctx, userID, conversationID,
			r.SetReaction.GetSeq(), r.SetReaction.GetSealed()); err != nil {
			return err
		}
		resp.Body = &pb.Response_SetReaction{SetReaction: &pb.SetReactionResponse{}}
	case *pb.Request_MarkRead:
		conversationID, err := parseConversationID(r.MarkRead.GetConversationId())
		if err != nil {
			return err
		}
		if err := l.conversations.MarkRead(ctx, userID, conversationID, r.MarkRead.GetSeq()); err != nil {
			return err
		}
		resp.Body = &pb.Response_MarkRead{MarkRead: &pb.MarkReadResponse{}}
	case *pb.Request_Sync:
		sync, err := l.users.Sync(ctx, userID, r.Sync.GetCursor(), r.Sync.GetLimit())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_Sync{Sync: sync}
	case *pb.Request_ListConversations:
		list, err := l.users.ListConversations(ctx, userID)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_ListConversations{ListConversations: &pb.ListConversationsResponse{Conversations: list}}
	case *pb.Request_GetMessages:
		conversationID, err := parseConversationID(r.GetMessages.GetConversationId())
		if err != nil {
			return err
		}
		msgs, err := l.conversations.Messages(ctx, userID, conversationID,
			r.GetMessages.GetBeforeSeq(), r.GetMessages.GetLimit())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_GetMessages{GetMessages: &pb.GetMessagesResponse{Messages: msgs}}
	case *pb.Request_SetDeviceKey:
		if err := l.accounts.SetDeviceKey(ctx, userID, deviceID, r.SetDeviceKey.GetPublicKey()); err != nil {
			return err
		}
		resp.Body = &pb.Response_SetDeviceKey{SetDeviceKey: &pb.SetDeviceKeyResponse{}}
	case *pb.Request_GetDeviceKeys:
		users, err := l.accounts.DeviceKeys(ctx, r.GetDeviceKeys.GetUserIds())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_GetDeviceKeys{GetDeviceKeys: &pb.GetDeviceKeysResponse{Users: users}}
	case *pb.Request_PublishKeys:
		conversationID, err := parseConversationID(r.PublishKeys.GetConversationId())
		if err != nil {
			return err
		}
		if err := l.conversations.PublishKeys(ctx, userID, deviceID, conversationID,
			r.PublishKeys.GetEpoch(), r.PublishKeys.GetKeys()); err != nil {
			return err
		}
		resp.Body = &pb.Response_PublishKeys{PublishKeys: &pb.PublishKeysResponse{}}
	case *pb.Request_GetConversationKeys:
		conversationID, err := parseConversationID(r.GetConversationKeys.GetConversationId())
		if err != nil {
			return err
		}
		keys, err := l.conversations.Keys(ctx, userID, deviceID, conversationID, r.GetConversationKeys.GetAfterEpoch())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_GetConversationKeys{GetConversationKeys: keys}
	case *pb.Request_RegisterPushToken:
		if err := l.users.RegisterPushToken(ctx, userID, deviceID,
			r.RegisterPushToken.GetService(), r.RegisterPushToken.GetToken()); err != nil {
			return err
		}
		resp.Body = &pb.Response_RegisterPushToken{RegisterPushToken: &pb.RegisterPushTokenResponse{}}
	default:
		return apierror.InvalidArgument("unknown request")
	}
	return nil
}

// signIn handles the requests allowed before Connect.
func (l *Local) signIn(ctx context.Context, req *pb.Request, resp *pb.Response) error {
	switch r := req.GetBody().(type) {
	case *pb.Request_Register:
		creds, err := l.accounts.Register(ctx, r.Register)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_Register{Register: &pb.RegisterResponse{Credentials: creds}}
	case *pb.Request_SignIn:
		creds, err := l.accounts.SignIn(ctx, r.SignIn)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_SignIn{SignIn: &pb.SignInResponse{Credentials: creds}}
	case *pb.Request_Connect:
		connected, err := l.accounts.Connect(ctx, r.Connect)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_Connect{Connect: connected}
	}
	return nil
}

func parseConversationID(b []byte) (ids.UUID, error) {
	id, ok := ids.Parse(b)
	if !ok {
		return id, apierror.InvalidArgument("conversation_id must be 16 bytes")
	}
	return id, nil
}

func parseMemberIDs(conversation, user []byte) (ids.UUID, ids.UUID, error) {
	conversationID, err := parseConversationID(conversation)
	if err != nil {
		return conversationID, ids.UUID{}, err
	}
	userID, ok := ids.Parse(user)
	if !ok {
		return conversationID, userID, apierror.InvalidArgument("user_id must be 16 bytes")
	}
	return conversationID, userID, nil
}
