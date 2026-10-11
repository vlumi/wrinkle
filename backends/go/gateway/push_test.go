package gateway

import (
	"bytes"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	backendv1 "github.com/vlumi/wrinkle/proto/go/wrinkle/backend/v1"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestDecodePush(t *testing.T) {
	event, _ := proto.Marshal(&pb.Event{Cursor: 7})
	data, _ := proto.Marshal(&backendv1.Push{Kind: &backendv1.Push_Event{Event: event}})
	got, signedOut, err := decodePush(data)
	if err != nil || signedOut != nil || !bytes.Equal(got, event) {
		t.Fatalf("event push: %x %v %v", got, signedOut, err)
	}

	data, _ = proto.Marshal(&backendv1.Push{Kind: &backendv1.Push_SignedOut{SignedOut: &backendv1.SignedOut{DeviceIds: []uint64{3, 5}}}})
	got, signedOut, err = decodePush(data)
	if err != nil || got != nil || !slices.Equal(signedOut.GetDeviceIds(), []uint64{3, 5}) || signedOut.GetAllDevices() {
		t.Fatalf("sign-out push: %x %v %v", got, signedOut, err)
	}

	data, _ = proto.Marshal(&backendv1.Push{Kind: &backendv1.Push_SignedOut{SignedOut: &backendv1.SignedOut{AllDevices: true}}})
	if _, signedOut, err = decodePush(data); err != nil || !signedOut.GetAllDevices() {
		t.Fatalf("all devices: %v %v", signedOut, err)
	}

	// An empty event is still an event.
	data, _ = proto.Marshal(&backendv1.Push{Kind: &backendv1.Push_Event{Event: []byte{}}})
	if got, signedOut, err = decodePush(data); err != nil || got == nil || signedOut != nil {
		t.Fatalf("empty event: %x %v %v", got, signedOut, err)
	}

	for _, bad := range [][]byte{nil, {0x0a}, {0x0a, 0x05, 0x01}, {0x12, 0x01, 0xff}, {0x18, 0x01}} {
		if _, _, err := decodePush(bad); err == nil {
			t.Errorf("decoded %x", bad)
		}
	}
}
