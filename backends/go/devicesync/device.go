package devicesync

import (
	"context"

	"github.com/vlumi/wrinkle/backends/go/internal/apierror"
	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

const maxPushToken = 200

// Connect signs the user in by name, creating the user on first use, and
// registers the device.
func (s *Service) Connect(ctx context.Context, req *pb.ConnectRequest) (*pb.ConnectResponse, error) {
	if !validName(req.GetUserName()) {
		return nil, apierror.InvalidArgument("user_name must be 1 to %d characters", maxNameLength)
	}
	if n := req.GetDeviceName(); n != "" && !validName(n) {
		return nil, apierror.InvalidArgument("device_name must be at most %d characters", maxNameLength)
	}
	userID := ids.User(req.GetUserName())
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO user (user_id, name, log_head, created_at) VALUES (?, ?, 0, ?)
		ON DUPLICATE KEY UPDATE name = name`,
		userID, req.GetUserName(), now); err != nil {
		return nil, err
	}
	deviceID := req.GetDeviceId()
	if deviceID == 0 {
		deviceID = ids.Random()
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO device (user_id, device_id, name, acked_position, created_at, last_seen_at)
		VALUES (?, ?, ?, 0, ?, ?)
		ON DUPLICATE KEY UPDATE last_seen_at = ?`,
		userID, deviceID, req.GetDeviceName(), now, now, now); err != nil {
		return nil, err
	}
	resp := &pb.ConnectResponse{
		User:     &pb.User{UserId: userID, Name: req.GetUserName()},
		DeviceId: deviceID,
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT acked_position FROM device WHERE user_id = ? AND device_id = ?`,
		userID, deviceID).Scan(&resp.DeviceCursor); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT log_head FROM user WHERE user_id = ?`, userID).Scan(&resp.LogHead); err != nil {
		return nil, err
	}
	return resp, nil
}

// Ack records how far a device has processed the log.
func (s *Service) Ack(ctx context.Context, userID, deviceID, cursor uint64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE device SET acked_position = GREATEST(acked_position, ?), last_seen_at = ?
		WHERE user_id = ? AND device_id = ?`,
		cursor, s.now(), userID, deviceID)
	return err
}

// RegisterPushToken records where to push for the device while it is not
// connected; an empty token removes the registration.
func (s *Service) RegisterPushToken(ctx context.Context, userID, deviceID uint64, service pb.PushService, token string) error {
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
	_, err := s.db.ExecContext(ctx, `
		UPDATE device SET push_service = ?, push_token = ?
		WHERE user_id = ? AND device_id = ?`,
		service.String(), token, userID, deviceID)
	return err
}
