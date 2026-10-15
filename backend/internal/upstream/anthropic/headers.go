package anthropic

import (
	"net/http"
	"strings"
)

// headers.go reads and writes Claude header spellings and orders diagnostic
// header names.
// Claude Header spelling can differ from Go's canonical map keys. Request
// builders read exact, mapped, and canonical spellings and store selected wire
// keys directly, so outbound fingerprint replacement also needs to clear values
// under the recognized alternative spellings.
//
// Casing maps identify names accepted for forwarding and names used in
// diagnostics. Diagnostic ordering deduplicates case-insensitive names, chooses
// the retained map key, and places known names in the configured sequence; it
// does not control HTTP transport serialization. Header mutations require a
// caller-owned, initialized map and caller synchronization when that map is
// shared, since these helpers add no locks.

// Header maps govern supported wire spelling, diagnostic ordering, and client
// forwarding permission; callers treat these initialized shared values as
// read-only.
var (
	// HeaderWireCasing maps recognized lowercase names to the spelling used by
	// outgoing request builders; callers use canonical lookup separately for
	// incoming Headers.
	HeaderWireCasing = map[string]string{
		// Title case
		"accept":     "Accept",
		"user-agent": "User-Agent",

		// SDK header casing.
		"x-stainless-retry-count":     "X-Stainless-Retry-Count",
		"x-stainless-timeout":         "X-Stainless-Timeout",
		"x-stainless-lang":            "X-Stainless-Lang",
		"x-stainless-package-version": "X-Stainless-Package-Version",
		"x-stainless-os":              "X-Stainless-OS",
		"x-stainless-arch":            "X-Stainless-Arch",
		"x-stainless-runtime":         "X-Stainless-Runtime",
		"x-stainless-runtime-version": "X-Stainless-Runtime-Version",
		"x-stainless-helper-method":   "x-stainless-helper-method",

		// Lowercase Anthropic SDK headers.
		"anthropic-dangerous-direct-browser-access": "anthropic-dangerous-direct-browser-access",
		"anthropic-version":                         "anthropic-version",
		"anthropic-beta":                            "anthropic-beta",
		"x-app":                                     "x-app",
		"content-type":                              "content-type",
		"accept-language":                           "accept-language",
		"sec-fetch-mode":                            "sec-fetch-mode",
		"accept-encoding":                           "accept-encoding",
		"authorization":                             "authorization",

		// Claude Code session and request headers.
		"x-claude-code-session-id": "X-Claude-Code-Session-Id",
		"x-client-request-id":      "x-client-request-id",
		"content-length":           "content-length",
	}

	// HeaderWireOrder selects the configured order of recognized names in
	// diagnostic output; HTTP serialization order remains with the request
	// transport.
	HeaderWireOrder = []string{
		"Accept",
		"X-Stainless-Retry-Count",
		"X-Stainless-Timeout",
		"X-Stainless-Lang",
		"X-Stainless-Package-Version",
		"X-Stainless-OS",
		"X-Stainless-Arch",
		"X-Stainless-Runtime",
		"X-Stainless-Runtime-Version",
		"anthropic-dangerous-direct-browser-access",
		"anthropic-version",
		"authorization",
		"x-app",
		"User-Agent",
		"X-Claude-Code-Session-Id",
		"content-type",
		"anthropic-beta",
		"x-client-request-id",
		"accept-language",
		"sec-fetch-mode",
		"accept-encoding",
		"content-length",
		"x-stainless-helper-method",
	}

	// HeaderWireOrderSet holds lowercase membership built by init from
	// HeaderWireOrder for callers checking which names belong to that diagnostic
	// sequence.
	HeaderWireOrderSet map[string]struct{}

	// AllowedHeaders identifies lowercase client Header names eligible for
	// platform forwarding; request builders supply authentication and other
	// controlled values separately.
	AllowedHeaders = map[string]bool{
		"accept":                                    true,
		"x-stainless-retry-count":                   true,
		"x-stainless-timeout":                       true,
		"x-stainless-lang":                          true,
		"x-stainless-package-version":               true,
		"x-stainless-os":                            true,
		"x-stainless-arch":                          true,
		"x-stainless-runtime":                       true,
		"x-stainless-runtime-version":               true,
		"x-stainless-helper-method":                 true,
		"anthropic-dangerous-direct-browser-access": true,
		"anthropic-version":                         true,
		"x-app":                                     true,
		"anthropic-beta":                            true,
		"accept-language":                           true,
		"sec-fetch-mode":                            true,
		"user-agent":                                true,
		"content-type":                              true,
		"accept-encoding":                           true,
		"x-claude-code-session-id":                  true,
		"x-client-request-id":                       true,
	}
)

