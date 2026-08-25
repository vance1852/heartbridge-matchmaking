// Package logging builds the structured logger of the service and carries it,
// together with the request id, through the context.
package logging

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// contextKey is the private key type of this package.
type contextKey struct{ name string }

var (
	loggerKey    = contextKey{name: "logger"}
	requestIDKey = contextKey{name: "request_id"}
)

// New builds a JSON logger at the requested level.
func New(writer io.Writer, level string) *slog.Logger {
	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: parseLevel(level)})
	return slog.New(handler)
}

// parseLevel maps a configuration string onto a slog level.
func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithLogger attaches a logger to the context.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey, logger)
}

// FromContext returns the context logger, falling back to the default logger so
// that call sites never need a nil check.
func FromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerKey).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}

// WithRequestID attaches a request id to the context and enriches the context
// logger so that every later record carries the correlation id.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	if requestID == "" {
		return ctx
	}
	enriched := context.WithValue(ctx, requestIDKey, requestID)
	return WithLogger(enriched, FromContext(ctx).With(slog.String("request_id", requestID)))
}

// RequestIDFromContext returns the correlation id of the current request.
func RequestIDFromContext(ctx context.Context) string {
	if requestID, ok := ctx.Value(requestIDKey).(string); ok {
		return requestID
	}
	return ""
}

// Middleware injects the application logger into every request context so that
// handlers and services below can log with the request correlation id attached.
func Middleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			next.ServeHTTP(writer, request.WithContext(WithLogger(request.Context(), logger)))
		})
	}
}
