package fanout

import (
	"encoding/json"
	"fmt"
	"strconv"

	"google.golang.org/protobuf/proto"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// rawOutboxRow is the outbox row as Debezium emits it after unwrapping:
// unsigned bigints as decimal strings, bytes as base64.
type rawOutboxRow struct {
	ConversationID string `json:"conversation_id"`
	OutboxID       string `json:"outbox_id"`
	UserA          string `json:"user_a"`
	UserB          string `json:"user_b"`
	Kind           string `json:"kind"`
	Payload        []byte `json:"payload"`
}

type outboxRow struct {
	conversationID, outboxID, userA, userB uint64
	kind                                   string
	event                                  *pb.Event
}

func parseOutboxRow(value []byte) (*outboxRow, error) {
	var r rawOutboxRow
	if err := json.Unmarshal(value, &r); err != nil {
		return nil, err
	}
	var o outboxRow
	var err error
	for _, f := range []struct {
		dst *uint64
		src string
	}{{&o.conversationID, r.ConversationID}, {&o.outboxID, r.OutboxID}, {&o.userA, r.UserA}, {&o.userB, r.UserB}} {
		if *f.dst, err = strconv.ParseUint(f.src, 10, 64); err != nil {
			return nil, fmt.Errorf("outbox row id %q: %w", f.src, err)
		}
	}
	o.kind = r.Kind
	o.event = &pb.Event{}
	if err := proto.Unmarshal(r.Payload, o.event); err != nil {
		return nil, fmt.Errorf("outbox payload: %w", err)
	}
	return &o, nil
}

func (o *outboxRow) peerOf(userID uint64) uint64 {
	if userID == o.userA {
		return o.userB
	}
	return o.userA
}
