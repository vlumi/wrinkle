package conformance

import (
	"strings"
	"testing"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func deviceEvent(t *testing.T, d *device, deviceID uint64, match func(*pb.DeviceEvent) bool) *pb.DeviceEvent {
	t.Helper()
	ev := nextEvent(t, d, func(ev *pb.Event) bool {
		e := ev.GetDevice()
		return e != nil && e.GetDevice().GetDeviceId() == deviceID && match(e)
	})
	return ev.GetDevice()
}

func deviceIDs(t *testing.T, d *device) []uint64 {
	t.Helper()
	list, err := d.ListDevices(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]uint64, len(list))
	for i, dev := range list {
		ids[i] = dev.GetDeviceId()
	}
	return ids
}

func TestListAndRenameDevices(t *testing.T) {
	d := newUser(t, "devices")
	d2 := otherDevice(t, d)

	signedIn := deviceEvent(t, d, d2.deviceID, func(e *pb.DeviceEvent) bool { return !e.GetSignedOut() })
	if signedIn.GetDevice().GetName() != "conformance" || signedIn.GetDevice().GetCreatedAt() == nil {
		t.Fatalf("sign-in event %v", signedIn)
	}
	list, err := d.ListDevices(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].GetDeviceId() != d.deviceID || list[1].GetDeviceId() != d2.deviceID {
		t.Fatalf("devices %v, want %d then %d", list, d.deviceID, d2.deviceID)
	}
	for _, dev := range list {
		if dev.GetName() != "conformance" || dev.GetPush() || dev.GetCreatedAt() == nil || dev.GetLastSeenAt() == nil {
			t.Fatalf("device %v", dev)
		}
	}

	renamed, err := d.RenameDevice(ctx(t), d2.deviceID, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.GetDeviceId() != d2.deviceID || renamed.GetName() != "phone" {
		t.Fatalf("renamed %v", renamed)
	}
	deviceEvent(t, d, d2.deviceID, func(e *pb.DeviceEvent) bool { return e.GetDevice().GetName() == "phone" && !e.GetSignedOut() })
	deviceEvent(t, d2, d2.deviceID, func(e *pb.DeviceEvent) bool { return e.GetDevice().GetName() == "phone" })
	// The own device too, and an empty name is a name.
	if dev, err := d.RenameDevice(ctx(t), d.deviceID, ""); err != nil || dev.GetName() != "" {
		t.Fatalf("rename own device: %v %v", dev, err)
	}
	_, err = d.RenameDevice(ctx(t), d.deviceID, strings.Repeat("n", 65))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	other := newUser(t, "other")
	_, err = other.RenameDevice(ctx(t), d.deviceID, "mine now")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	if ids := deviceIDs(t, other); len(ids) != 1 || ids[0] != other.deviceID {
		t.Fatalf("other's devices %v", ids)
	}
}

func TestSignOutDevice(t *testing.T) {
	d := newUser(t, "remote")
	d2 := otherDevice(t, d)
	deviceEvent(t, d, d2.deviceID, func(e *pb.DeviceEvent) bool { return !e.GetSignedOut() })

	err := d.SignOutDevice(ctx(t), d.deviceID)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	other := newUser(t, "other")
	err = other.SignOutDevice(ctx(t), d2.deviceID)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)

	if err := d.SignOutDevice(ctx(t), d2.deviceID); err != nil {
		t.Fatal(err)
	}
	waitSignedOut(t, d2)
	_, err = dial(t).Connect(ctx(t), d2.token)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
	deviceEvent(t, d, d2.deviceID, func(e *pb.DeviceEvent) bool { return e.GetSignedOut() })
	if ids := deviceIDs(t, d); len(ids) != 1 || ids[0] != d.deviceID {
		t.Fatalf("devices after signing out the other: %v", ids)
	}
	err = d.SignOutDevice(ctx(t), d2.deviceID)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	_, err = d.RenameDevice(ctx(t), d2.deviceID, "gone")
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_NOT_FOUND)
	// The remaining device is still connected.
	if _, err := d.ListConversations(ctx(t)); err != nil {
		t.Fatal(err)
	}
}

func TestChangePasswordClosesOtherDevices(t *testing.T) {
	d := newUser(t, "pwclose")
	d2 := otherDevice(t, d)
	d3 := otherDevice(t, d)
	if err := d.ChangePassword(ctx(t), password, "another password"); err != nil {
		t.Fatal(err)
	}
	waitSignedOut(t, d2)
	waitSignedOut(t, d3)
	// One event each, in any order.
	pending := map[uint64]bool{d2.deviceID: true, d3.deviceID: true}
	for len(pending) > 0 {
		nextEvent(t, d, func(ev *pb.Event) bool {
			e := ev.GetDevice()
			if e.GetSignedOut() && pending[e.GetDevice().GetDeviceId()] {
				delete(pending, e.GetDevice().GetDeviceId())
				return true
			}
			return false
		})
	}
	if ids := deviceIDs(t, d); len(ids) != 1 || ids[0] != d.deviceID {
		t.Fatalf("devices after the change: %v", ids)
	}
}
