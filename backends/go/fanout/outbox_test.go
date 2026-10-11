package fanout

import (
	"encoding/base64"
	"testing"
)

// Records as Debezium wrote them, binary columns in base64.

func TestParseOutboxRow(t *testing.T) {
	value := []byte(`{"conversation_id":"C4X3FkaURgebCmaEBLUfTA==","outbox_id":"6453981837480687173","user_a":"QdU4zkeJR2KIGb3J8piNBQ==","user_b":"ybnQII5KToqNOikjwBiS0A==","kind":"message","payload":"EgwIjaCu1gYQmKXepgNSQQo/ChALhfcWRpRGB5sKZoQEtR9MEAEaEEHVOM5HiUdiiBm9yfKYjQUiAW0qBmZyb20gYjIMCI2grtYGEJil3qYD","created_at":1791725581886543}`)
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
	if o.peerOf(o.userA) != o.userB || o.peerOf(o.userB) != o.userA {
		t.Fatal("peers")
	}
	msg := o.event.GetMessage().GetMessage()
	if msg.GetSeq() != 1 || msg.GetText() != "from b" || string(msg.GetConversationId()) != string(o.conversationID[:]) {
		t.Fatalf("event %v", o.event)
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
