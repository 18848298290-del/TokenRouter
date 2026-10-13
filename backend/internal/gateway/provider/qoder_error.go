package provider

import (
	"errors"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// Qoder error codes distinguish entitlement denial from authentication
// rejection even when both arrive as HTTP 403. Gateway classification retains
// the source status and message, translates ordinary credential failures to
// unauthorized, and lets the agent-limit signal override the transport status
// with rate limiting.

// DescribeQoderError extracts a Qoder APIError from the error chain and
// produces gateway status, kind, and source evidence. Unrecognized errors
// return an empty view; entitlement 403 stays forbidden, and agent-limit
// classification overrides status to HTTP 429.
func DescribeQoderError(err error) forward.QoderErrorView {
	var apiErr *qoder.APIError
	if !errors.As(err, &apiErr) {
		return forward.QoderErrorView{}
	}

	status := http.StatusBadGateway
	errType := "upstream_error"
	switch apiErr.StatusCode {
	case http.StatusUnauthorized:
		status = http.StatusUnauthorized
	case http.StatusForbidden:
		// Qoder distinguishes model entitlement from other 403 failures by its API
		// error code; retain forbidden for entitlement and use unauthorized otherwise.
		if apiErr.IsEntitlementDenied() {
			status = http.StatusForbidden
		} else {
			status = http.StatusUnauthorized
		}
	case http.StatusTooManyRequests:
		status = http.StatusTooManyRequests
		errType = "rate_limit_error"
	case http.StatusServiceUnavailable:
		status = http.StatusServiceUnavailable
	default:
		if apiErr.StatusCode >= http.StatusInternalServerError {
			status = http.StatusBadGateway
		}
	}
	// Agent-limit codes describe quota even when the source transport status is
	// different, so the gateway reports a rate-limit response for that condition.
	if apiErr.IsAgentLimit() {
		status = http.StatusTooManyRequests
		errType = "rate_limit_error"
	}
	return forward.QoderErrorView{Recognized: true, Status: status, SourceStatus: apiErr.StatusCode, Kind: errType, Message: apiErr.Error(), Body: apiErr.Body}
}
