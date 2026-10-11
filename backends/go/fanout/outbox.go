package fanout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Outbox rows arrive as Debezium emits them after unwrapping: binary
// columns as base64 strings, unsigned bigints as numbers or decimal strings.

// rawID is a BINARY(16) column.
type rawID []byte

func (r rawID) uuid(column string) (ids.UUID, error) {
	id, ok := ids.Parse(r)
	if !ok {
		return id, fmt.Errorf("%s of %d bytes", column, len(r))
	}
	return id, nil
}

// rawUint is a BIGINT UNSIGNED column.
type rawUint uint64

func (u *rawUint) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(b, `"`)
	n, err := strconv.ParseUint(string(b), 10, 64)
	if err != nil {
		return fmt.Errorf("unsigned bigint %s: %w", b, err)
	}
	*u = rawUint(n)
	return nil
}

// outboxRow is a row of a conversation's outbox.
type outboxRow struct {
	conversationID, userA, userB ids.UUID
	outboxID                     uint64
	kind                         string
	event                        *pb.Event
}

func parseOutboxRow(value []byte) (*outboxRow, error) {
	var r struct {
		ConversationID rawID   `json:"conversation_id"`
		OutboxID       rawUint `json:"outbox_id"`
		UserA          rawID   `json:"user_a"`
		UserB          rawID   `json:"user_b"`
		Kind           string  `json:"kind"`
		Payload        []byte  `json:"payload"`
	}
	if err := json.Unmarshal(value, &r); err != nil {
		return nil, err
	}
	o := &outboxRow{outboxID: uint64(r.OutboxID), kind: r.Kind}
	var err error
	if o.conversationID, err = r.ConversationID.uuid("conversation_id"); err != nil {
		return nil, err
	}
	if o.userA, err = r.UserA.uuid("user_a"); err != nil {
		return nil, err
	}
	if o.userB, err = r.UserB.uuid("user_b"); err != nil {
		return nil, err
	}
	if o.event, err = parseEvent(r.Payload); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *outboxRow) peerOf(userID ids.UUID) ids.UUID {
	if userID == o.userA {
		return o.userB
	}
	return o.userA
}

// userOutboxRow is a row of a user's outbox: a profile change.
type userOutboxRow struct {
	userID   ids.UUID
	outboxID uint64
	kind     string
	event    *pb.Event
}

func parseUserOutboxRow(value []byte) (*userOutboxRow, error) {
	var r struct {
		UserID   rawID   `json:"user_id"`
		OutboxID rawUint `json:"outbox_id"`
		Kind     string  `json:"kind"`
		Payload  []byte  `json:"payload"`
	}
	if err := json.Unmarshal(value, &r); err != nil {
		return nil, err
	}
	o := &userOutboxRow{outboxID: uint64(r.OutboxID), kind: r.Kind}
	var err error
	if o.userID, err = r.UserID.uuid("user_id"); err != nil {
		return nil, err
	}
	if o.event, err = parseEvent(r.Payload); err != nil {
		return nil, err
	}
	return o, nil
}

func parseEvent(payload []byte) (*pb.Event, error) {
	event := &pb.Event{}
	if err := proto.Unmarshal(payload, event); err != nil {
		return nil, fmt.Errorf("outbox payload: %w", err)
	}
	return event, nil
}
