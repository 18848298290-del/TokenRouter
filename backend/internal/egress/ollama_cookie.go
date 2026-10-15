package egress

import (
	"errors"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// ollama_cookie.go validates pasted Ollama Cookie headers and retains
// recognized session cookies in input order.
// Ollama cloud usage forwards selected session cookies from a pasted Cookie
// header, which may also contain unrelated tracking values. Every pair is
// validated before filtering, so a malformed tracking pair, repeated name, or
// Set-Cookie attribute rejects the whole input instead of disappearing during
// selection. Raw input size is checked before surrounding whitespace is trimmed.
//
// Accepted session pairs retain their input order and original name casing.
// Auth.js and NextAuth session names can carry numeric shard suffixes, allowing
// separate pieces to remain ordered when the selected pairs are joined. Name
// recognition is case sensitive, while duplicate detection is case insensitive.
// At least one recognized session pair is needed for a usable outgoing cookie
// value.

// ollamaCloudUsageMaxSessionBytes bounds the raw pasted cookie text before
// whitespace trimming and per-pair validation begin.
const ollamaCloudUsageMaxSessionBytes = 16 * 1024

// NormalizeOllamaCloudUsageCookie validates pasted Cookie pairs and returns the
// recognized session pairs in input order. The raw input has a 16 KiB limit;
// surrounding whitespace is trimmed before embedded CR/LF checks.
//
// All pairs need nonempty token names and values, and case-insensitive
// duplicates or Set-Cookie attributes reject the entire input. Allowed session
// names remain case sensitive, including numeric shard suffixes. At least one
// recognized session pair is required, and retained values are joined with
// semicolon-space separators.
func NormalizeOllamaCloudUsageCookie(raw string) (string, error) {
	if len(raw) > ollamaCloudUsageMaxSessionBytes {
		return "", errors.New("session is too large")
	}
	raw = strings.TrimSpace(raw)
	if strings.ContainsAny(raw, "\r\n") {
		return "", errors.New("session contains invalid header characters")
	}
	if raw == "" {
		return "", errors.New("session cannot be empty")
	}
	if !httpguts.ValidHeaderFieldValue(raw) {
		return "", errors.New("session contains invalid header characters")
	}
	blockedAttributes := map[string]struct{}{
		"domain": {}, "path": {}, "expires": {}, "max-age": {}, "samesite": {}, "secure": {}, "httponly": {}, "partitioned": {},
	}
	parts := strings.Split(raw, ";")
	normalized := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		name, value, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !ok || name == "" || value == "" || !httpguts.ValidHeaderFieldName(name) || strings.HasPrefix(name, "$") {
			return "", errors.New("session must be a Cookie header containing name=value pairs")
		}
		lowerName := strings.ToLower(name)
		if _, blocked := blockedAttributes[lowerName]; blocked {
			return "", errors.New("paste a Cookie header, not a Set-Cookie value with attributes")
		}
		if _, duplicate := seen[lowerName]; duplicate {
			return "", errors.New("session contains duplicate cookie names")
		}
		if strings.ContainsAny(value, ";\r\n") {
			return "", errors.New("session contains an invalid cookie value")
		}
		seen[lowerName] = struct{}{}
		// Validate discarded pairs too, then retain session names so unrelated
		// tracking cookies are not sent with the server-side usage request.
		if isAllowedOllamaCloudSessionCookie(name) {
			normalized = append(normalized, name+"="+value)
		}
	}
	if len(normalized) == 0 {
		return "", errors.New("session does not contain an allowed Ollama session cookie")
	}
	return strings.Join(normalized, "; "), nil
}

// isAllowedOllamaCloudSessionCookie reports whether a case-sensitive name
// identifies a supported Ollama session cookie. Auth.js and NextAuth names may
// include a nonempty numeric shard suffix, allowing split session cookies to
// retain their order.
func isAllowedOllamaCloudSessionCookie(name string) bool {
	switch name {
	case "wos-session", "__Secure-session", "session", "ollama_session", "__Host-ollama_session":
		return true
	}
	for _, base := range []string{
		"next-auth.session-token",
		"__Secure-next-auth.session-token",
		"authjs.session-token",
		"__Secure-authjs.session-token",
	} {
		if name == base {
			return true
		}
		if suffix, ok := strings.CutPrefix(name, base+"."); ok && suffix != "" {
			validShard := true
			for _, char := range suffix {
				if char < '0' || char > '9' {
					validShard = false
					break
				}
			}
			if validShard {
				return true
			}
		}
	}
	return false
}
