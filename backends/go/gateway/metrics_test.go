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
		{Body: &pb.Request_CreateGroup{CreateGroup: &pb.CreateGroupRequest{}}}:                   "create_group",
		{Body: &pb.Request_AddMembers{AddMembers: &pb.AddMembersRequest{}}}:                      "add_members",
		{Body: &pb.Request_RemoveMember{RemoveMember: &pb.RemoveMemberRequest{}}}:                "remove_member",
		{Body: &pb.Request_LeaveGroup{LeaveGroup: &pb.LeaveGroupRequest{}}}:                      "leave_group",
		{Body: &pb.Request_SetTopic{SetTopic: &pb.SetTopicRequest{}}}:                            "set_topic",
		{Body: &pb.Request_SetAdmin{SetAdmin: &pb.SetAdminRequest{}}}:                            "set_admin",
		{Body: &pb.Request_GetMembers{GetMembers: &pb.GetMembersRequest{}}}:                      "get_members",
	} {
		if got := requestType(req); got != want {
			t.Errorf("requestType(%v) = %q, want %q", req, got, want)
		}
	}
}
