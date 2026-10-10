// Package ids derives the identifiers the backend uses. Ids fit in 63 bits
// so they survive signed 64-bit handling anywhere along the way.
package ids

import (
	"crypto/rand"
	"encoding/binary"

	"github.com/cespare/xxhash/v2"
)

const mask = 1<<63 - 1

// User derives a user id from the user's name, so sign-in needs no lookup.
func User(name string) uint64 {
	return nonZero(xxhash.Sum64String(name) & mask)
}

// Conversation derives the id of the conversation between two users; the
// order of the arguments does not matter.
func Conversation(a, b uint64) uint64 {
	a, b = Participants(a, b)
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[:8], a)
	binary.BigEndian.PutUint64(buf[8:], b)
	return nonZero(xxhash.Sum64(buf[:]) & mask)
}

// Participants orders two user ids as stored: user_a < user_b.
func Participants(a, b uint64) (uint64, uint64) {
	if a > b {
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
	return nonZero(binary.BigEndian.Uint64(buf[:]) & mask)
}

func nonZero(id uint64) uint64 {
	if id == 0 {
		return 1
	}
	return id
}