// init builds lowercase membership for the configured diagnostic Header
// sequence.
func init() {
	HeaderWireOrderSet = make(map[string]struct{}, len(HeaderWireOrder))
	for _, k := range HeaderWireOrder {
		HeaderWireOrderSet[strings.ToLower(k)] = struct{}{}
	}
}

// ResolveWireCasing returns the configured spelling for a case-insensitive
// recognized name. Unknown keys retain their supplied spelling, allowing
// callers to apply their own policy before storing them.
func ResolveWireCasing(key string) string {
	if wk, ok := HeaderWireCasing[strings.ToLower(key)]; ok {
		return wk
	}
	return key
}

// SetHeaderRaw replaces value at the exact key while clearing its canonical and
// configured wire forms. Callers provide an initialized Header map and
// synchronize access when that map is shared.
//
// Direct map insertion retains the selected spelling for request serialization.
// Deleting the recognized alternatives prevents a previously forwarded value
// from surviving under one of those forms, but arbitrary other spellings are
// outside this lookup. The helper changes the caller's map and replaces the
// value slice with one value.
func SetHeaderRaw(h http.Header, key, value string) {
	// Clear the named casing alternatives before exact-key insertion so
	// replacement cannot leave a recognized forwarded value reachable through
	// another helper lookup.
	h.Del(key) // remove canonical form (e.g. "Anthropic-Beta")
	if wk := ResolveWireCasing(key); wk != key {
		delete(h, wk) // remove wire casing form if different
	}
	delete(h, key) // remove exact raw key if it differs from canonical
	h[key] = []string{value}
}

// AddHeaderRaw appends value under key's exact spelling on the supplied Header
// map. Callers provide an initialized map and synchronization for shared
// access; existing alternative spellings remain separate entries.
func AddHeaderRaw(h http.Header, key, value string) {
	h[key] = append(h[key], value)
}

// DeleteHeaderAllForms removes the supplied, canonical, and configured wire
// forms of key. Nil maps and empty names return directly, and callers
// synchronize access when mutating a shared Header map.
func DeleteHeaderAllForms(h http.Header, key string) {
	if h == nil || key == "" {
		return
	}
	h.Del(key) // canonical
	delete(h, key)
	if wk := ResolveWireCasing(key); wk != key {
		delete(h, wk)
	}
}

// GetHeaderRaw reads the first value under the exact key, then its mapped wire
// form, then Go's canonical lookup. Nil maps return empty, and arbitrary
// alternative casing is outside these three choices.
func GetHeaderRaw(h http.Header, key string) string {
	// 1. exact key
	if vals := h[key]; len(vals) > 0 {
		return vals[0]
	}
	// 2. wire casing (e.g. looking up "Anthropic-Dangerous-Direct-Browser-Access"
	// finds "anthropic-dangerous-direct-browser-access")
	if wk := ResolveWireCasing(key); wk != key {
		if vals := h[wk]; len(vals) > 0 {
			return vals[0]
		}
	}
	// 3. canonical fallback
	return h.Get(key)
}

// SortHeadersByWireOrder returns one retained key for each case-insensitive
// Header name, with recognized names first in HeaderWireOrder. Unrecognized
// names follow in map iteration order, and nil input produces an empty slice.
//
// The lowercase index lets the configured order find whatever spelling the map
// contains. When several spellings represent one name, map traversal chooses
// the representative, so callers cannot rely on which spelling wins. This
// result supports diagnostics; HTTP transport order is determined elsewhere and
// the input map is left unchanged.
func SortHeadersByWireOrder(h http.Header) []string {
	// The lowercase index coalesces spellings for diagnostic order; duplicate
	// representatives follow map traversal, which gives callers no stable choice
	// of retained spelling.
	present := make(map[string]string, len(h))
	for k := range h {
		present[strings.ToLower(k)] = k
	}

	result := make([]string, 0, len(h))
	seen := make(map[string]struct{}, len(h))

	for _, wk := range HeaderWireOrder {
		lk := strings.ToLower(wk)
		if actual, ok := present[lk]; ok {
			if _, dup := seen[lk]; !dup {
				result = append(result, actual)
				seen[lk] = struct{}{}
			}
		}
	}

	for k := range h {
		lk := strings.ToLower(k)
		if _, ok := seen[lk]; !ok {
			result = append(result, k)
			seen[lk] = struct{}{}
		}
	}

	return result
}
