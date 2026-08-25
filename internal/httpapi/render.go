// Package httpapi exposes the JSON API of the service. Handlers translate between
// the wire format and the service layer and never contain business rules.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vance1852/heartbridge-matchmaking/internal/apperr"
	"github.com/vance1852/heartbridge-matchmaking/internal/logging"
	"github.com/vance1852/heartbridge-matchmaking/internal/repository"
)

// maxRequestBody bounds how much of a request body the server reads.
const maxRequestBody = 1 << 20

// errorBody is the unified error envelope of the API.
type errorBody struct {
	Error errorPayload `json:"error"`
}

// errorPayload carries the stable code, the readable message and the request id.
type errorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// writeJSON renders a successful payload.
func writeJSON(writer http.ResponseWriter, request *http.Request, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		writeError(writer, request, apperr.Wrap(apperr.CodeInternal, "encode response", err))
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if _, err := writer.Write(body); err != nil {
		logging.FromContext(request.Context()).Warn("could not write response body", "error", err.Error())
	}
}

// renderErrorBody encodes the unified error envelope of a failure.
func renderErrorBody(request *http.Request, err error) []byte {
	body, marshalErr := json.Marshal(errorBody{Error: errorPayload{
		Code:      string(apperr.CodeOf(err)),
		Message:   apperr.MessageOf(err),
		RequestID: logging.RequestIDFromContext(request.Context()),
	}})
	if marshalErr != nil {
		return []byte(`{"error":{"code":"internal","message":"unexpected internal error"}}`)
	}
	return body
}

// writeError renders the unified error envelope with the mapped HTTP status.
func writeError(writer http.ResponseWriter, request *http.Request, err error) {
	code := apperr.CodeOf(err)
	status := apperr.HTTPStatus(code)
	if status >= http.StatusInternalServerError {
		logging.FromContext(request.Context()).Error("request failed",
			"path", request.URL.Path, "code", string(code), "error", err.Error())
	} else {
		logging.FromContext(request.Context()).Debug("request rejected",
			"path", request.URL.Path, "code", string(code), "error", err.Error())
	}
	body := renderErrorBody(request, err)
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if _, writeErr := writer.Write(body); writeErr != nil {
		logging.FromContext(request.Context()).Warn("could not write error body", "error", writeErr.Error())
	}
}

// decodeBody reads and strictly decodes a JSON request body, returning the raw
// bytes as well so that the idempotency guard can fingerprint them.
func decodeBody(request *http.Request, target any) ([]byte, error) {
	limited := http.MaxBytesReader(nil, request.Body, maxRequestBody)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeInvalidArgument, "read request body", err)
	}
	if len(raw) == 0 {
		return nil, apperr.New(apperr.CodeInvalidArgument, "a JSON request body is required")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, apperr.Wrap(apperr.CodeInvalidArgument, "decode request body", err)
	}
	if decoder.More() {
		return nil, apperr.New(apperr.CodeInvalidArgument,
			"the request body must contain exactly one JSON document")
	}
	return raw, nil
}

// parsePage reads the limit and offset query parameters.
func parsePage(request *http.Request) (repository.Page, error) {
	page := repository.Page{}
	query := request.URL.Query()
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return repository.Page{}, apperr.Wrap(apperr.CodeInvalidArgument, "limit must be an integer", err)
		}
		page.Limit = parsed
	}
	if raw := strings.TrimSpace(query.Get("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return repository.Page{}, apperr.Wrap(apperr.CodeInvalidArgument, "offset must be an integer", err)
		}
		page.Offset = parsed
	}
	if err := page.Validate(); err != nil {
		return repository.Page{}, err
	}
	return page, nil
}

// parseInstant reads an RFC3339 query parameter.
func parseInstant(request *http.Request, name string) (*time.Time, error) {
	raw := strings.TrimSpace(request.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, apperr.Wrapf(apperr.CodeInvalidArgument, err,
			"%s must be an RFC3339 timestamp", name)
	}
	utc := parsed.UTC()
	return &utc, nil
}

// requiredInstant reads a mandatory RFC3339 query parameter.
func requiredInstant(request *http.Request, name string) (time.Time, error) {
	value, err := parseInstant(request, name)
	if err != nil {
		return time.Time{}, err
	}
	if value == nil {
		return time.Time{}, apperr.Newf(apperr.CodeInvalidArgument, "%s is required", name)
	}
	return *value, nil
}

// parseTimestamp reads an RFC3339 value from a JSON payload field.
func parseTimestamp(field, raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, apperr.Wrapf(apperr.CodeInvalidArgument, err,
			"%s must be an RFC3339 timestamp", field)
	}
	return parsed.UTC(), nil
}

// isMissing reports whether err means the addressed resource does not exist.
func isMissing(err error) bool { return errors.Is(err, apperr.ErrNotFound) }
