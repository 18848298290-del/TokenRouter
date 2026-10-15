package policy

import (
	"fmt"
	"net/textproto"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// headers.go validates and canonicalizes the ordered list of configured
// client-IP headers.
// Configured client-IP header names are stored in an ordered, canonical form.
// Normalization rejects invalid HTTP field names and limits distinct sources,
// while case-insensitive deduplication keeps the first configured position for
// each name across process configuration and runtime settings.

// MaxForwardedClientIPHeaders bounds the unique names accepted for ordered
// client-address discovery in process configuration and persisted runtime settings.
const MaxForwardedClientIPHeaders = 16

// NormalizeForwardedClientIPHeaders returns canonical, unique names in their configured order.
// Whitespace is trimmed, names use HTTP field syntax, and duplicates ignore case.
// Invalid names or a seventeenth unique name return nil with an error; empty
// input produces an allocated empty slice for JSON array persistence.
func NormalizeForwardedClientIPHeaders(headers []string) ([]string, error) {
	normalized := make([]string, 0, len(headers))
	seen := make(map[string]struct{}, len(headers))
	for _, header := range headers {
		header = strings.TrimSpace(header)
		if !httpguts.ValidHeaderFieldName(header) {
			return nil, fmt.Errorf("invalid HTTP header field name %q", header)
		}
		// HTTP names ignore case. Canonical spelling gives stored settings and
		// Header.Values the same representation while folded keys detect aliases.
		canonical := textproto.CanonicalMIMEHeaderKey(header)
		key := strings.ToLower(canonical)
		if _, exists := seen[key]; exists {
			continue
		}
		// Count unique sources after deduplication, so differently cased aliases
		// do not consume the limit reserved for independently configured header names.
		if len(normalized) == MaxForwardedClientIPHeaders {
			return nil, fmt.Errorf("forwarded client IP headers must contain at most %d unique names", MaxForwardedClientIPHeaders)
		}
		seen[key] = struct{}{}
		normalized = append(normalized, canonical)
	}
	return normalized, nil
}
