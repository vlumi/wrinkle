package accounts

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters for new hashes; verification reads them from the
// stored string, so they can change.
const (
	argonMemory  = 19456
	argonTime    = 2
	argonThreads = 1
	argonSaltLen = 16
	argonKeyLen  = 32
)

var errMalformedHash = errors.New("malformed password hash")

// hashSlots bounds concurrent hashing: each takes about 19 MiB and a core
// for tens of milliseconds, and a burst of sign-ins must not exhaust memory.
var hashSlots = make(chan struct{}, runtime.GOMAXPROCS(0))

func acquireHashSlot(ctx context.Context) error {
	select {
	case hashSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseHashSlot() { <-hashSlots }

// hashPassword returns the argon2id hash of the password in the PHC string
// format.
func hashPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if err := acquireHashSlot(ctx); err != nil {
		return "", err
	}
	defer releaseHashSlot()
	return encodeHash(password, salt, argonMemory, argonTime, argonThreads), nil
}

func encodeHash(password string, salt []byte, memory, time uint32, threads uint8) string {
	key := argon2.IDKey([]byte(password), salt, time, memory, threads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memory, time, threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// verifyPassword reports whether the password matches the PHC string.
func verifyPassword(ctx context.Context, password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false, errMalformedHash
	}
	var memory, time uint32
	var threads uint8
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil || n != 3 ||
		fmt.Sprintf("m=%d,t=%d,p=%d", memory, time, threads) != parts[3] {
		return false, errMalformedHash
	}
	// Bounds against a corrupt row costing unbounded memory or time.
	if memory < 8 || memory > 1<<21 || time < 1 || time > 16 || threads < 1 {
		return false, errMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errMalformedHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, errMalformedHash
	}
	if err := acquireHashSlot(ctx); err != nil {
		return false, err
	}
	defer releaseHashSlot()
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
