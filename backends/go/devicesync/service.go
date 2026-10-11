// Package devicesync is the sync service: sign-in and devices, the user's
// event log, and the conversation list, against the user keyspace.
package devicesync

import (
	"database/sql"
	"time"
	"unicode/utf8"

	"github.com/vlumi/wrinkle/backends/go/conversation"
)

const maxNameLength = 64

type Service struct {
	db            *sql.DB
	conversations *conversation.Service
	now           func() time.Time
}

func New(db *sql.DB, conversations *conversation.Service) *Service {
	return &Service{db: db, conversations: conversations, now: func() time.Time { return time.Now().UTC() }}
}

func validName(name string) bool {
	return name != "" && utf8.ValidString(name) && utf8.RuneCountInString(name) <= maxNameLength
}
