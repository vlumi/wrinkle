package main

import (
	"os"
	"strings"
	"testing"
)

func TestSetUnsetFrom(t *testing.T) {
	t.Setenv("WRINKLE_TEST_SET", "kept")
	for _, key := range []string{"WRINKLE_TEST_PLAIN", "WRINKLE_TEST_EXPORTED", "WRINKLE_TEST_QUOTED"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	setUnsetFrom(strings.NewReader(`
# a comment
WRINKLE_TEST_PLAIN=plain
export WRINKLE_TEST_EXPORTED = exported
WRINKLE_TEST_QUOTED="quoted"
WRINKLE_TEST_SET=replaced
not a setting
`))
	for key, want := range map[string]string{
		"WRINKLE_TEST_PLAIN":    "plain",
		"WRINKLE_TEST_EXPORTED": "exported",
		"WRINKLE_TEST_QUOTED":   "quoted",
		"WRINKLE_TEST_SET":      "kept",
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestEnvInt(t *testing.T) {
	for value, want := range map[string]int{"": 64, "8": 8, "many": 64} {
		t.Setenv("WRINKLE_TEST_INT", value)
		if got := envInt("WRINKLE_TEST_INT", 64); got != want {
			t.Errorf("%q: got %d, want %d", value, got, want)
		}
	}
}
