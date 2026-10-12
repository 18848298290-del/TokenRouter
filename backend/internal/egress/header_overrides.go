package egress

import (
	"strings"

	"golang.org/x/net/http/httpguts"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Provider header overrides replace outgoing values case-insensitively after
// platform authentication and client identity are prepared. Provider
// eligibility allows API keys on Anthropic, OpenAI, Kimi, Zhipu, DeepSeek, and
// Jev, plus API keys or OAuth on Grok. Egress validates syntax and blocks
// credential, session, and framing names.
//
// Write validation normalizes names before duplicate detection so each accepted
// header retains one value after conversion between supported map
// representations. It builds a replacement table before changing credentials,
// leaving the submitted table available on validation failure. Read-time
// resolution filters stored values through the same entry rules and allocates a
// request map, while empty stored values remain editable placeholders rather
// than applied overrides.

// Header override credential keys and size limits govern write validation and
// request-time filtering of stored administrator values.
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

// IsHeaderOverrideBlockedName reports whether the supplied lowercase header
// name is reserved by credential, session, or transport handling. The caller
// lowercases its name first; this lookup performs an exact map-key comparison.
func IsHeaderOverrideBlockedName(lowerName string) bool {
	_, blocked := headerOverrideBlockedNames[lowerName]
	return blocked
}

// ResolveHeaderOverrides returns a new map of normalized permitted nonempty
// header overrides, or nil when none survive. Invalid, blocked, and empty
// entries are skipped during request-time reads, including values stored before
// current write validation.
//
// Accepted names become lowercase map keys, keeping each request separate from
// stored map storage. Legacy case-variant names can collide after normalization;
// the last visited entry wins in Go map iteration order.
// NormalizeHeaderOverrideCredentials rejects those collisions at write time, but
// this read helper does not establish an ordering between legacy duplicates.
func ResolveHeaderOverrides(raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	result := make(map[string]string, len(raw))
	for name, value := range raw {
		lowerName, value, err := NormalizeHeaderOverrideEntry(name, value)
		// Read-time filtering covers legacy or directly stored credentials, so a
		// rejected entry cannot bypass the current override rules in a request.
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

// NormalizeHeaderOverrideCredentials validates override fields and replaces the
// credential entry with a normalized name-to-value object on success. Missing
// or nil fields leave the supplied map unchanged, while a present enable flag
// needs a boolean value.
//
// The function accepts either supported map representation, rejects excessive
// entry counts, and normalizes every name and value before detecting duplicate
// lowercase names. Named empty values remain editable placeholders. Validation
// failure returns before replacing header_overrides, so callers can reject the
// submitted credential object rather than persist a partially normalized table.
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
		// Empty name-and-value rows are editor placeholders rather than headers.
		// Omitting them prevents an empty map key from being persisted as an override.
		if lowerName == "" {
			continue
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

// ValidHeaderName reports whether name has HTTP token syntax according to
// httpguts. Callers separately determine provider eligibility and reject
// reserved credential, session, and transport names before applying an override.
func ValidHeaderName(name string) bool { return httpguts.ValidHeaderFieldName(name) }
