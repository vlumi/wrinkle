// Package apierror carries a contract error code with an error.
package apierror

import (
	"errors"
	"fmt"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

type Error struct {
	Code    pb.ErrorCode
	Message string
	Reason  pb.ErrorReason
}

func (e *Error) Error() string { return e.Message }

func New(code pb.ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func InvalidArgument(format string, args ...any) *Error {
	return New(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, format, args...)
}

func Unauthenticated(format string, args ...any) *Error {
	return New(pb.ErrorCode_ERROR_CODE_UNAUTHENTICATED, format, args...)
}

func PermissionDenied(format string, args ...any) *Error {
	return New(pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED, format, args...)
}

func NotFound(format string, args ...any) *Error {
	return New(pb.ErrorCode_ERROR_CODE_NOT_FOUND, format, args...)
}

// FailedPrecondition names the precondition, which clients act on.
func FailedPrecondition(reason pb.ErrorReason, format string, args ...any) *Error {
	return New(pb.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, format, args...).Because(reason)
}

func Unavailable(format string, args ...any) *Error {
	return New(pb.ErrorCode_ERROR_CODE_UNAVAILABLE, format, args...)
}

func ResourceExhausted(format string, args ...any) *Error {
	return New(pb.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED, format, args...)
}

func AlreadyExists(format string, args ...any) *Error {
	return New(pb.ErrorCode_ERROR_CODE_ALREADY_EXISTS, format, args...)
}

func CursorExpired(format string, args ...any) *Error {
	return New(pb.ErrorCode_ERROR_CODE_CURSOR_EXPIRED, format, args...)
}

// Because sets the error's reason.
func (e *Error) Because(reason pb.ErrorReason) *Error {
	e.Reason = reason
	return e
}

// Code returns the error's code; errors without one are internal.
func Code(err error) pb.ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return pb.ErrorCode_ERROR_CODE_INTERNAL
}

// Proto converts an error to its wire form. Internal errors keep their
// detail out of the response.
func Proto(err error) *pb.Error {
	code := Code(err)
	msg := err.Error()
	if code == pb.ErrorCode_ERROR_CODE_INTERNAL {
		msg = "internal error"
	}
	var reason pb.ErrorReason
	if e := (*Error)(nil); errors.As(err, &e) {
		reason = e.Reason
	}
	return &pb.Error{Code: code, Message: msg, Reason: reason}
}

// Response is the response to a request that failed with err.
func Response(requestID uint64, err error) *pb.Response {
	return &pb.Response{RequestId: requestID, Body: &pb.Response_Error{Error: Proto(err)}}
}
