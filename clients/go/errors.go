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
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Code returns the error code of a backend error, or UNSPECIFIED.
func Code(err error) pb.ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return pb.ErrorCode_ERROR_CODE_UNSPECIFIED
}
