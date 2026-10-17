package provider

import (
	"net/http"
	"strings"
	"unicode/utf8"

	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// upstream_request_id.go selects upstream response identifiers and prepares
// bounded values for usage records. HTTP completion adapters use a provider's
// configured header, or X-Request-ID when its configuration is blank. Only the
// selected response header contributes an identifier, so a missing custom header
// leaves the usage field empty even when another header is present.
//
// Usage identifiers fit a byte-limited database field. Values are trimmed before
// capture, then truncated with any incomplete UTF-8 suffix removed. WebSocket
// turns omit header-derived identifiers because individual turns share one HTTP
// connection. A missing provider also leaves the usage field empty.

const (
	// maxUsageUpstreamRequestIDLen bounds captured upstream IDs in bytes before
	// persistence to usage_logs.upstream_request_id, including its UTF-8 prefix repair.
	maxUsageUpstreamRequestIDLen = 128
)

// UpstreamRequestIDFromHeaders reads and trims the selected upstream response
// header. Blank provider configuration selects X-Request-ID. Nil providers,
// empty headers, and a missing selected header return an empty string; a
// configured header takes precedence even when another header has a value.
func UpstreamRequestIDFromHeaders(provider *acctcore.Record, h http.Header) string {
	if provider == nil || len(h) == 0 {
		return ""
	}
	name := acctcore.UpstreamRequestIDHeaderName(provider)
	if name == "" {
		// Match request diagnostics so ordinary HTTP completions retain a searchable
		// upstream identifier when administrators leave the header setting blank.
		name = "X-Request-ID"
	}
	return strings.TrimSpace(h.Get(name))
}

// usageUpstreamRequestIDPtr prepares the header-derived ID for
// usage_logs.upstream_request_id. WS mode and missing IDs return nil;
// oversized HTTP IDs are byte-truncated and shortened until the retained
// prefix is valid UTF-8.
func usageUpstreamRequestIDPtr(provider *acctcore.Record, h http.Header, wsMode bool) *string {
	if wsMode {
		return nil
	}
	id := UpstreamRequestIDFromHeaders(provider, h)
	if id == "" {
		return nil
	}
	// Cap bytes for storage, then discard an incomplete UTF-8 suffix so the
	// truncated identifier remains a valid string for the usage record writer.
	if len(id) > maxUsageUpstreamRequestIDLen {
		id = id[:maxUsageUpstreamRequestIDLen]
		for id != "" && !utf8.ValidString(id) {
			id = id[:len(id)-1]
		}
	}
	if id == "" {
		return nil
	}
	return &id
}
