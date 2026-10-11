package push

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func sealedMessage(ciphertext int) *pb.Message {
	return &pb.Message{
		Seq:             7,
		SenderUserId:    bytes.Repeat([]byte{0xab}, 16),
		ClientMessageId: "m-1",
		Content: &pb.Message_Sealed{Sealed: &pb.Sealed{
			Epoch: 3, Nonce: bytes.Repeat([]byte{1}, 12), Ciphertext: bytes.Repeat([]byte{2}, ciphertext),
		}},
	}
}

func TestMessageAlertCarriesSealedContent(t *testing.T) {
	conversationID := ids.UUID{0x42}
	alert := messageAlert("bob", 2, conversationID, sealedMessage(100))
	raw, err := alert.payload()
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	aps := body["aps"].(map[string]any)
	if aps["mutable-content"] != float64(1) || aps["alert"].(map[string]any)["body"] != "New message" ||
		aps["alert"].(map[string]any)["title"] != "bob" {
		t.Fatalf("aps %v", aps)
	}
	want := map[string]any{
		"conversation_id":   conversationID.String(),
		"seq":               float64(7),
		"epoch":             float64(3),
		"nonce":             base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 12)),
		"ciphertext":        base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 100)),
		"sender_user_id":    "abababababababababababababababab",
		"client_message_id": "m-1",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
}

func TestMessageAlertLeavesOutContentThatDoesNotFit(t *testing.T) {
	alert := messageAlert("bob", 2, ids.UUID{0x42}, sealedMessage(4000))
	raw, err := alert.payload()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxPayload {
		t.Fatalf("payload of %d bytes", len(raw))
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"epoch", "nonce", "ciphertext", "sender_user_id", "client_message_id"} {
		if _, ok := body[k]; ok {
			t.Errorf("%s present", k)
		}
	}
	if body["seq"] != float64(7) || body["aps"].(map[string]any)["mutable-content"] != float64(1) {
		t.Fatalf("payload %v", body)
	}
}
