package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

// gemini_error_mapping.go maps Google error categories to Messages error types
// and selected HTTP status overrides.
// Google error objects can describe a failure more specifically than the HTTP
// status alone. Messages error presentation uses the mapped category and
// optional status override while keeping message selection in the HTTP output
// adapter.

// ClaudeErrorMapping supplies the Messages category and optional presentation
// overrides.
type ClaudeErrorMapping struct {
	Type string // Anthropic error category
	// Message is empty in MapGeminiErrorBodyToClaudeError; output adapters select
	// their client message after combining the mapping with HTTP status.
	Message string
	// StatusCode overrides HTTP mapping when positive; zero leaves that decision
	// to the consuming Gemini output adapter and its status-specific defaults.
	StatusCode int
}

// MapGeminiErrorBodyToClaudeError extracts an Anthropic error category from a
// Google error object. Empty, malformed or empty-error bodies return nil.
// Message stays empty, and only INVALID_ARGUMENT, NOT_FOUND and
// RESOURCE_EXHAUSTED supply status overrides.
func MapGeminiErrorBodyToClaudeError(body []byte) *ClaudeErrorMapping {
	if len(body) == 0 {
		return nil
	}

	var parsed struct {
		Error struct {
			Code    int    `json:"code"`    // upstream numeric status
			Message string `json:"message"` // upstream diagnostic message
			Status  string `json:"status"`  // Google error category
		} `json:"error"` // decoded upstream error
	}
	// Unreadable error bytes leave mapping unavailable; output can still classify
	// the actual HTTP status rather than treating parse failure as another upstream error.
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	if strings.TrimSpace(parsed.Error.Status) == "" && parsed.Error.Code == 0 && strings.TrimSpace(parsed.Error.Message) == "" {
		return nil
	}

	mapped := &ClaudeErrorMapping{
		Type:    MapGeminiStatusToClaudeErrorType(parsed.Error.Status),
		Message: "",
	}
	// An unknown Google category still identifies an error object, so expose the
	// generic upstream category and let HTTP output complete the status and message.
	if mapped.Type == "" {
		mapped.Type = "upstream_error"
	}

	switch strings.ToUpper(strings.TrimSpace(parsed.Error.Status)) {
	case "INVALID_ARGUMENT":
		mapped.StatusCode = http.StatusBadRequest
	case "NOT_FOUND":
		mapped.StatusCode = http.StatusNotFound
	case "RESOURCE_EXHAUSTED":
		mapped.StatusCode = http.StatusTooManyRequests
	default:
		// Other Google categories supply a type while leaving the status to the
		// consuming adapter, which also sees the actual upstream HTTP response.
	}

	// The body can contain provider internals, so this mapper leaves message
	// selection to HTTP output, where configured rules and status handling apply.
	return mapped
}

// MapGeminiStatusToClaudeErrorType returns the Anthropic category for a
// trimmed, case-insensitive Google status. An unknown status returns an empty
// string so the caller can select its own upstream-error category.
func MapGeminiStatusToClaudeErrorType(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "INVALID_ARGUMENT":
		return "invalid_request_error"
	case "PERMISSION_DENIED":
		return "permission_error"
	case "NOT_FOUND":
		return "not_found_error"
	case "RESOURCE_EXHAUSTED":
		return "rate_limit_error"
	case "UNAUTHENTICATED":
		return "authentication_error"
	case "UNAVAILABLE":
		return "overloaded_error"
	case "INTERNAL":
		return "api_error"
	case "DEADLINE_EXCEEDED":
		return "timeout_error"
	default:
		return ""
	}
}
