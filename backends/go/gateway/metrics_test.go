package gateway

import (
	"testing"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestRequestType(t *testing.T) {
	for req, want := range map[*pb.Request]string{
		{}: "unknown",
		{Body: &pb.Request_Connect{Connect: &pb.ConnectRequest{}}}:                               "connect",
		{Body: &pb.Request_OpenConversation{OpenConversation: &pb.OpenConversationRequest{}}}:    "open_conversation",
		{Body: &pb.Request_SendMessage{SendMessage: &pb.SendMessageRequest{}}}:                   "send_message",
		{Body: &pb.Request_MarkRead{MarkRead: &pb.MarkReadRequest{}}}:                            "mark_read",
		{Body: &pb.Request_Sync{Sync: &pb.SyncRequest{}}}:                                        "sync",
		{Body: &pb.Request_ListConversations{ListConversations: &pb.ListConversationsRequest{}}}: "list_conversations",
		{Body: &pb.Request_GetMessages{GetMessages: &pb.GetMessagesRequest{}}}:                   "get_messages",
		{Body: &pb.Request_RegisterPushToken{RegisterPushToken: &pb.RegisterPushTokenRequest{}}}: "register_push_token",
	} {
		if got := requestType(req); got != want {
			t.Errorf("requestType(%v) = %q, want %q", req, got, want)
		}
	}
}
