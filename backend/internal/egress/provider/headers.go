package provider

import (
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// Provider overrides replace a request header even when its map contains several
// wire spellings of the same case-insensitive name. ApplyRequestHeaders removes
// those variants before inserting the casing selected by the upstream caller,
// keeping the override as one value while leaving unrelated headers available.
// The caller prepares permitted policy values first.
//
// Response forwarding uses a separate compiled allow/removal policy. Accepted
// values are appended into newly allocated slice storage through Header.Add,
// retaining multiple response values and canonical destination names.
// WriteFilteredHeaders then appends those results to an initialized destination
// map, so existing destination values remain. Server-controlled framing names
// stay excluded by the compiled response policy.

// ApplyRequestHeaders replaces every existing case variant of each policy
// override with one value under wireCasing. Nil headers do nothing. Callers
// provide a nonnil casing function when overrides exist and obtain permitted
// values through RequestPolicy before application.
//
// Header maps can contain noncanonical spellings inserted outside Header.Set, so
// each replacement first scans and removes matching stored names. The selected
// wire spelling is then inserted directly into the map. This order avoids
// retaining several values under different case variants for the same override,
// while unrelated headers remain unchanged.
func ApplyRequestHeaders(headers http.Header, policy egress.EgressPolicy, wireCasing func(string) string) {
	if headers == nil {
		return
	}
	for name, value := range policy.Headers {
		// Remove every matching wire spelling before replacement, since map
		// storage can contain duplicate case variants created outside Header.Set.
		for existing := range headers {
			if strings.EqualFold(existing, name) {
				delete(headers, existing)
			}
		}
		headers[wireCasing(name)] = []string{value}
	}
}

// FilterHeaders allocates a response-header map containing allowed values from
// src. A nil filter uses the default policy, and accepted value slices are
// rebuilt through Header.Add so mutations to the returned slices stay separate.
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

// WriteFilteredHeaders appends permitted response values from src to dst after
// filtering. Existing destination values remain present; callers supply an
// initialized destination map when any accepted values will be written.
func WriteFilteredHeaders(dst http.Header, src http.Header, filter *egress.CompiledHeaderFilter) {
	filtered := FilterHeaders(src, filter)
	for key, values := range filtered {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
