// Package ids holds the identifiers the backend uses: random UUIDs for users
// and conversations, and random 63-bit ids for devices and outbox rows.
// The 63 bits survive signed 64-bit handling anywhere along the way.
package ids

import (
	"bytes"
	"crypto/rand"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// UUID is a user or conversation id: a version 4 UUID, 16 bytes. The zero
// value is no id. It is stored as BINARY(16) and travels as bytes.
type UUID [16]byte

// New returns a random version 4 UUID.
func New() UUID {
	var u UUID
	if _, err := rand.Read(u[:]); err != nil {
		panic(err)
	}
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return u
}

// Parse takes an id from the wire; it must be exactly 16 bytes.
func Parse(b []byte) (UUID, bool) {
	var u UUID
	if len(b) != len(u) {
		return u, false
	}
	copy(u[:], b)
	return u, true
}

// FromHex parses the lowercase hex form String returns.
func FromHex(s string) (UUID, bool) {
	var u UUID
	if len(s) != 2*len(u) {
		return u, false
	}
	if _, err := hex.Decode(u[:], []byte(s)); err != nil {
		return u, false
	}
	return u, u.String() == s
}

func (u UUID) IsZero() bool { return u == UUID{} }

// Bytes returns the id for the wire; the zero id is empty.
func (u UUID) Bytes() []byte {
	if u.IsZero() {
		return nil
	}
	return bytes.Clone(u[:])
}

// String is the lowercase hex of the 16 bytes, as Valkey keys name users.
func (u UUID) String() string { return hex.EncodeToString(u[:]) }

// Value stores the id as BINARY(16).
func (u UUID) Value() (driver.Value, error) { return bytes.Clone(u[:]), nil }

// Scan reads a BINARY(16) column.
func (u *UUID) Scan(src any) error {
	b, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("uuid from %T", src)
	}
	if len(b) != len(u) {
		return fmt.Errorf("uuid of %d bytes", len(b))
	}
	copy(u[:], b)
	return nil
}

// Participants orders two user ids as stored: user_a < user_b, bytewise.
func Participants(a, b UUID) (UUID, UUID) {
	if bytes.Compare(a[:], b[:]) > 0 {
		return b, a
	}
	return a, b
}

// Random returns a random id, for devices and outbox rows.
func Random() uint64 {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err)
	}
	if id := binary.BigEndian.Uint64(buf[:]) & (1<<63 - 1); id != 0 {
		return id
	}
	return 1
}
