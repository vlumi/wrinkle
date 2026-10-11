package fanout

import (
	"encoding/base64"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Records as Debezium wrote them, binary columns in base64.

func TestParseOutboxRow(t *testing.T) {
	value := []byte(`{"conversation_id":"C4X3FkaURgebCmaEBLUfTA==","outbox_id":"6453981837480687173",` +
		`"recipients":"QdU4zkeJR2KIGb3J8piNBcm50CCOSk6KjTopI8AYktA=","conversation_kind":"group","topic":"Hikes","member_count":2,` +
		`"kind":"message","payload":"EgwIjaCu1gYQmKXepgNSQQo/ChALhfcWRpRGB5sKZoQEtR9MEAEaEEHVOM5HiUdiiBm9yfKYjQUiAW0qBmZyb20gYjIMCI2grtYGEJil3qYD","created_at":1791725581886543}`)
	o, err := parseOutboxRow(value)
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.StdEncoding.EncodeToString(o.conversationID[:]); got != "C4X3FkaURgebCmaEBLUfTA==" {
		t.Fatalf("conversation id %s", got)
	}
	if o.outboxID != 6453981837480687173 || o.kind != "message" {
		t.Fatalf("outbox id %d kind %s", o.outboxID, o.kind)
	}
	if o.conversationKind != "group" || o.direct() || o.topic != "Hikes" || o.memberCount != 2 {
		t.Fatalf("conversation %s %q %d", o.conversationKind, o.topic, o.memberCount)
	}
	if len(o.recipients) != 2 || base64.StdEncoding.EncodeToString(o.recipients[0][:]) != "QdU4zkeJR2KIGb3J8piNBQ==" ||
		base64.StdEncoding.EncodeToString(o.recipients[1][:]) != "ybnQII5KToqNOikjwBiS0A==" {
		t.Fatalf("recipients %v", o.recipients)
	}
	if o.peerOf(o.recipients[0]) != o.recipients[1] || o.peerOf(o.recipients[1]) != o.recipients[0] {
		t.Fatal("peers")
	}
	msg := o.event.GetMessage().GetMessage()
	if msg.GetSeq() != 1 || string(msg.GetConversationId()) != string(o.conversationID[:]) {
		t.Fatalf("event %v", o.event)
	}
	if _, err := parseOutboxRow([]byte(`{"conversation_id":"C4X3FkaURgebCmaEBLUfTA==","outbox_id":1,"recipients":"AAEC","member_count":0,"payload":""}`)); err == nil {
		t.Fatal("parsed 3 bytes of recipients")
	}
}

func TestParseOutboxRowTarget(t *testing.T) {
	payload, err := proto.Marshal(&pb.Event{Body: &pb.Event_MessageDeleted{MessageDeleted: &pb.MessageDeletedEvent{Seq: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	row := func(target string) []byte {
		return []byte(`{"conversation_id":"C4X3FkaURgebCmaEBLUfTA==","outbox_id":"2","recipients":"","conversation_kind":"direct",` +
			`"member_count":2,"kind":"message_deleted","target_outbox_id":` + target + `,"payload":"` + base64.StdEncoding.EncodeToString(payload) + `"}`)
	}
	o, err := parseOutboxRow(row(`"18446744073709551615"`))
	if err != nil {
		t.Fatal(err)
	}
	if o.targetOutboxID != 18446744073709551615 || o.event.GetMessageDeleted().GetSeq() != 3 {
		t.Fatalf("target %d event %v", o.targetOutboxID, o.event)
	}
	if o, err = parseOutboxRow(row("null")); err != nil || o.targetOutboxID != 0 {
		t.Fatalf("target of a null column: %v %v", o, err)
	}
}

func TestMembership(t *testing.T) {
	a, b, c := ids.New(), ids.New(), ids.New()
	user := func(id ids.UUID) *pb.User { return &pb.User{UserId: id.Bytes()} }
	notice := func(sender ids.UUID, n *pb.Notice) *pb.Message {
		return &pb.Message{SenderUserId: sender.Bytes(), Content: &pb.Message_Notice{Notice: n}}
	}
	left := notice(a, &pb.Notice{Kind: &pb.Notice_MemberLeft{MemberLeft: &pb.MemberLeft{Promoted: user(b)}}})
	for _, tc := range []struct {
		name          string
		user          ids.UUID
		msg           *pb.Message
		member, admin flag
	}{
		{"creator", a, notice(a, &pb.Notice{Kind: &pb.Notice_GroupCreated{GroupCreated: &pb.GroupCreated{}}}), flag{true, true}, flag{true, true}},
		{"invitee", b, notice(a, &pb.Notice{Kind: &pb.Notice_GroupCreated{GroupCreated: &pb.GroupCreated{}}}), flag{true, true}, flag{true, false}},
		{"added", c, notice(a, &pb.Notice{Kind: &pb.Notice_MembersAdded{MembersAdded: &pb.MembersAdded{Users: []*pb.User{user(b), user(c)}}}}), flag{true, true}, flag{true, false}},
		{"not added", a, notice(a, &pb.Notice{Kind: &pb.Notice_MembersAdded{MembersAdded: &pb.MembersAdded{Users: []*pb.User{user(c)}}}}), flag{}, flag{}},
		{"removed", c, notice(a, &pb.Notice{Kind: &pb.Notice_MemberRemoved{MemberRemoved: &pb.MemberRemoved{User: user(c)}}}), flag{true, false}, flag{true, false}},
		{"leaver", a, left, flag{true, false}, flag{true, false}},
		{"promoted", b, left, flag{}, flag{true, true}},
		{"bystander", c, left, flag{}, flag{}},
		{"demoted", b, notice(a, &pb.Notice{Kind: &pb.Notice_AdminChanged{AdminChanged: &pb.AdminChanged{User: user(b)}}}), flag{}, flag{true, false}},
		{"sealed", b, &pb.Message{SenderUserId: a.Bytes(), Content: &pb.Message_Sealed{Sealed: &pb.Sealed{Epoch: 1}}}, flag{}, flag{}},
	} {
		member, admin := membership(tc.user, tc.msg)
		if member != tc.member || admin != tc.admin {
			t.Errorf("%s: member %v admin %v, want %v %v", tc.name, member, admin, tc.member, tc.admin)
		}
	}
}

func TestParseUserOutboxRow(t *testing.T) {
	value := []byte(`{"user_id":"NhA4dCVFTBuu2mxC1WP1gw==","outbox_id":"1","kind":"profile","payload":"EgwIqqCu1gYQ0N6YiAFqLAoqChA2EDh0JUVMG67abELVY/WDEhFpY29uLTU3OWFiMWFjZjk3YRoDZm94","created_at":1791725610285618}`)
	o, err := parseUserOutboxRow(value)
	if err != nil {
		t.Fatal(err)
	}
	if o.outboxID != 1 || o.kind != "profile" {
		t.Fatalf("outbox id %d kind %s", o.outboxID, o.kind)
	}
	user := o.event.GetProfile().GetUser()
	if string(user.GetUserId()) != string(o.userID[:]) || user.GetName() != "icon-579ab1acf97a" || user.GetIcon() != "fox" {
		t.Fatalf("event %v", o.event)
	}
}

func TestParseRejectsShortIds(t *testing.T) {
	if _, err := parseUserOutboxRow([]byte(`{"user_id":"AAEC","outbox_id":1,"kind":"profile","payload":""}`)); err == nil {
		t.Fatal("parsed a 3-byte user id")
	}
	if _, err := parseUserOutboxRow([]byte(`{"user_id":"NhA4dCVFTBuu2mxC1WP1gw==","outbox_id":"x","kind":"profile","payload":""}`)); err == nil {
		t.Fatal("parsed a malformed outbox id")
	}
}
