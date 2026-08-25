// Package middleware holds the HTTP cross-cutting concerns: request identity,
// structured access logging, panic recovery, request deadlines and bearer token
// authentication.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
)

// RequestIDHeader is the header carrying the correlation id in and out.
const RequestIDHeader = "X-Request-Id"

// Middleware decorates an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware left to right, so the first entry sees the request
// first.
func Chain(handler http.Handler, middlewares ...Middleware) http.Handler {
	for index := len(middlewares) - 1; index >= 0; index-- {
		handler = middlewares[index](handler)
	}
	return handler
}

// actorKey is the private context key holding the authenticated actor.
type actorKey struct{}

// WithActor attaches the authenticated actor to the context.
func WithActor(ctx context.Context, actor identity.Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFromContext returns the authenticated actor of the request.
func ActorFromContext(ctx context.Context) (identity.Actor, bool) {
	actor, ok := ctx.Value(actorKey{}).(identity.Actor)
	return actor, ok
}

// RequestID accepts a client supplied correlation id or mints one, then places it
// in the context, the logger and the response headers.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			requestID := request.Header.Get(RequestIDHeader)
			if !validRequestID(requestID) {
				requestID = newRequestID()
			}
			writer.Header().Set(RequestIDHeader, requestID)
			ctx := logging.WithRequestID(request.Context(), requestID)
			next.ServeHTTP(writer, request.WithContext(ctx))
		})
	}
}

// validRequestID accepts a bounded, printable client correlation id.
func validRequestID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if r < '!' || r > '~' {
			return false
		}
	}
	return true
}

// newRequestID mints a random correlation id.
func newRequestID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "req-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return "req-" + hex.EncodeToString(raw)
}

// statusRecorder captures the status code and payload size for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

// WriteHeader records the status code.
func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

// Write records the payload size and defaults the status to 200.
func (r *statusRecorder) Write(payload []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	written, err := r.ResponseWriter.Write(payload)
	r.bytes += written
	return written, err
}

// Status returns the recorded status code.
func (r *statusRecorder) Status() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// AccessLog emits one structured record per request. It never logs the request
// body, headers or bearer tokens.
func AccessLog() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: writer}
			next.ServeHTTP(recorder, request)
			logging.FromContext(request.Context()).Info("http request",
				"method", request.Method,
				"path", request.URL.Path,
				"status", recorder.Status(),
				"bytes", recorder.bytes,
				"duration_ms", time.Since(started).Milliseconds(),
			)
		})
	}
}

// Timeout bounds the handling of one request and propagates the deadline through
// the context so that services and repositories abort with it.
func Timeout(limit time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		if limit <= 0 {
			return next
		}
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			ctx, cancel := context.WithTimeout(request.Context(), limit)
			defer cancel()
			next.ServeHTTP(writer, request.WithContext(ctx))
		})
	}
}
