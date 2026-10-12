package provider

import (
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// ApplyRequestHeaders applies policy overrides using the caller's wire casing function.
func ApplyRequestHeaders(headers http.Header, policy egress.EgressPolicy, wireCasing func(string) string) {
	if headers == nil {
		return
	}
	for name, value := range policy.Headers {
		for existing := range headers {
			if strings.EqualFold(existing, name) {
				delete(headers, existing)
			}
		}
		headers[wireCasing(name)] = []string{value}
	}
}

// FilterHeaders copies response headers allowed by the filter.
func FilterHeaders(src http.Header, filter *egress.CompiledHeaderFilter) http.Header {
	filtered := make(http.Header, len(src))
	for key, values := range src {
		if !filter.Allows(key) {
			continue
		}
		for _, value := range values {
			filtered.Add(key, value)
		}
	}
	return filtered
}

// WriteFilteredHeaders appends allowed response headers to dst.
func WriteFilteredHeaders(dst http.Header, src http.Header, filter *egress.CompiledHeaderFilter) {
	filtered := FilterHeaders(src, filter)
	for key, values := range filtered {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
