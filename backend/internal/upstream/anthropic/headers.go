package anthropic

import (
	"net/http"
	"strings"
)

var (
	// HeaderWireCasing restores CLI header spelling after Go canonicalizes incoming
	// keys. The casing is based on Claude CLI HTTPS traffic to api.anthropic.com.
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

	// HeaderWireOrder orders diagnostic output to match observed CLI traffic.
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

	// HeaderWireOrderSet contains lowercase keys from HeaderWireOrder.
	HeaderWireOrderSet map[string]struct{}

	// AllowedHeaders lists client headers eligible for forwarding.
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

func init() {
	HeaderWireOrderSet = make(map[string]struct{}, len(HeaderWireOrder))
	for _, k := range HeaderWireOrder {
		HeaderWireOrderSet[strings.ToLower(k)] = struct{}{}
	}
}

// ResolveWireCasing maps a recognized header to CLI wire casing.
// Unrecognized keys retain their supplied casing.
func ResolveWireCasing(key string) string {
	if wk, ok := HeaderWireCasing[strings.ToLower(key)]; ok {
		return wk
	}
	return key
}

// SetHeaderRaw sets a header bypassing Go's canonical-case normalization.
// The key is stored exactly as provided, preserving original casing.
//
// It removes values under the canonical, mapped wire, and supplied raw keys
// before storing the replacement.
func SetHeaderRaw(h http.Header, key, value string) {
	h.Del(key) // remove canonical form (e.g. "Anthropic-Beta")
	if wk := ResolveWireCasing(key); wk != key {
		delete(h, wk) // remove wire casing form if different
	}
	delete(h, key) // remove exact raw key if it differs from canonical
	h[key] = []string{value}
}

// AddHeaderRaw appends a header value bypassing Go's canonical-case normalization.
func AddHeaderRaw(h http.Header, key, value string) {
	h[key] = append(h[key], value)
}

// DeleteHeaderAllForms removes the raw, mapped wire, and canonical forms of key
// so forwarded values do not survive under one of those spellings.
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

// GetHeaderRaw reads a header value, trying multiple key forms to handle the mismatch
// between Go canonical keys, wire casing keys, and raw keys:
//  1. exact key as provided
//  2. wire casing form (from HeaderWireCasing)
//  3. Go canonical form (via http.Header.Get)
func GetHeaderRaw(h http.Header, key string) string {
	// 1. exact key
	if vals := h[key]; len(vals) > 0 {
		return vals[0]
	}
	// 2. wire casing (e.g. looking up "Anthropic-Dangerous-Direct-Browser-Access" finds "anthropic-dangerous-direct-browser-access")
	if wk := ResolveWireCasing(key); wk != key {
		if vals := h[wk]; len(vals) > 0 {
			return vals[0]
		}
	}
	// 3. canonical fallback
	return h.Get(key)
}

// SortHeadersByWireOrder returns one key per case-insensitive header name.
// Known names follow HeaderWireOrder; other names follow in unspecified order.
func SortHeadersByWireOrder(h http.Header) []string {
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
