package google

import (
	"encoding/json"
	"fmt"
	"strings"
)

// error.go decodes Google error responses and extracts diagnostic messages from
// nested or top-level error fields.
// Google error diagnostics can include typed detail objects for activation
// links or disabled-service reasons. Raw detail entries let Code Assist readers
// inspect metadata and help-link forms independently, while the message reader
// accepts either a nested error message or a top-level compatible message.

// ErrorResponse wraps the structured diagnostic object returned by Google APIs.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"` // structured error diagnostic
}

// ErrorDetail holds the numeric code, message, status, and structured Google diagnostics.
type ErrorDetail struct {
	Code    int    `json:"code"`    // upstream numeric code
	Message string `json:"message"` // upstream diagnostic message
	Status  string `json:"status"`  // upstream symbolic status
	// Details retains raw diagnostic entries. Code Assist activation readers
	// attempt metadata and help-link decoding independently, inspecting their
	// contents without requiring the entry's @type label to match.
	Details []json.RawMessage `json:"details,omitempty"`
}

// ErrorDetailInfo holds a reason, domain, and string metadata from a diagnostic entry.
type ErrorDetailInfo struct {
	Type     string            `json:"@type"`              // diagnostic type URI
	Reason   string            `json:"reason,omitempty"`   // upstream reason label
	Domain   string            `json:"domain,omitempty"`   // upstream diagnostic domain
	Metadata map[string]string `json:"metadata,omitempty"` // provider diagnostic attributes
}

// ErrorHelp holds labeled help links from a structured Google error detail.
type ErrorHelp struct {
	Type  string     `json:"@type"`           // diagnostic type URI
	Links []HelpLink `json:"links,omitempty"` // diagnostic help links
}

// HelpLink pairs a diagnostic link URL with its human-readable description.
type HelpLink struct {
	Description string `json:"description"` // human-readable link description
	URL         string `json:"url"`         // diagnostic link URL
}

// ParseError decodes a Google error envelope and wraps JSON decoding failures.
// Successful decoding can leave missing error fields at zero, including null
// input; callers inspect the returned code and status for their expected error.
func ParseError(body string) (*ErrorResponse, error) {
	var errResp ErrorResponse
	if err := json.Unmarshal([]byte(body), &errResp); err != nil {
		return nil, fmt.Errorf("failed to parse error response: %w", err)
	}
	return &errResp, nil
}

// ExtractPlatformMessage returns a nonblank nested error.message string,
// otherwise a nonblank top-level message string, retaining its supplied spacing.
// Failed JSON decoding or absent usable strings returns empty; this reader
// extracts diagnostic text without validating the provider's error status.
func ExtractPlatformMessage(body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		// An undecodable body provides no structured message for this reader.
		// Return empty so callers can continue their separate status and raw-error handling.
		return ""
	}

	if errObj, ok := payload["error"].(map[string]any); ok {
		if msg, ok := errObj["message"].(string); ok && strings.TrimSpace(msg) != "" {
			return msg
		}
	}

	// A compatible top-level message can supply diagnostic text when the
	// nested error object contains no usable string, keeping that alternate shape readable.
	if msg, ok := payload["message"].(string); ok && strings.TrimSpace(msg) != "" {
		return msg
	}

	return ""
}
