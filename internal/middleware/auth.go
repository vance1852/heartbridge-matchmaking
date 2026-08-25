package middleware

import (
	"context"
	"net/http"
	"runtime/debug"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/domain/identity"
	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
	"github.com/vance1852/heartbridge-matchmaking/internal/security"
)

// Authenticator resolves a bearer token into an actor.
type Authenticator interface {
	Authenticate(ctx context.Context, plaintextToken string) (identity.Actor, error)
}

// ErrorWriter renders an error as the unified JSON body. The HTTP package owns
// the rendering; middleware only needs a way to call it.
type ErrorWriter func(writer http.ResponseWriter, request *http.Request, err error)

// Authenticate resolves the bearer token of a request. Requests without a token
// pass through unauthenticated, so a route can decide whether it is public; the
// route level guard rejects the missing actor.
func Authenticate(authenticator Authenticator, writeError ErrorWriter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			token := security.BearerToken(request.Header.Get("Authorization"))
			if token == "" {
				next.ServeHTTP(writer, request)
				return
			}
			actor, err := authenticator.Authenticate(request.Context(), token)
			if err != nil {
				writeError(writer, request, err)
				return
			}
			next.ServeHTTP(writer, request.WithContext(WithActor(request.Context(), actor)))
		})
	}
}

// RequireActor rejects a request that carries no valid session.
func RequireActor(writeError ErrorWriter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if _, ok := ActorFromContext(request.Context()); !ok {
				writeError(writer, request,
					apperr.New(apperr.CodeUnauthenticated, "a valid session token is required"))
				return
			}
			next.ServeHTTP(writer, request)
		})
	}
}

// RequireRole rejects an authenticated actor without one of the accepted roles.
func RequireRole(writeError ErrorWriter, accepted ...identity.Role) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			actor, ok := ActorFromContext(request.Context())
			if !ok {
				writeError(writer, request,
					apperr.New(apperr.CodeUnauthenticated, "a valid session token is required"))
				return
			}
			if err := actor.RequireRole(accepted...); err != nil {
				writeError(writer, request, err)
				return
			}
			next.ServeHTTP(writer, request)
		})
	}
}

// Recover turns a panic into a unified 500 response and logs the stack once. The
// stack trace never reaches the client.
func Recover(writeError ErrorWriter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				logging.FromContext(request.Context()).Error("recovered from panic",
					"path", request.URL.Path,
					"panic", recovered,
					"stack", string(debug.Stack()))
				writeError(writer, request,
					apperr.New(apperr.CodeInternal, "the request could not be completed"))
			}()
			next.ServeHTTP(writer, request)
		})
	}
}
