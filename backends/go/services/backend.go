// Package services handles a device's requests: in-process over the
// conversation and sync services, or forwarded to a services process over
// the internal RPC. The gateway owns the connection state and uses one of
// the two.
package services

import (
	"context"
	"log/slog"

	"github.com/vlumi/wrinkle/backends/go/conversation"
	"github.com/vlumi/wrinkle/backends/go/devicesync"
	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Backend handles requests for a device. Call returns an error only when
// the request could not be handled at all; a request's own failure is the
// response's error body.
type Backend interface {
	Call(ctx context.Context, userID, deviceID uint64, req *pb.Request) (*pb.Response, error)
	Ack(ctx context.Context, userID, deviceID, cursor uint64) error
}

// Local runs the services in-process.
type Local struct {
	conversations *conversation.Service
	users         *devicesync.Service
	log           *slog.Logger
}

func NewLocal(conversations *conversation.Service, users *devicesync.Service, log *slog.Logger) *Local {
	return &Local{conversations: conversations, users: users, log: log}
}

func (l *Local) Call(ctx context.Context, userID, deviceID uint64, req *pb.Request) (*pb.Response, error) {
	resp := &pb.Response{RequestId: req.GetId()}
	if err := l.dispatch(ctx, userID, deviceID, req, resp); err != nil {
		if apierror.Code(err) == pb.ErrorCode_ERROR_CODE_INTERNAL {
			l.log.Error("request failed", "request", req.GetId(), "err", err)
		}
		resp.Body = &pb.Response_Error{Error: apierror.Proto(err)}
	}
	return resp, nil
}

func (l *Local) Ack(ctx context.Context, userID, deviceID, cursor uint64) error {
	return l.users.Ack(ctx, userID, deviceID, cursor)
}

// dispatch handles one request, setting the response body on success.
func (l *Local) dispatch(ctx context.Context, userID, deviceID uint64, req *pb.Request, resp *pb.Response) error {
	if connect := req.GetConnect(); connect != nil {
		connected, err := l.users.Connect(ctx, connect)
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_Connect{Connect: connected}
		return nil
	}
	if userID == 0 {
		return apierror.Unauthenticated("connect first")
	}
	switch r := req.GetBody().(type) {
	case *pb.Request_OpenConversation:
		conv, err := l.users.Open(ctx, userID, r.OpenConversation.GetPeerUserName())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_OpenConversation{OpenConversation: &pb.OpenConversationResponse{Conversation: conv}}
	case *pb.Request_SendMessage:
		msg, err := l.conversations.Send(ctx, userID, r.SendMessage.GetConversationId(),
			r.SendMessage.GetClientMessageId(), r.SendMessage.GetText())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_SendMessage{SendMessage: &pb.SendMessageResponse{Message: msg}}
	case *pb.Request_MarkRead:
		if err := l.conversations.MarkRead(ctx, userID, r.MarkRead.GetConversationId(), r.MarkRead.GetSeq()); err != nil {
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
		msgs, err := l.conversations.Messages(ctx, userID, r.GetMessages.GetConversationId(),
			r.GetMessages.GetBeforeSeq(), r.GetMessages.GetLimit())
		if err != nil {
			return err
		}
		resp.Body = &pb.Response_GetMessages{GetMessages: &pb.GetMessagesResponse{Messages: msgs}}
	case *pb.Request_RegisterPushToken:
		if err := l.users.RegisterPushToken(ctx, userID, deviceID,
			r.RegisterPushToken.GetService(), r.RegisterPushToken.GetToken()); err != nil {
			return err
		}
		resp.Body = &pb.Response_RegisterPushToken{RegisterPushToken: &pb.RegisterPushTokenResponse{}}
	default:
		return apierror.InvalidArgument("request has no body")
	}
	return nil
}
