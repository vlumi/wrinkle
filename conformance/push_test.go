package conformance

import (
	"strings"
	"testing"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestRegisterPushToken(t *testing.T) {
	d := newUser(t, "push")
	token := strings.Repeat("ab", 32)
	if err := d.RegisterPushToken(ctx(t), pb.PushService_PUSH_SERVICE_APNS_SANDBOX, token); err != nil {
		t.Fatal(err)
	}
	// Registering again replaces; an empty token removes.
	if err := d.RegisterPushToken(ctx(t), pb.PushService_PUSH_SERVICE_APNS, token); err != nil {
		t.Fatal(err)
	}
	if err := d.RegisterPushToken(ctx(t), pb.PushService_PUSH_SERVICE_UNSPECIFIED, ""); err != nil {
		t.Fatal(err)
	}
	err := d.RegisterPushToken(ctx(t), pb.PushService_PUSH_SERVICE_UNSPECIFIED, token)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	err = d.RegisterPushToken(ctx(t), pb.PushService_PUSH_SERVICE_APNS, strings.Repeat("x", 201))
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	c := dial(t)
	err = c.RegisterPushToken(ctx(t), pb.PushService_PUSH_SERVICE_APNS, token)
	wantCode(t, err, pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
}
