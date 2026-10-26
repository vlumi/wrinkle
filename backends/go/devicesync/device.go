package devicesync

import (
	"context"
	"strings"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	"github.com/vlumi/wrinkle/backends/go/internal/vtdb"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const maxPushToken = 200

// Ack records how far a device has processed the log.
func (s *Service) Ack(ctx context.Context, userID ids.UUID, deviceID, cursor uint64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE device SET acked_position = GREATEST(acked_position, ?), last_seen_at = ?
		WHERE user_id = ? AND device_id = ?`,
		cursor, vtdb.Now(), userID, deviceID)
	return err
}

// RegisterPushToken records where to push for the device while it is not
// connected; an empty token removes the registration.
func (s *Service) RegisterPushToken(ctx context.Context, userID ids.UUID, deviceID uint64, service pb.PushService, token string) error {
	if token == "" {
		_, err := s.db.ExecContext(ctx, `
			UPDATE device SET push_service = NULL, push_token = NULL
			WHERE user_id = ? AND device_id = ?`, userID, deviceID)
		return err
	}
	switch service {
	case pb.PushService_PUSH_SERVICE_APNS, pb.PushService_PUSH_SERVICE_APNS_SANDBOX:
	default:
		return apierror.InvalidArgument("unknown push service %v", service)
	}
	if len(token) > maxPushToken {
		return apierror.InvalidArgument("push token is longer than %d bytes", maxPushToken)
	}
	// APNs tokens are hex, and go into the request path as they are.
	if strings.Trim(token, "0123456789abcdefABCDEF") != "" {
		return apierror.InvalidArgument("push token must be hex")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE device SET push_service = ?, push_token = ?
		WHERE user_id = ? AND device_id = ?`,
		service.String(), token, userID, deviceID)
	return err
}
