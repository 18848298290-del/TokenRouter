package egress

import (
	"strings"

	"golang.org/x/net/http/httpguts"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Header overrides apply to API key providers on Anthropic, OpenAI, and supported
// Chinese provider platforms, and to Grok API key and OAuth providers.
// Administrators configure header name/value pairs to replace outgoing headers
// case-insensitively. Entries with empty values are ignored when applying overrides.
const (
	credKeyHeaderOverrideEnabled = "header_override_enabled"
	credKeyHeaderOverrides       = "header_overrides"

	maxHeaderOverrideEntries     = 64
	maxHeaderOverrideNameLength  = 200
	maxHeaderOverrideValueLength = 8192
)

// headerOverrideBlockedNames lists lowercase headers that providers cannot override.
//   - Connection and hop-by-hop headers belong to the HTTP stack.
//   - Host and content-length are controlled by Request.Host and ContentLength.
//   - Content-type describes the body, including each request's multipart boundary.
//   - Authorization, API keys, and cookies come from provider credentials.
//   - Accept-encoding can force compression that breaks SSE and usage parsing.
//   - WebSocket handshake headers belong to the dialer.
//   - Session headers, such as session_id, x-claude-code-session-id, and x-grok-conv-id,
//     isolate individual requests; static values can mix sessions.
var headerOverrideBlockedNames = map[string]struct{}{
	"host":                     {},
	"content-length":           {},
	"content-type":             {},
	"transfer-encoding":        {},
	"connection":               {},
	"keep-alive":               {},
	"proxy-authenticate":       {},
	"proxy-authorization":      {},
	"proxy-connection":         {},
	"te":                       {},
	"trailer":                  {},
	"upgrade":                  {},
	"authorization":            {},
	"x-api-key":                {},
	"x-goog-api-key":           {},
	"cookie":                   {},
	"accept-encoding":          {},
	"sec-websocket-key":        {},
	"sec-websocket-version":    {},
	"sec-websocket-extensions": {},
	"sec-websocket-protocol":   {},
	"sec-websocket-accept":     {},
	"session_id":               {},
	"conversation_id":          {},
	"x-codex-turn-state":       {},
	"x-codex-turn-metadata":    {},
	"chatgpt-account-id":       {},
	"x-claude-code-session-id": {},
	"x-client-request-id":      {},
	"x-grok-conv-id":           {},
}

func IsHeaderOverrideBlockedName(lowerName string) bool {
	_, blocked := headerOverrideBlockedNames[lowerName]
	return blocked
}

// ResolveHeaderOverrides normalizes overrides and skips invalid or empty entries.
// Read-time filtering also covers stored data predating validation or blocklist additions.
func ResolveHeaderOverrides(raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	result := make(map[string]string, len(raw))
	for name, value := range raw {
		lowerName, value, err := NormalizeHeaderOverrideEntry(name, value)
		if err != nil || lowerName == "" || value == "" {
			continue
		}
		result[lowerName] = value
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// NormalizeHeaderOverrideCredentials validates and normalizes overrides in place.
// Provider write paths call it; absent override fields leave credentials unchanged.
// It trims names and values, lowercases names, and discards entirely empty entries.
func NormalizeHeaderOverrideCredentials(credentials map[string]any) error {
	if credentials == nil {
		return nil
	}
	if raw, ok := credentials[credKeyHeaderOverrideEnabled]; ok && raw != nil {
		if _, isBool := raw.(bool); !isBool {
			return infraerrors.New(infraerrors.Category(400), "INVALID_HEADER_OVERRIDE",
				"header_override_enabled must be a boolean")
		}
	}
	raw, ok := credentials[credKeyHeaderOverrides]
	if !ok || raw == nil {
		return nil
	}

	var entries map[string]any
	switch m := raw.(type) {
	case map[string]any:
		entries = m
	case map[string]string:
		entries = make(map[string]any, len(m))
		for k, v := range m {
			entries[k] = v
		}
	default:
		return infraerrors.New(infraerrors.Category(400), "INVALID_HEADER_OVERRIDE",
			"header_overrides must be an object of header name to string value")
	}

	if len(entries) > maxHeaderOverrideEntries {
		return infraerrors.Newf(infraerrors.Category(400), "INVALID_HEADER_OVERRIDE",
			"header_overrides supports at most %d entries", maxHeaderOverrideEntries)
	}

	normalized := make(map[string]any, len(entries))
	for name, rawValue := range entries {
		value, isString := rawValue.(string)
		if !isString {
			return infraerrors.Newf(infraerrors.Category(400), "INVALID_HEADER_OVERRIDE",
				"header %q value must be a string", name)
		}
		lowerName, value, err := NormalizeHeaderOverrideEntry(name, value)
		if err != nil {
			return err
		}
		if lowerName == "" {
			continue // Discard an entirely empty placeholder row.
		}
		if _, dup := normalized[lowerName]; dup {
			return infraerrors.Newf(infraerrors.Category(400), "INVALID_HEADER_OVERRIDE",
				"duplicate header name %q (matching is case-insensitive)", lowerName)
		}
		normalized[lowerName] = value
	}
	credentials[credKeyHeaderOverrides] = normalized
	return nil
}

// NormalizeHeaderOverrideEntry validates one entry for both write-time rejection
// and read-time filtering, keeping those paths on the same validation rules.
// An empty name and value return ("", "", nil); a named empty value is a valid placeholder.
func NormalizeHeaderOverrideEntry(name, value string) (string, string, error) {
	lowerName := strings.ToLower(strings.TrimSpace(name))
	value = strings.TrimSpace(value)
	if lowerName == "" {
		if value == "" {
			return "", "", nil
		}
		return "", "", infraerrors.New(infraerrors.Category(400), "INVALID_HEADER_OVERRIDE",
			"header name must not be empty")
	}
	if len(lowerName) > maxHeaderOverrideNameLength {
		return "", "", infraerrors.Newf(infraerrors.Category(400), "INVALID_HEADER_OVERRIDE",
			"header name %q exceeds %d characters", lowerName, maxHeaderOverrideNameLength)
	}
	if !ValidHeaderName(lowerName) {
		return "", "", infraerrors.Newf(infraerrors.Category(400), "INVALID_HEADER_OVERRIDE",
			"invalid header name %q", lowerName)
	}
	if IsHeaderOverrideBlockedName(lowerName) {
		return "", "", infraerrors.Newf(infraerrors.Category(400), "INVALID_HEADER_OVERRIDE",
			"header %q is not allowed to be overridden", lowerName)
	}
	if len(value) > maxHeaderOverrideValueLength {
		return "", "", infraerrors.Newf(infraerrors.Category(400), "INVALID_HEADER_OVERRIDE",
			"header %q value exceeds %d characters", lowerName, maxHeaderOverrideValueLength)
	}
	if !httpguts.ValidHeaderFieldValue(value) {
		return "", "", infraerrors.Newf(infraerrors.Category(400), "INVALID_HEADER_OVERRIDE",
			"header %q has an invalid value", lowerName)
	}
	return lowerName, value, nil
}

// ValidHeaderName checks HTTP token syntax; callers check provider eligibility and blocked names.
func ValidHeaderName(name string) bool { return httpguts.ValidHeaderFieldName(name) }
