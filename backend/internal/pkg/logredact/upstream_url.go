package logredact

import (
	"regexp"
	"strings"
)

// Upstream diagnostic messages can include query credentials embedded in URLs.
// SafeUpstreamURL removes whole query and fragment suffixes for endpoint
// display, while SanitizeUpstreamQueries masks a fixed set of query assignments
// inside larger messages where surrounding diagnostic text remains useful.

// upstreamQueryPattern recognizes nonempty credential values following
// question-mark or ampersand query delimiters, retaining the matched key and
// delimiter for diagnostic text replacement.
var upstreamQueryPattern = regexp.MustCompile(`(?i)([?&](?:key|client_secret|access_token|refresh_token)=)[^&"\s]+`)

// SafeUpstreamURL trims endpoint text and removes query and fragment suffixes
// by delimiter position. It does not parse the URL; callers remove credentials
// in userinfo or paths before logging its result.
func SafeUpstreamURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	// Endpoint display omits query and fragment contents because query parameters
	// can contain credentials and fragment text is unnecessary for locating the endpoint.
	if idx := strings.IndexByte(rawURL, '?'); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	if idx := strings.IndexByte(rawURL, '#'); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	return rawURL
}

// SanitizeUpstreamQueries masks nonempty key, client_secret, access_token, and
// refresh_token query assignments inside diagnostic text. Matching is
// case-insensitive and stops at ampersands, quotes, or whitespace; other
// credential forms require separate redaction.
func SanitizeUpstreamQueries(msg string) string {
	if msg == "" {
		return msg
	}
	return upstreamQueryPattern.ReplaceAllString(msg, `$1***`)
}
