package httpclient

import (
	"net/url"
	"strings"
)

// OpenAI-compatible servers can expose a root URL, a prefixed API path, a
// different version, or an already complete endpoint. Endpoint construction
// separates the parsed path from its query so adding a route does not turn
// query text into part of that path. Recognized version suffixes keep the
// server's selected version while receiving the endpoint's relative route.
//
// Parsed results clear fragments and RawPath after editing Path, letting
// url.URL encode the resulting path consistently. Invalid URL syntax uses
// textual concatenation as a compatibility result. Responses token-count
// construction has a separate shortcut for supplied text ending in
// /responses; other inputs use the same parsed endpoint rules.

// BuildOpenAIEndpointURL appends endpoint to a trimmed OpenAI-compatible base
// and returns the resulting URL text. An existing full or relative endpoint
// suffix is reused; a recognized versioned base receives the route after its
// leading /v1 prefix.
//
// URL parsing keeps queries attached to the URL while the path changes,
// clears fragments, and rebuilds path escaping. If parsing fails, textual
// concatenation appends the full endpoint to the trailing-slash-trimmed base
// instead.
func BuildOpenAIEndpointURL(base string, endpoint string) string {
	normalized := strings.TrimSpace(base)
	endpoint = "/" + strings.TrimLeft(strings.TrimSpace(endpoint), "/")
	relative := strings.TrimPrefix(endpoint, "/v1")
	parsed, err := url.Parse(normalized)
	if err != nil {
		// Parsing cannot separate this base into components. Retain the supplied
		// text and append the complete endpoint for compatibility with such input.
		return strings.TrimRight(normalized, "/") + endpoint
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, endpoint) && !strings.HasSuffix(path, relative) {
		if OpenAIBaseURLHasVersionSuffix(path) {
			path += relative
		} else {
			path += endpoint
		}
	}
	parsed.Path = path
	// Path has changed, so reuse of its prior escaped spelling could identify
	// a different route. Queries survive while fragments are removed here.
	parsed.RawPath = ""
	parsed.Fragment = ""
	return parsed.String()
}

// BuildOpenAIResponsesInputTokensURL returns the Responses token-count
// endpoint for base. Trimmed text ending in /responses receives /input_tokens
// directly; other input goes through BuildOpenAIEndpointURL and its version,
// query, and fragment handling.
func BuildOpenAIResponsesInputTokensURL(base string) string {
	normalized := strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(normalized, "/responses") {
		return normalized + "/input_tokens"
	}
	return BuildOpenAIEndpointURL(base, "/v1/responses/input_tokens")
}

// OpenAIBaseURLHasVersionSuffix reports whether the final nonempty path
// segment is a recognized API version. Full URLs use their parsed path; other
// input uses text from its first slash, with trailing slashes removed.
func OpenAIBaseURLHasVersionSuffix(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}

	pathValue := ""
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		pathValue = parsed.Path
	} else if slash := strings.Index(trimmed, "/"); slash >= 0 {
		pathValue = trimmed[slash:]
	}

	pathValue = strings.TrimRight(pathValue, "/")
	if pathValue == "" {
		return false
	}
	lastSlash := strings.LastIndex(pathValue, "/")
	segment := pathValue
	if lastSlash >= 0 {
		segment = pathValue[lastSlash+1:]
	}
	return IsOpenAIAPIVersionSegment(segment)
}

// IsOpenAIAPIVersionSegment reports whether trimmed, case-insensitive text
// matches a supported version shape. It accepts v followed by digits, a
// single decimal part, or suffix text beginning alpha, beta, or preview.
func IsOpenAIAPIVersionSegment(segment string) bool {
	s := strings.ToLower(strings.TrimSpace(segment))
	if len(s) < 2 || s[0] != 'v' || !IsASCIIDigit(s[1]) {
		return false
	}

	i := 1
	for i < len(s) && IsASCIIDigit(s[i]) {
		i++
	}
	if i == len(s) {
		return true
	}
	if s[i] == '.' {
		i++
		if i == len(s) || !IsASCIIDigit(s[i]) {
			return false
		}
		for i < len(s) && IsASCIIDigit(s[i]) {
			i++
		}
		return i == len(s)
	}

	suffix := s[i:]
	return strings.HasPrefix(suffix, "alpha") ||
		strings.HasPrefix(suffix, "beta") ||
		strings.HasPrefix(suffix, "preview")
}

// IsASCIIDigit reports whether b is a decimal ASCII digit.
func IsASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
