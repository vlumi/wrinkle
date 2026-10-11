package accounts

import (
	"context"
	"strings"
	"testing"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
)

// knownHash is argon2id of "correct horse" with the salt
// "wrinkle-salt-16b" and the standard parameters. Every backend's test
// checks the same string, so each verifies what the others write.
const knownHash = "$argon2id$v=19$m=19456,t=2,p=1$d3JpbmtsZS1zYWx0LTE2Yg$C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc"

func TestKnownHash(t *testing.T) {
	if got := encodeHash("correct horse", []byte("wrinkle-salt-16b"), argonMemory, argonTime, argonThreads); got != knownHash {
		t.Fatalf("hash is %s", got)
	}
	ok, err := verifyPassword(context.Background(), "correct horse", knownHash)
	if err != nil || !ok {
		t.Fatalf("known hash does not verify: %v %v", ok, err)
	}
	if ok, _ := verifyPassword(context.Background(), "correct horse!", knownHash); ok {
		t.Fatal("wrong password verifies")
	}
}

func TestHashRoundTrip(t *testing.T) {
	hash, err := hashPassword(context.Background(), "battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected format: %s", hash)
	}
	if ok, err := verifyPassword(context.Background(), "battery staple", hash); err != nil || !ok {
		t.Fatalf("does not verify: %v %v", ok, err)
	}
	other, _ := hashPassword(context.Background(), "battery staple")
	if other == hash {
		t.Fatal("two hashes share a salt")
	}
}

func TestMalformedHash(t *testing.T) {
	for _, h := range []string{
		"",
		"$argon2i$v=19$m=19456,t=2,p=1$d3JpbmtsZS1zYWx0LTE2Yg$C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc",
		"$argon2id$v=16$m=19456,t=2,p=1$d3JpbmtsZS1zYWx0LTE2Yg$C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc",
		"$argon2id$v=19$m=19456,t=2,p=1x$d3JpbmtsZS1zYWx0LTE2Yg$C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc",
		"$argon2id$v=19$m=19456,t=2,p=1$d3JpbmtsZS1zYWx0LTE2Yg==$C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc",
		"$argon2id$v=19$m=99999999,t=2,p=1$d3JpbmtsZS1zYWx0LTE2Yg$C0bzChY3jiQgxbgv3i8xuBwSxgSCpWPcslIttuCCCCc",
	} {
		if _, err := verifyPassword(context.Background(), "correct horse", h); err == nil {
			t.Fatalf("accepted %q", h)
		}
	}
}

func TestToken(t *testing.T) {
	userID := ids.New()
	token := newToken(userID, 42)
	s := token.String()
	if !strings.HasPrefix(s, "v1.") || len(s) != 3+75 {
		t.Fatalf("token %q", s)
	}
	parsed, ok := parseToken(s)
	if !ok || parsed.userID != userID || parsed.deviceID != 42 || string(parsed.hash()) != string(token.hash()) {
		t.Fatalf("round trip gave %+v, %v", parsed, ok)
	}
	for _, bad := range []string{"", "v1.", "v2." + s[3:], s + "A", s[:len(s)-1], strings.Replace(s, "v1.", "v1.+", 1)} {
		if _, ok := parseToken(bad); ok {
			t.Fatalf("parsed %q", bad)
		}
	}
}

func TestTokenLayout(t *testing.T) {
	// user id, device id big-endian, secret: fixed bytes give a fixed token.
	token := deviceToken{userID: ids.UUID{0: 0xff, 15: 0x01}, deviceID: 0x0102030405060708, secret: make([]byte, 32)}
	want := "v1._wAAAAAAAAAAAAAAAAAAAQECAwQFBgcIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if got := token.String(); got != want {
		t.Fatalf("token %s, want %s", got, want)
	}
}

func TestValidName(t *testing.T) {
	for _, name := range []string{"a", "Bob", "Ana María", strings.Repeat("n", 64), strings.Repeat("😀", 16), "émile"} {
		if !ValidName(name) {
			t.Errorf("rejected %q", name)
		}
	}
	for _, name := range []string{"", strings.Repeat("n", 65), strings.Repeat("😀", 17), " bob", "bob ", "bob ", "　bob",
		"bo\x00b", "bo\nb", "bo\u0085b", "bo\u007fb", "\xff"} {
		if ValidName(name) {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestValidIcon(t *testing.T) {
	for _, icon := range []string{"", "cat", "unicorn"} {
		if !validIcon(icon) {
			t.Errorf("rejected %q", icon)
		}
	}
	for _, icon := range []string{"Cat", "dragon", " cat"} {
		if validIcon(icon) {
			t.Errorf("accepted %q", icon)
		}
	}
}
