package provider

import (
	"net/http"
	"strings"
	"unicode/utf8"

	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// Usage records capture an upstream request ID only from the
// provider-configured response header. WebSocket turns omit that
// header-derived value, and HTTP IDs are trimmed and byte-capped before
// storage, with truncated tails reduced to valid UTF-8.

const (
	// maxUsageUpstreamRequestIDLen bounds captured upstream IDs in bytes before
	// persistence to usage_logs.upstream_request_id, including its UTF-8 prefix repair.
	maxUsageUpstreamRequestIDLen = 128
)

// UpstreamRequestIDFromHeaders reads the provider-configured response header
// and trims its value. Empty headers or an absent configured name return an
// empty string; common platform header names are not inferred.
func UpstreamRequestIDFromHeaders(provider *acctcore.Record, h http.Header) string {
	if len(h) == 0 {
		return ""
	}
	name := acctcore.UpstreamRequestIDHeaderName(provider)
	if name == "" {
		return ""
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
