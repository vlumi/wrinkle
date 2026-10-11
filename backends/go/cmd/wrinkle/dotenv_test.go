package main

import "testing"

func TestKeyIDFromFile(t *testing.T) {
	if got := keyIDFromFile("/x/AuthKey_ABCDE12345.p8"); got != "ABCDE12345" {
		t.Fatalf("got %q", got)
	}
	if got := keyIDFromFile("/x/key.p8"); got != "" {
		t.Fatalf("got %q", got)
	}
}
