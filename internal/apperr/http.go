package apperr

import "net/http"

// HTTPStatus maps a stable business code onto the HTTP status used by the API.
// The mapping lives next to the codes so that adding a code forces a decision
// about its transport representation.
func HTTPStatus(code Code) int {
	switch code {
	case CodeInvalidArgument:
		return http.StatusBadRequest
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeConflict, CodeVersionConflict, CodeIdempotencyMismatch:
		return http.StatusConflict
	case CodeQuotaExhausted, CodeCapacityExhausted, CodeIllegalTransition, CodePreconditionFailed:
		return http.StatusUnprocessableEntity
	case CodeCanceled:
		// 499 is the widely used "client closed request" status. It keeps
		// cancelled requests out of the 5xx error budget.
		return 499
	case CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// StatusOf is a convenience wrapper resolving the HTTP status of any error.
func StatusOf(err error) int {
	return HTTPStatus(CodeOf(err))
}
