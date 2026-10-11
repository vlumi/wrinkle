package gateway

import (
	"errors"
	"slices"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	backendv1 "github.com/vlumi/wrinkle/proto/go/wrinkle/backend/v1"
)

// statusSignedOut closes the connections of a device signed out.
const statusSignedOut websocket.StatusCode = 4001

// Field numbers of Push, read without decoding the event it carries.
var (
	pushEvent     = (&backendv1.Push{}).ProtoReflect().Descriptor().Fields().ByName("event").Number()
	pushSignedOut = (&backendv1.Push{}).ProtoReflect().Descriptor().Fields().ByName("signed_out").Number()
)

var errMalformedPush = errors.New("malformed push")

// decodePush splits a push into the serialized event it carries, aliasing
// the payload, or the devices signed out. Of repeated fields of the oneof,
// the last wins, as in any protobuf decoder.
func decodePush(payload []byte) (event []byte, signedOut *backendv1.SignedOut, err error) {
	for len(payload) > 0 {
		num, typ, n := protowire.ConsumeTag(payload)
		if n < 0 {
			return nil, nil, errMalformedPush
		}
		payload = payload[n:]
		if typ == protowire.BytesType && (num == pushEvent || num == pushSignedOut) {
			value, m := protowire.ConsumeBytes(payload)
			if m < 0 {
				return nil, nil, errMalformedPush
			}
			payload = payload[m:]
			if num == pushEvent {
				event, signedOut = value, nil
				continue
			}
			signedOut, event = &backendv1.SignedOut{}, nil
			if err := proto.Unmarshal(value, signedOut); err != nil {
				return nil, nil, errMalformedPush
			}
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, payload)
		if m < 0 {
			return nil, nil, errMalformedPush
		}
		payload = payload[m:]
	}
	if event == nil && signedOut == nil {
		return nil, nil, errMalformedPush
	}
	return event, signedOut, nil
}

// deliver hands a push on the user's channel to the user's connections
// here: an event is sent as it is, a sign-out closes the connections of
// the devices it names.
func (s *Server) deliver(userID ids.UUID, payload []byte) {
	event, signedOut, err := decodePush(payload)
	if err != nil {
		s.log.Warn("dropping push", "user", userID, "err", err)
		return
	}
	conns := s.registry.conns(userID)
	if signedOut == nil {
		for _, c := range conns {
			c.pushEvent(event)
		}
		return
	}
	for _, c := range conns {
		_, deviceID, _ := c.identity()
		if signedOut.GetAllDevices() || slices.Contains(signedOut.GetDeviceIds(), deviceID) {
			c.close(statusSignedOut, "signed out")
		}
	}
}
