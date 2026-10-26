package wrinkleclient

import (
	"errors"
	"fmt"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Error is an error response from the backend.
type Error struct {
	Code    pb.ErrorCode
	Message string
	Reason  pb.ErrorReason
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Reason returns the reason of a backend error, or UNSPECIFIED.
func Reason(err error) pb.ErrorReason {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return pb.ErrorReason_ERROR_REASON_UNSPECIFIED
}

// staleKeys reports whether the error asks for the conversation's keys
// again: the epoch moved on, or must.
func staleKeys(err error) bool {
	r := Reason(err)
	return r == pb.ErrorReason_ERROR_REASON_STALE_KEY_EPOCH || r == pb.ErrorReason_ERROR_REASON_KEYS_MUST_ROTATE
}

// Code returns the error code of a backend error, or UNSPECIFIED.
func Code(err error) pb.ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return pb.ErrorCode_ERROR_CODE_UNSPECIFIED
}
