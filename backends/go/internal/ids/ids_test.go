package ids

import "testing"

func TestIdsFitIn63Bits(t *testing.T) {
	for _, id := range []uint64{User("alice"), User(""), Conversation(1, 2), Random()} {
		if id == 0 || id>>63 != 0 {
			t.Fatalf("id %d is zero or uses the top bit", id)
		}
	}
}

func TestConversationIsSymmetric(t *testing.T) {
	a, b := User("alice"), User("bob")
	if Conversation(a, b) != Conversation(b, a) {
		t.Fatal("conversation id depends on argument order")
	}
	if Conversation(a, b) == Conversation(a, User("carol")) {
		t.Fatal("different pairs share a conversation id")
	}
}
