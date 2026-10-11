// Package push notifies devices that are not connected, through APNs. The
// fan-out worker calls the notifier after appending a message to a user's
// log; devices that are connected get the event over their WebSocket
// instead.
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"
)

// APNs endpoints; the tests point both at a local server.
const (
	ProductionURL = "https://api.push.apple.com"
	SandboxURL    = "https://api.sandbox.push.apple.com"
)

// APNsConfig is a token-based provider: a .p8 key from the developer
// account, its key id, the team id, and the app's bundle id as the topic.
// Without a key id, the one in the key's file name is used.
type APNsConfig struct {
	KeyFile       string
	KeyID         string
	TeamID        string
	Topic         string
	ProductionURL string
	SandboxURL    string
	HTTPClient    *http.Client
}

// ErrUnregistered means the token is no longer valid for the topic; the
// registration should be removed.
var ErrUnregistered = errors.New("device token is no longer registered")

type APNs struct {
	cfg  APNsConfig
	key  *ecdsa.PrivateKey
	http *http.Client

	mu        sync.Mutex
	token     string
	tokenTime time.Time
}

// Apple accepts a provider token for an hour and rejects one refreshed more
// often than every 20 minutes.
const tokenLifetime = 45 * time.Minute

func NewAPNs(cfg APNsConfig) (*APNs, error) {
	pemBytes, err := os.ReadFile(cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("apns key: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("apns key: not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apns key: not an EC key")
	}
	if cfg.KeyID == "" {
		cfg.KeyID = keyIDFromFileName(cfg.KeyFile)
	}
	if cfg.KeyID == "" || cfg.TeamID == "" || cfg.Topic == "" {
		return nil, errors.New("apns: key id, team id and topic are all required")
	}
	if cfg.ProductionURL == "" {
		cfg.ProductionURL = ProductionURL
	}
	if cfg.SandboxURL == "" {
		cfg.SandboxURL = SandboxURL
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &APNs{cfg: cfg, key: key, http: client}, nil
}

var keyIDInFileName = regexp.MustCompile(`AuthKey_([A-Z0-9]{10})\.p8$`)

// keyIDFromFileName reads the key id from the name Apple gives the
// download, AuthKey_<id>.p8.
func keyIDFromFileName(path string) string {
	if m := keyIDInFileName.FindStringSubmatch(path); m != nil {
		return m[1]
	}
	return ""
}

// providerToken returns the current JWT, signing a new one when it is old.
func (a *APNs) providerToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Since(a.tokenTime) < tokenLifetime {
		return a.token, nil
	}
	now := time.Now()
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": a.cfg.KeyID})
	claims, _ := json.Marshal(map[string]any{"iss": a.cfg.TeamID, "iat": now.Unix()})
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, a.key, digest[:])
	if err != nil {
		return "", err
	}
	// JWS wants the raw 64-byte r || s, not ASN.1.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	a.token = signingInput + "." + enc.EncodeToString(sig)
	a.tokenTime = now
	return a.token, nil
}

// Alert is what a notification shows, or, when Background, a silent
// notification that wakes the app to act on Data.
type Alert struct {
	Title      string
	Body       string
	Badge      int
	ThreadID   string
	Background bool
	// The app's notification service extension may change the alert
	// before it shows, as it does to decrypt the message.
	MutableContent bool
	// Custom keys beside "aps", for the app to act on when the alert is
	// tapped or the app woken.
	Data map[string]any
}

// maxPayload is the most a notification's JSON may have.
const maxPayload = 4096

// payload is the notification's JSON.
func (alert Alert) payload() ([]byte, error) {
	aps := map[string]any{
		"alert":     map[string]string{"title": alert.Title, "body": alert.Body},
		"badge":     alert.Badge,
		"sound":     "default",
		"thread-id": alert.ThreadID,
	}
	if alert.MutableContent {
		aps["mutable-content"] = 1
	}
	if alert.Background {
		aps = map[string]any{"content-available": 1}
	}
	payload := map[string]any{"aps": aps}
	for k, v := range alert.Data {
		payload[k] = v
	}
	return json.Marshal(payload)
}

// Send delivers one alert to one device token.
func (a *APNs) Send(ctx context.Context, sandbox bool, deviceToken string, alert Alert) error {
	jwt, err := a.providerToken()
	if err != nil {
		return err
	}
	body, err := alert.payload()
	if err != nil {
		return err
	}
	pushType, priority := "alert", "10"
	if alert.Background {
		// Apple requires the low priority for background notifications.
		pushType, priority = "background", "5"
	}
	base := a.cfg.ProductionURL
	if sandbox {
		base = a.cfg.SandboxURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/3/device/"+deviceToken, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", a.cfg.Topic)
	req.Header.Set("apns-push-type", pushType)
	req.Header.Set("apns-priority", priority)
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var reason struct {
		Reason string `json:"reason"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = json.Unmarshal(raw, &reason)
	switch {
	case resp.StatusCode == http.StatusGone,
		reason.Reason == "BadDeviceToken",
		reason.Reason == "DeviceTokenNotForTopic":
		return fmt.Errorf("%w: %s", ErrUnregistered, reason.Reason)
	case reason.Reason == "ExpiredProviderToken":
		a.mu.Lock()
		a.token = ""
		a.mu.Unlock()
	}
	return fmt.Errorf("apns: %d %s", resp.StatusCode, reason.Reason)
}
