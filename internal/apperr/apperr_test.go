package apperr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// TestErrorChainIsPreserved verifies that wrapping keeps the original cause
// reachable through errors.Is and errors.As.
func TestErrorChainIsPreserved(t *testing.T) {
	root := errors.New("disk is full")
	wrapped := Wrap(CodeUnavailable, "append audit event", root)
	if !errors.Is(wrapped, root) {
		t.Fatal("expected the root cause to stay reachable")
	}
	deeper := Wrap(CodeInternal, "record proposal", wrapped)
	if !errors.Is(deeper, root) {
		t.Fatal("expected the root cause to survive a second wrap")
	}
	var typed *Error
	if !errors.As(deeper, &typed) {
		t.Fatal("expected the typed error to be extractable")
	}
	if typed.Code != CodeInternal {
		t.Fatalf("expected the outermost code, got %s", typed.Code)
	}
	if CodeOf(deeper) != CodeInternal {
		t.Fatalf("expected CodeOf to report the outermost code, got %s", CodeOf(deeper))
	}
	if !errors.Is(deeper, ErrUnavailable()) {
		t.Fatal("expected the chain to match the unavailable code")
	}
	if Wrap(CodeInternal, "nothing happened", nil) != nil {
		t.Fatal("expected wrapping a nil cause to stay nil")
	}
	if Wrapf(CodeInternal, nil, "nothing %s", "happened") != nil {
		t.Fatal("expected formatted wrapping of a nil cause to stay nil")
	}
}

// ErrUnavailable is a helper returning a sentinel for the unavailable code.
func ErrUnavailable() error { return New(CodeUnavailable, "dependency unavailable") }

// TestSentinelsMatchByCode verifies the errors.Is behaviour of the sentinels.
func TestSentinelsMatchByCode(t *testing.T) {
	notFound := Newf(CodeNotFound, "match %q was not found", "mch_1")
	if !errors.Is(notFound, ErrNotFound) {
		t.Fatal("expected the not-found sentinel to match by code")
	}
	if errors.Is(notFound, ErrConflict) {
		t.Fatal("expected a different code not to match")
	}
	wrapped := fmt.Errorf("service layer: %w", notFound)
	if !errors.Is(wrapped, ErrNotFound) {
		t.Fatal("expected the sentinel to match through a fmt wrap")
	}
	for _, sentinel := range []error{
		ErrNotFound, ErrVersionConflict, ErrQuotaExhausted,
		ErrCapacityExhausted, ErrUnauthenticated, ErrForbidden, ErrConflict,
	} {
		if !errors.Is(sentinel, sentinel) {
			t.Fatalf("expected %v to match itself", sentinel)
		}
	}
	if errors.Is(ErrNotFound, errors.New("plain")) {
		t.Fatal("expected a plain error not to match a coded sentinel")
	}
}

// TestContextErrorsAreClassified verifies cancellation is never reported as an
// internal failure.
func TestContextErrorsAreClassified(t *testing.T) {
	if code := CodeOf(context.Canceled); code != CodeCanceled {
		t.Fatalf("expected %s, got %s", CodeCanceled, code)
	}
	if code := CodeOf(context.DeadlineExceeded); code != CodeDeadlineExceeded {
		t.Fatalf("expected %s, got %s", CodeDeadlineExceeded, code)
	}
	wrapped := fmt.Errorf("query members: %w", context.Canceled)
	if code := CodeOf(wrapped); code != CodeCanceled {
		t.Fatalf("expected a wrapped cancellation to stay %s, got %s", CodeCanceled, code)
	}
	if MessageOf(context.Canceled) == "" || MessageOf(context.DeadlineExceeded) == "" {
		t.Fatal("expected context failures to carry a readable message")
	}
	if code := CodeOf(errors.New("something else")); code != CodeInternal {
		t.Fatalf("expected an unknown error to be internal, got %s", code)
	}
	if CodeOf(nil) != "" || MessageOf(nil) != "" {
		t.Fatal("expected nil to carry no code and no message")
	}
}

// TestMessageDoesNotLeakInternals verifies the client facing message.
func TestMessageDoesNotLeakInternals(t *testing.T) {
	internal := Wrap(CodeInternal, "insert match", errors.New("SQL logic error: no such table"))
	if got := MessageOf(internal); got != "insert match" {
		t.Fatalf("expected the declared message, got %q", got)
	}
	plain := errors.New("SQL logic error: no such table")
	if got := MessageOf(plain); got != "unexpected internal error" {
		t.Fatalf("expected a neutral message for an unclassified error, got %q", got)
	}
	if text := internal.Error(); text == "" {
		t.Fatal("expected the error text to include the chain for logs")
	}
	var nilError *Error
	if nilError.Error() != "<nil>" {
		t.Fatalf("expected a nil receiver to render safely, got %q", nilError.Error())
	}
	if nilError.Unwrap() != nil {
		t.Fatal("expected a nil receiver to unwrap to nil")
	}
	if !nilError.Is(nil) {
		t.Fatal("expected a nil receiver to match nil")
	}
}

// TestHTTPStatusMapping guards the transport contract of every code.
func TestHTTPStatusMapping(t *testing.T) {
	expected := map[Code]int{
		CodeInvalidArgument:     http.StatusBadRequest,
		CodeUnauthenticated:     http.StatusUnauthorized,
		CodeForbidden:           http.StatusForbidden,
		CodeNotFound:            http.StatusNotFound,
		CodeConflict:            http.StatusConflict,
		CodeVersionConflict:     http.StatusConflict,
		CodeIdempotencyMismatch: http.StatusConflict,
		CodeQuotaExhausted:      http.StatusUnprocessableEntity,
		CodeCapacityExhausted:   http.StatusUnprocessableEntity,
		CodeIllegalTransition:   http.StatusUnprocessableEntity,
		CodePreconditionFailed:  http.StatusUnprocessableEntity,
		CodeCanceled:            499,
		CodeDeadlineExceeded:    http.StatusGatewayTimeout,
		CodeUnavailable:         http.StatusServiceUnavailable,
		CodeInternal:            http.StatusInternalServerError,
	}
	for code, status := range expected {
		if got := HTTPStatus(code); got != status {
			t.Fatalf("code %s: expected status %d, got %d", code, status, got)
		}
	}
	if got := HTTPStatus(Code("brand_new")); got != http.StatusInternalServerError {
		t.Fatalf("expected an unknown code to map to 500, got %d", got)
	}
	if got := StatusOf(Newf(CodeNotFound, "gone")); got != http.StatusNotFound {
		t.Fatalf("expected StatusOf to follow the code, got %d", got)
	}
	if got := StatusOf(context.Canceled); got != 499 {
		t.Fatalf("expected a cancelled request to map to 499, got %d", got)
	}
}
