package audit

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

// redact.go masks sensitive audit fields and limits the bodies retained in
// audit records.
// Audit bodies may contain provider credentials even when their field names use
// different separators or casing. Normalized key matching masks those values
// recursively, while non-JSON and oversized bodies become size-bearing placeholders
// so request capture can remain bounded and avoid retaining uninspectable secrets.

// Redaction limits replace sensitive values and stop recursion before deeply
// nested input can produce an unbounded traversal of an audit request body.
const (
	auditRedactedPlaceholder = "***" // sensitive JSON value replacement
	auditRedactMaxDepth      = 24    // maximum inspected nesting depth
)

// auditBodySensitiveSubstrings matches normalized request-body keys.
// Any match redacts the entire value, including new_password, secret_access_key, and temp_token.
var auditBodySensitiveSubstrings = []string{
	"password", "passwd", "secret", "token",
	"apikey", "accesskey", "privatekey",
	"otp", "credentialvalue",
	"sessionkey", "serviceprovider",
}

// Redactor matches sensitive JSON keys using an immutable normalized key set.
type Redactor struct {
	// exact combines built-in names and caller-supplied module keys. NewRedactor
	// finishes populating this map before middleware shares the redactor across requests.
	exact map[string]struct{}
}

// auditNormalizeBodyKey lowercases keys and removes separators.
// This gives private_key, privateKey, and privatekey the same representation,
// so matching also covers request credentials whose names omit separators.
func auditNormalizeBodyKey(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range strings.ToLower(strings.TrimSpace(key)) {
		// Ignore credential-name separators so module-supplied snake_case keys also match
		// camelCase request-body fields after lowercasing, keeping both naming forms under one rule.
		switch r {
		case '_', '-', '.', ' ':
			continue
		default:
			_, _ = b.WriteRune(r)
		}
	}
	return b.String()
}

// NewRedactor combines built-in credential names with the supplied module keys.
// Keys are normalized into a new set, so later changes to extraKeys do not affect
// the redactor shared by HTTP middleware.
func NewRedactor(extraKeys []string) *Redactor {
	builtin := []string{"code", "codes", "pin", "cvv", "authorization", "cookie", "x-api-key", "credential", "key", "proxy_key", "custom_key", "session"}
	set := make(map[string]struct{}, len(builtin)+len(extraKeys))
	for _, k := range append(builtin, extraKeys...) {
		set[auditNormalizeBodyKey(k)] = struct{}{}
	}
	return &Redactor{exact: set}
}

// IsSensitiveKey reports whether the normalized key matches a sensitive name or
// contains a configured sensitive substring. Callers use the result to replace
// the complete value, including any nested object beneath that key.
func (r *Redactor) IsSensitiveKey(key string) bool {
	k := auditNormalizeBodyKey(key)
	if _, ok := r.exact[k]; ok {
		return true
	}
	for _, sub := range auditBodySensitiveSubstrings {
		if strings.Contains(k, sub) {
			return true
		}
	}
	return false
}

// RedactBody returns bounded request text suitable for audit storage after masking
// sensitive JSON fields. Empty bodies return an empty string; malformed, non-JSON,
// and oversized input returns an omission placeholder. Encoded output may be cut
// at the storage byte limit and is then marked as truncated.
func (r *Redactor) RedactBody(raw []byte, contentType string) string {
	if len(raw) == 0 {
		return ""
	}
	if len(raw) > AuditRequestBodyCaptureLimit {
		// The middleware may have truncated raw; report the capture limit as a lower bound on body size.
		return "<body omitted: exceeds " + strconv.Itoa(AuditRequestBodyCaptureLimit) + " bytes>"
	}
	ct := strings.ToLower(contentType)
	if !strings.Contains(ct, "json") || !json.Valid(raw) {
		// Text redaction can miss secrets in forms and other non-JSON bodies, so omit them entirely.
		return "<non-json body omitted: " + strconv.Itoa(len(raw)) + " bytes, content-type=" + strings.TrimSpace(contentType) + ">"
	}

	var value any
	// Request identifiers can exceed float64 precision. Preserve their decimal
	// spelling so audit records and administrator confirmation previews name the actual requested objects.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		// An unexpected decode failure still omits the raw bytes, because recording
		// them would bypass the sensitive-key traversal performed on decoded values.
		return "<unparsable body omitted>"
	}
	redacted := r.redactValue(value, 0)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		// Keep a redaction marker on encoding failure so the audit record can be
		// retained without substituting an unredacted copy of the original request.
		return "<redacted>"
	}
	out := string(encoded)
	if len(out) > auditRequestBodyMaxBytes {
		// The truncation marker distinguishes a stored prefix from a complete body.
		out = out[:auditRequestBodyMaxBytes] + "...<truncated>"
	}
	return out
}

// redactValue copies decoded objects and arrays while masking sensitive-key
// values. Scalar values pass through, and recursion beyond the configured depth
// replaces the entire remaining subtree with an explanatory placeholder.
func (r *Redactor) redactValue(value any, depth int) any {
	if depth > auditRedactMaxDepth {
		// Omit the remaining subtree once the recursion bound is exceeded, so deeper
		// credentials cannot escape inspection through an unchecked return of the input.
		return "<depth limit exceeded>"
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			if r.IsSensitiveKey(k) {
				out[k] = auditRedactedPlaceholder
				continue
			}
			out[k] = r.redactValue(item, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = r.redactValue(item, depth+1)
		}
		return out
	default:
		return value
	}
}

// MaskAuditCredential returns the shared log-redaction display form of a credential.
// Whitespace is trimmed first; empty credentials stay empty and short credentials
// become a mask. Longer credentials expose the prefix and suffix selected by
// logredact.MaskCredential.
func MaskAuditCredential(credential string) string { return logredact.MaskCredential(credential) }

// RedactAuditQuery masks supported credential names in trimmed raw URL query text.
// An empty query returns an empty string; matching and value masking follow
// logredact.RedactText rather than parsing the query into decoded URL parameters.
func RedactAuditQuery(rawQuery string) string {
	rawQuery = strings.TrimSpace(rawQuery)
	if rawQuery == "" {
		return ""
	}
	return logredact.RedactText(rawQuery, "api_key", "apikey", "token", "secret", "key")
}
