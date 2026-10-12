package logredact

import (
	"regexp"
	"strings"
)

// upstreamQueryPattern matches recognized credential values in URL query parameters.
var upstreamQueryPattern = regexp.MustCompile(`(?i)([?&](?:key|client_secret|access_token|refresh_token)=)[^&"\s]+`)

// SafeUpstreamURL trims whitespace and removes query and fragment text.
// Callers must remove credentials in userinfo or paths before logging the result.
func SafeUpstreamURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if idx := strings.IndexByte(rawURL, '?'); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	if idx := strings.IndexByte(rawURL, '#'); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	return rawURL
}

// SanitizeUpstreamQueries masks recognized query credential values inside a message.
func SanitizeUpstreamQueries(msg string) string {
	if msg == "" {
		return msg
	}
	return upstreamQueryPattern.ReplaceAllString(msg, `$1***`)
}
