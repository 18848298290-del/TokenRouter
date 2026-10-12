package provider

import (
	"net/http"
	"strings"
	"unicode/utf8"

	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

const (
	// maxUsageUpstreamRequestIDLen is the byte cap used to fit IDs in the VARCHAR(128) column.
	maxUsageUpstreamRequestIDLen = 128
)

// UpstreamRequestIDFromHeaders reads the direct upstream ID from the provider-configured header.
// It returns an empty string when the provider has no configured header.
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

// usageUpstreamRequestIDPtr returns the ID stored in usage_logs.upstream_request_id.
// WebSocket turns and missing IDs return nil. Long IDs are truncated to the byte
// cap, then trimmed to a valid UTF-8 prefix.
func usageUpstreamRequestIDPtr(provider *acctcore.Record, h http.Header, wsMode bool) *string {
	if wsMode {
		return nil
	}
	id := UpstreamRequestIDFromHeaders(provider, h)
	if id == "" {
		return nil
	}
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
