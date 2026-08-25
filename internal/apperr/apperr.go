// Package apperr defines the stable, transport-independent error vocabulary of
// the HeartBridge matchmaking service. Every layer wraps failures into *Error so
// that the HTTP boundary can map a business failure onto a stable code without
// inspecting concrete database or driver errors.
package apperr

import (
	"context"
	"errors"
	"fmt"
)

// Code is a stable machine readable error code. Codes are part of the public API
// contract and must never be renamed once released.
type Code string

const (
	// CodeInvalidArgument marks a malformed or semantically invalid request.
	CodeInvalidArgument Code = "invalid_argument"
	// CodeUnauthenticated marks a missing, expired or revoked session token.
	CodeUnauthenticated Code = "unauthenticated"
	// CodeForbidden marks an authenticated actor lacking the required role or
	// ownership over the addressed resource.
	CodeForbidden Code = "forbidden"
	// CodeNotFound marks an addressed resource that does not exist.
	CodeNotFound Code = "not_found"
	// CodeConflict marks a uniqueness or business duplication conflict.
	CodeConflict Code = "conflict"
	// CodeVersionConflict marks a lost optimistic-locking race.
	CodeVersionConflict Code = "version_conflict"
	// CodeQuotaExhausted marks an entitlement without remaining introductions.
	CodeQuotaExhausted Code = "quota_exhausted"
	// CodeCapacityExhausted marks a venue slot without remaining capacity.
	CodeCapacityExhausted Code = "capacity_exhausted"
	// CodeIllegalTransition marks a rejected state machine transition.
	CodeIllegalTransition Code = "illegal_state_transition"
	// CodePreconditionFailed marks an unmet cross-entity precondition.
	CodePreconditionFailed Code = "precondition_failed"
	// CodeIdempotencyMismatch marks a replayed idempotency key that was first
	// used for a different request fingerprint.
	CodeIdempotencyMismatch Code = "idempotency_mismatch"
	// CodeCanceled marks a caller-cancelled operation.
	CodeCanceled Code = "canceled"
	// CodeDeadlineExceeded marks an operation that ran past its deadline.
	CodeDeadlineExceeded Code = "deadline_exceeded"
	// CodeUnavailable marks a temporarily unusable dependency.
	CodeUnavailable Code = "unavailable"
	// CodeInternal marks an unexpected failure.
	CodeInternal Code = "internal"
)

// Error is the single error type crossing package boundaries in this service.
// It keeps a stable Code, an end-user readable Message and the wrapped cause so
// that errors.Is and errors.As keep working across layers.
type Error struct {
	Code    Code
	Message string
	Err     error
}

// Error implements the error interface and always keeps the wrapped cause
// visible so that logs retain the full chain.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the wrapped cause to errors.Is and errors.As.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is reports whether target is an *Error carrying the same Code. This lets
// callers compare against the exported sentinels without string matching.
func (e *Error) Is(target error) bool {
	if e == nil {
		return target == nil
	}
	var other *Error
	if !errors.As(target, &other) {
		return false
	}
	return other != nil && other.Code == e.Code
}

// New builds an *Error without a wrapped cause.
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Newf builds an *Error with a formatted message.
func Newf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a stable code and message to an existing cause. It returns nil
// when cause is nil so that call sites can wrap unconditionally.
func Wrap(code Code, message string, cause error) error {
	if cause == nil {
		return nil
	}
	return &Error{Code: code, Message: message, Err: cause}
}

// Wrapf attaches a stable code and a formatted message to an existing cause.
func Wrapf(code Code, cause error, format string, args ...any) error {
	if cause == nil {
		return nil
	}
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Err: cause}
}

// Sentinels used with errors.Is across the service.
var (
	// ErrNotFound is the canonical "missing resource" sentinel.
	ErrNotFound = New(CodeNotFound, "resource not found")
	// ErrVersionConflict signals a lost optimistic-locking race.
	ErrVersionConflict = New(CodeVersionConflict, "resource was modified concurrently")
	// ErrQuotaExhausted signals an entitlement without remaining introductions.
	ErrQuotaExhausted = New(CodeQuotaExhausted, "introduction quota exhausted")
	// ErrCapacityExhausted signals a fully booked venue slot.
	ErrCapacityExhausted = New(CodeCapacityExhausted, "venue slot has no remaining capacity")
	// ErrUnauthenticated signals a missing, expired or revoked session.
	ErrUnauthenticated = New(CodeUnauthenticated, "authentication required")
	// ErrForbidden signals insufficient role or ownership.
	ErrForbidden = New(CodeForbidden, "operation not permitted for this actor")
	// ErrConflict signals a duplication conflict.
	ErrConflict = New(CodeConflict, "conflicting resource state")
)

// CodeOf resolves the stable code of any error. Context cancellation and
// deadline errors are mapped explicitly so that a cancelled request is never
// reported as an internal failure.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var typed *Error
	if errors.As(err, &typed) && typed != nil {
		return typed.Code
	}
	switch {
	case errors.Is(err, context.Canceled):
		return CodeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return CodeDeadlineExceeded
	default:
		return CodeInternal
	}
}

// MessageOf resolves the end-user readable message of any error, falling back to
// a neutral message so that internal details never reach the client.
func MessageOf(err error) string {
	if err == nil {
		return ""
	}
	var typed *Error
	if errors.As(err, &typed) && typed != nil && typed.Message != "" {
		return typed.Message
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "request cancelled by client"
	case errors.Is(err, context.DeadlineExceeded):
		return "request exceeded its deadline"
	default:
		return "unexpected internal error"
	}
}
