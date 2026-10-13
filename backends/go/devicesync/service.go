// Package devicesync is the sync service: devices and their acks, the
// user's event log, and the conversation list, against the user keyspace.
package devicesync

import (
	"database/sql"

	"github.com/vlumi/wrinkle/backends/go/accounts"
	"github.com/vlumi/wrinkle/backends/go/conversation"
)

type Service struct {
	db            *sql.DB
	accounts      *accounts.Service
	conversations *conversation.Service
}

func New(db *sql.DB, accounts *accounts.Service, conversations *conversation.Service) *Service {
	return &Service{db: db, accounts: accounts, conversations: conversations}
}
