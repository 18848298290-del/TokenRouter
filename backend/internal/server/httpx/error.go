package httpx

import (
	"maps"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// ToHTTP converts an error into an HTTP status code and a JSON-serializable body.
//
// The returned body matches the project's apperror.Status shape:
// { code, reason, message, metadata }.
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

// ErrorCode returns the HTTP code for an application error, including nil and custom legacy codes.
func ErrorCode(err error) int {
	return int(apperror.CategoryOf(err))
}

// HTTPStatusToGoogleStatus maps an HTTP status to Google's error status name.
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
		if status >= 500 {
			return "INTERNAL"
		}
		return "UNKNOWN"
	}
}
