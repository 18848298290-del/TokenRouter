package httpx

import (
	"maps"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// error.go maps application errors and HTTP status codes to response
// representations.
// Wrapped application errors carry the numeric response code, reason, and
// message selected by their owner. Mapping extracts that same entity through
// wrappers and copies its string metadata for the response, while unrecognized
// errors become the generic internal-error body supplied by apperror.

// ToHTTP returns the numeric code and serializable application-error status.
// Nil input or a nil ApplicationError found through wrappers returns HTTP 200.
// Unrecognized errors use the generic 500 message; recognized codes pass through
// unchanged, and the returned metadata map can be edited independently.
// @project-doc docs/interfaces/http_api.md#response_errors
func ToHTTP(err error) (statusCode int, body apperror.Status) {
	if err == nil {
		return http.StatusOK, apperror.Status{Code: int32(http.StatusOK)}
	}

	appErr := apperror.FromError(err)
	if appErr == nil {
		return http.StatusOK, apperror.Status{Code: int32(http.StatusOK)}
	}

	body = apperror.Status{
		Code:    appErr.Code,
		Reason:  appErr.Reason,
		Message: appErr.Message,
	}
	if appErr.Metadata != nil {
		body.Metadata = make(map[string]string, len(appErr.Metadata))
		maps.Copy(body.Metadata, appErr.Metadata)
	}
	return int(appErr.Code), body
}

// ErrorCode returns the numeric application category through error wrappers.
// Nil interfaces yield 200, unknown errors yield 500, and custom codes pass
// through. Callers provide a true nil interface for no error; CategoryOf
// dereferences a typed nil ApplicationError found in a nonnil error chain.
func ErrorCode(err error) int {
	return int(apperror.CategoryOf(err))
}

// HTTPStatusToGoogleStatus maps named HTTP failures to their Google status names.
// Unlisted statuses at or above 500 use INTERNAL, including custom server codes;
// other unlisted statuses use UNKNOWN for the caller's Google error body.
func HTTPStatusToGoogleStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	default:
		// A custom code such as 529 still selects the server-error category here,
		// keeping its returned status consistent with other failures in that numeric range.
		if status >= 500 {
			return "INTERNAL"
		}
		return "UNKNOWN"
	}
}
