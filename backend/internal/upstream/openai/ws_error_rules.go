package openai

import (
	"context"
	"errors"
	"net/http"
	"strings"

	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// ws_error_rules.go classifies WebSocket acquisition and protocol errors for
// recovery and HTTP error reporting.
// WebSocket errors arrive either during HTTP handshake acquisition or inside a
// protocol error event. Acquisition uses captured status and typed local
// failures, while event classification reads normalized code, type, and message
// evidence for gateway recovery.
//
// Recovery eligibility and HTTP status are separate outputs. Rate/usage limits
// prevent generic event recovery after exact transport/continuation codes
// have been considered. Those exact codes can permit the outer recovery
// policy to act before marker-based limit checks. HTTP
// classification prioritizes bad input, authentication, and permission evidence
// before limit markers, with BadGateway as its default. These predicates
// describe evidence; the gateway decides whether an attempt can still recover
// after downstream output begins.

// ClassifyWSAcquireError labels acquisition failure from handshake status
// before local queue and deadline errors. Nil or unknown causes return
// acquire_conn; a captured unrecognized status returns dial_failed for the
// outer acquisition policy.
func ClassifyWSAcquireError(err error) string {
	if err == nil {
		return "acquire_conn"
	}
	if dialErr, ok := errors.AsType[*WSDialError](err); ok {
		switch dialErr.StatusCode {
		case http.StatusUpgradeRequired:
			return "upgrade_required"
		case http.StatusUnauthorized, http.StatusForbidden:
			return "auth_failed"
		case http.StatusTooManyRequests:
			return "upstream_rate_limited"
		}
		if dialErr.StatusCode >= 500 {
			return "upstream_5xx"
		}
		return "dial_failed"
	}
	if errors.Is(err, ErrOpenAIWSConnQueueFull) {
		return "conn_queue_full"
	}
	if errors.Is(err, ErrOpenAIWSPreferredConnUnavailable) {
		return "preferred_conn_unavailable"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "acquire_timeout"
	}
	return "acquire_conn"
}

// IsWSRateLimitError recognizes normalized limit markers in code, type, or
// message. Message-only evidence requires matching reached/exceeded wording,
// while typed rate, usage, and insufficient-quota code markers can identify a
// limit independently.
func IsWSRateLimitError(codeRaw, errTypeRaw, msgRaw string) bool {
	code := strings.ToLower(strings.TrimSpace(codeRaw))
	errType := strings.ToLower(strings.TrimSpace(errTypeRaw))
	msg := strings.ToLower(strings.TrimSpace(msgRaw))

	if strings.Contains(errType, "rate_limit") || strings.Contains(errType, "usage_limit") {
		return true
	}
	if strings.Contains(code, "rate_limit") || strings.Contains(code, "usage_limit") || strings.Contains(code, "insufficient_quota") {
		return true
	}
	if strings.Contains(msg, "usage limit") && strings.Contains(msg, "reached") {
		return true
	}
	if strings.Contains(msg, "rate limit") && (strings.Contains(msg, "reached") || strings.Contains(msg, "exceeded")) {
		return true
	}
	return false
}

// ClassifyWSErrorEventFromRaw returns a recovery reason and eligibility flag
// from normalized event fields. Exact supported codes take precedence over
// message/type matching. Upgrade, unsupported transport, connection limits,
// invalid ciphertext, and missing continuation codes select their own eligible
// reason.
//
// Rate or usage evidence returns upstream_rate_limited with false eligibility
// before general message recovery checks. Supported message/type recovery
// families, including server-error events, can return true; unrecognized events
// return event_error/false. The flag describes this classifier's eligibility
// evidence, while the enclosing gateway still checks output progress and its
// selected recovery policy before replay.
func ClassifyWSErrorEventFromRaw(codeRaw, errTypeRaw, msgRaw string) (string, bool) {
	code := strings.ToLower(strings.TrimSpace(codeRaw))
	errType := strings.ToLower(strings.TrimSpace(errTypeRaw))
	msg := strings.ToLower(strings.TrimSpace(msgRaw))

	switch code {
	case "upgrade_required":
		return "upgrade_required", true
	case "websocket_not_supported", "websocket_unsupported":
		return "ws_unsupported", true
	case "websocket_connection_limit_reached":
		return "ws_connection_limit_reached", true
	case "invalid_encrypted_content":
		return "invalid_encrypted_content", true
	case "previous_response_not_found":
		return "previous_response_not_found", true
	}
	if IsWSRateLimitError(codeRaw, errTypeRaw, msgRaw) {
		return "upstream_rate_limited", false
	}
	if strings.Contains(msg, "upgrade required") || strings.Contains(msg, "status 426") {
		return "upgrade_required", true
	}
	if strings.Contains(errType, "upgrade") {
		return "upgrade_required", true
	}
	if strings.Contains(msg, "websocket") && strings.Contains(msg, "unsupported") {
		return "ws_unsupported", true
	}
	if strings.Contains(msg, "connection limit") && strings.Contains(msg, "websocket") {
		return "ws_connection_limit_reached", true
	}
	if strings.Contains(msg, "invalid_encrypted_content") ||
		(strings.Contains(msg, "encrypted content") && strings.Contains(msg, "could not be verified")) {
		return "invalid_encrypted_content", true
	}
	if strings.Contains(msg, "previous_response_not_found") ||
		(strings.Contains(msg, "previous response") && strings.Contains(msg, "not found")) {
		return "previous_response_not_found", true
	}
	if strings.Contains(errType, "server_error") || strings.Contains(code, "server_error") {
		return "upstream_error_event", true
	}
	return "event_error", false
}

// ClassifyWSErrorEvent parses event fields and delegates recovery
// classification. Empty messages return event_error with false eligibility,
// letting callers distinguish absent evidence from a recognized transport or
// continuation failure.
func ClassifyWSErrorEvent(message []byte) (string, bool) {
	if len(message) == 0 {
		return "event_error", false
	}
	return ClassifyWSErrorEventFromRaw(protocolopenai.ParseWSErrorEventFields(message))
}

// WSErrorHTTPStatusFromRaw maps normalized code and type evidence to an HTTP
// status. Invalid input precedes authentication, permission, and rate/usage
// limits; unmatched fields return BadGateway for the enclosing gateway failure.
func WSErrorHTTPStatusFromRaw(codeRaw, errTypeRaw string) int {
	code := strings.ToLower(strings.TrimSpace(codeRaw))
	errType := strings.ToLower(strings.TrimSpace(errTypeRaw))
	switch {
	case strings.Contains(errType, "invalid_request"),
		strings.Contains(code, "invalid_request"),
		strings.Contains(code, "bad_request"),
		code == "invalid_encrypted_content",
		code == "previous_response_not_found":
		return http.StatusBadRequest
	case strings.Contains(errType, "authentication"),
		strings.Contains(code, "invalid_api_key"),
		strings.Contains(code, "unauthorized"):
		return http.StatusUnauthorized
	case strings.Contains(errType, "permission"),
		strings.Contains(code, "forbidden"):
		return http.StatusForbidden
	case IsWSRateLimitError(codeRaw, errTypeRaw, ""):
		return http.StatusTooManyRequests
	default:
		return http.StatusBadGateway
	}
}

// WSErrorHTTPStatus parses event code/type fields and returns their classified
// HTTP status. Empty or unrecognized event evidence maps to BadGateway; the
// wrapper follows the same precedence as direct raw-field callers.
func WSErrorHTTPStatus(message []byte) int {
	if len(message) == 0 {
		return http.StatusBadGateway
	}
	codeRaw, errTypeRaw, _ := protocolopenai.ParseWSErrorEventFields(message)
	return WSErrorHTTPStatusFromRaw(codeRaw, errTypeRaw)
}
