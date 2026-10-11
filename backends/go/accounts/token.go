package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
)

// A device token is "v1." and the unpadded base64url of the user id, the
// device id as 8 bytes big-endian and a 32-byte secret. The device row keeps
// only the SHA-256 of the secret.
const (
	tokenPrefix    = "v1."
	tokenSecretLen = 32
	tokenLen       = 16 + 8 + tokenSecretLen
)

type deviceToken struct {
	userID   ids.UUID
	deviceID uint64
	secret   []byte
}

// newToken makes a token for the device with a fresh secret.
func newToken(userID ids.UUID, deviceID uint64) deviceToken {
	secret := make([]byte, tokenSecretLen)
	if _, err := rand.Read(secret); err != nil {
		panic(err)
	}
	return deviceToken{userID: userID, deviceID: deviceID, secret: secret}
}

func (t deviceToken) String() string {
	raw := make([]byte, 0, tokenLen)
	raw = append(raw, t.userID[:]...)
	raw = binary.BigEndian.AppendUint64(raw, t.deviceID)
	raw = append(raw, t.secret...)
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// hash is what the device row stores.
func (t deviceToken) hash() []byte {
	sum := sha256.Sum256(t.secret)
	return sum[:]
}

func parseToken(s string) (deviceToken, bool) {
	encoded, ok := strings.CutPrefix(s, tokenPrefix)
	if !ok {
		return deviceToken{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != tokenLen {
		return deviceToken{}, false
	}
	var t deviceToken
	copy(t.userID[:], raw[:16])
	t.deviceID = binary.BigEndian.Uint64(raw[16:24])
	t.secret = raw[24:]
	return t, true
}
