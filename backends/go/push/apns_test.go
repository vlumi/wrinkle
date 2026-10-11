package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "AuthKey.p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return key, path
}

// http2Server is a TLS server speaking HTTP/2, as APNs requires.
func http2Server(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// verifyJWT checks the provider token's signature and claims.
func verifyJWT(t *testing.T, token string, pub *ecdsa.PublicKey) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("bad signature encoding: %v (%d bytes)", err, len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		t.Fatal("signature does not verify")
	}
	var header map[string]string
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	_ = json.Unmarshal(raw, &header)
	if header["alg"] != "ES256" || header["kid"] != "KEY123" {
		t.Fatalf("header: %v", header)
	}
	var claims map[string]any
	raw, _ = base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(raw, &claims)
	if claims["iss"] != "TEAM456" {
		t.Fatalf("claims: %v", claims)
	}
}

func TestSendSignsAndAddressesTheRequest(t *testing.T) {
	key, path := testKey(t)
	var got *http.Request
	var body map[string]any
	srv := http2Server(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
	})

	a, err := NewAPNs(APNsConfig{
		KeyFile: path, KeyID: "KEY123", TeamID: "TEAM456", Topic: "fi.example.app",
		ProductionURL: srv.URL + "/prod", SandboxURL: srv.URL, HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = a.Send(context.Background(), true, "abc123", Alert{
		Title: "bob", Body: "hello", Badge: 3, ThreadID: "42", Data: map[string]any{"conversation_id": "42", "seq": uint64(7)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/3/device/abc123" {
		t.Fatalf("path %q: the sandbox URL should be used", got.URL.Path)
	}
	if got.ProtoMajor != 2 {
		t.Fatalf("APNs requires HTTP/2, got %s", got.Proto)
	}
	for k, want := range map[string]string{"apns-topic": "fi.example.app", "apns-push-type": "alert", "apns-priority": "10"} {
		if got.Header.Get(k) != want {
			t.Fatalf("%s = %q, want %q", k, got.Header.Get(k), want)
		}
	}
	verifyJWT(t, strings.TrimPrefix(got.Header.Get("authorization"), "bearer "), &key.PublicKey)
	aps := body["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	if alert["title"] != "bob" || alert["body"] != "hello" || aps["badge"].(float64) != 3 || aps["thread-id"] != "42" {
		t.Fatalf("payload: %v", body)
	}
	if body["conversation_id"] != "42" || body["seq"] != float64(7) {
		t.Fatalf("custom data missing: %v", body)
	}
}

func TestSendBackground(t *testing.T) {
	_, path := testKey(t)
	var got *http.Request
	var body map[string]any
	srv := http2Server(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusOK)
	})
	a, err := NewAPNs(APNsConfig{KeyFile: path, KeyID: "KEY123", TeamID: "TEAM456", Topic: "t",
		ProductionURL: srv.URL, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	err = a.Send(context.Background(), false, "tok", Alert{
		Background: true, Data: map[string]any{"conversation_id": "42", "seq": uint64(7), "deleted": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("apns-push-type") != "background" || got.Header.Get("apns-priority") != "5" {
		t.Fatalf("headers %v", got.Header)
	}
	aps := body["aps"].(map[string]any)
	if len(aps) != 1 || aps["content-available"] != float64(1) {
		t.Fatalf("aps %v", aps)
	}
	if body["conversation_id"] != "42" || body["seq"] != float64(7) || body["deleted"] != true {
		t.Fatalf("payload: %v", body)
	}
}

func TestSendReportsUnregisteredTokens(t *testing.T) {
	_, path := testKey(t)
	for _, tc := range []struct {
		status int
		reason string
	}{{http.StatusGone, "Unregistered"}, {http.StatusBadRequest, "BadDeviceToken"}} {
		srv := http2Server(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"reason":"` + tc.reason + `"}`))
		})
		a, err := NewAPNs(APNsConfig{KeyFile: path, KeyID: "KEY123", TeamID: "TEAM456", Topic: "t",
			ProductionURL: srv.URL, HTTPClient: srv.Client()})
		if err != nil {
			t.Fatal(err)
		}
		err = a.Send(context.Background(), false, "tok", Alert{Title: "x", Body: "y"})
		if !errors.Is(err, ErrUnregistered) {
			t.Fatalf("%d %s: got %v, want ErrUnregistered", tc.status, tc.reason, err)
		}
	}
}

func TestProviderTokenIsReused(t *testing.T) {
	_, path := testKey(t)
	a, err := NewAPNs(APNsConfig{KeyFile: path, KeyID: "K", TeamID: "T", Topic: "t"})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := a.providerToken()
	second, _ := a.providerToken()
	if first != second {
		t.Fatal("a fresh token was signed within its lifetime; Apple throttles that")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("こんにちは世界", 5); got != "こんにち…" {
		t.Fatalf("got %q", got)
	}
	if got := truncate("short", 10); got != "short" {
		t.Fatalf("got %q", got)
	}
}

func TestKeyIDFromFileName(t *testing.T) {
	if got := keyIDFromFileName("/x/AuthKey_ABCDE12345.p8"); got != "ABCDE12345" {
		t.Fatalf("got %q", got)
	}
	if got := keyIDFromFileName("/x/key.p8"); got != "" {
		t.Fatalf("got %q", got)
	}
}
