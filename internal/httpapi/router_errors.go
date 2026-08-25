package httpapi

import (
	"net/http"
	"strings"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
)

// routerErrorWriter rewrites the plain text answers the standard router produces
// for an unknown path or an unsupported method into the unified JSON envelope.
// Without it the API would answer two different error formats depending on
// whether a request reached a handler at all.
type routerErrorWriter struct {
	http.ResponseWriter
	request     *http.Request
	intercepted bool
	committed   bool
}

// WriteHeader intercepts the router generated 404 and 405 answers. A response
// that already declares a JSON content type comes from a handler and passes
// through untouched.
func (w *routerErrorWriter) WriteHeader(status int) {
	if w.committed {
		return
	}
	w.committed = true
	if !isRouterGenerated(w.Header(), status) {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.intercepted = true

	var failure error
	switch status {
	case http.StatusNotFound:
		failure = apperr.New(apperr.CodeNotFound, "the requested endpoint does not exist")
	default:
		failure = apperr.New(apperr.CodeInvalidArgument,
			"this endpoint does not support the requested HTTP method")
	}
	body := renderErrorBody(w.request, failure)
	header := w.Header()
	header.Del("X-Content-Type-Options")
	header.Set("Content-Type", "application/json; charset=utf-8")
	// The status itself is preserved: a wrong method stays a 405 even though the
	// business code describes an invalid request.
	w.ResponseWriter.WriteHeader(status)
	if _, err := w.ResponseWriter.Write(body); err != nil {
		logging.FromContext(w.request.Context()).Warn("could not write router error body",
			"error", err.Error())
	}
}

// Write drops the plain text payload of an intercepted router answer.
func (w *routerErrorWriter) Write(payload []byte) (int, error) {
	if w.intercepted {
		return len(payload), nil
	}
	if !w.committed {
		w.committed = true
	}
	return w.ResponseWriter.Write(payload)
}

// isRouterGenerated reports whether a response was produced by the router rather
// than by one of the handlers.
func isRouterGenerated(header http.Header, status int) bool {
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		return false
	}
	return !strings.Contains(header.Get("Content-Type"), "application/json")
}

// normalizeRouterErrors wraps a handler so that router generated answers use the
// unified error envelope.
func normalizeRouterErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		next.ServeHTTP(&routerErrorWriter{ResponseWriter: writer, request: request}, request)
	})
}
