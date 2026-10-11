package wrinkleclient

import (
	"context"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// CreateGroup creates a group with the named members; retrying with the
// same clientGroupID returns the group created the first time.
func (c *Client) CreateGroup(ctx context.Context, topic string, memberNames []string, clientGroupID string) (*pb.Conversation, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_CreateGroup{
		CreateGroup: &pb.CreateGroupRequest{Topic: topic, MemberNames: memberNames, ClientGroupId: clientGroupID}}})
	if err != nil {
		return nil, err
	}
	return resp.GetCreateGroup().GetConversation(), nil
}

func (c *Client) AddMembers(ctx context.Context, conversationID []byte, memberNames []string) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_AddMembers{
		AddMembers: &pb.AddMembersRequest{ConversationId: conversationID, MemberNames: memberNames}}})
	return err
}

func (c *Client) RemoveMember(ctx context.Context, conversationID, userID []byte) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_RemoveMember{
		RemoveMember: &pb.RemoveMemberRequest{ConversationId: conversationID, UserId: userID}}})
	return err
}

func (c *Client) LeaveGroup(ctx context.Context, conversationID []byte) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_LeaveGroup{
		LeaveGroup: &pb.LeaveGroupRequest{ConversationId: conversationID}}})
	return err
}

func (c *Client) SetTopic(ctx context.Context, conversationID []byte, topic string) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_SetTopic{
		SetTopic: &pb.SetTopicRequest{ConversationId: conversationID, Topic: topic}}})
	return err
}

func (c *Client) SetAdmin(ctx context.Context, conversationID, userID []byte, admin bool) error {
	_, err := c.Call(ctx, &pb.Request{Body: &pb.Request_SetAdmin{
		SetAdmin: &pb.SetAdminRequest{ConversationId: conversationID, UserId: userID, Admin: admin}}})
	return err
}

// GetMembers returns a group's current members, those who joined first
// first.
func (c *Client) GetMembers(ctx context.Context, conversationID []byte) ([]*pb.Member, error) {
	resp, err := c.Call(ctx, &pb.Request{Body: &pb.Request_GetMembers{
		GetMembers: &pb.GetMembersRequest{ConversationId: conversationID}}})
	if err != nil {
		return nil, err
	}
	return resp.GetGetMembers().GetMembers(), nil
}
