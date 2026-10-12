package audit

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
)

const (
	auditRedactedPlaceholder = "***"
	auditRedactMaxDepth      = 24
)

// auditBodySensitiveSubstrings matches normalized request-body keys.
// Any match redacts the entire value, including new_password, secret_access_key, and temp_token.
var auditBodySensitiveSubstrings = []string{
	"password", "passwd", "secret", "token",
	"apikey", "accesskey", "privatekey",
	"otp", "credentialvalue",
	"sessionkey", "serviceprovider",
}

// Redactor holds normalized sensitive keys and is shared read-only after construction.
// NewRedactor combines built-in keys with module-owned provider and payment key
// lists supplied by the caller, keeping redaction consistent with credential fields.
type Redactor struct{ exact map[string]struct{} }

// auditNormalizeBodyKey lowercases keys and removes separators.
// This gives private_key, privateKey, and privatekey the same representation,
// so matching also covers credentials whose names omit separators.
func auditNormalizeBodyKey(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range strings.ToLower(strings.TrimSpace(key)) {
		switch r {
		case '_', '-', '.', ' ':
			continue
		default:
			_, _ = b.WriteRune(r)
		}
	}
	return b.String()
}

func NewRedactor(extraKeys []string) *Redactor {
	builtin := []string{"code", "codes", "pin", "cvv", "authorization", "cookie", "x-api-key", "credential", "key", "proxy_key", "custom_key", "session"}
	set := make(map[string]struct{}, len(builtin)+len(extraKeys))
	for _, k := range append(builtin, extraKeys...) {
		set[auditNormalizeBodyKey(k)] = struct{}{}
	}
	return &Redactor{exact: set}
}

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

// RedactBody prepares request bodies for audit storage:
//   - JSON values under sensitive keys are redacted recursively; other fields remain visible.
//   - Invalid JSON, non-JSON, and bodies above the capture limit are replaced with placeholders.
//   - Encoded output above the storage limit is truncated and marked.
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
	if err := json.Unmarshal(raw, &value); err != nil {
		return "<unparsable body omitted>"
	}
	redacted := r.redactValue(value, 0)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return "<redacted>"
	}
	out := string(encoded)
	if len(out) > auditRequestBodyMaxBytes {
		out = out[:auditRequestBodyMaxBytes] + "...<truncated>"
	}
	return out
}

func (r *Redactor) redactValue(value any, depth int) any {
	if depth > auditRedactMaxDepth {
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

func MaskAuditCredential(credential string) string { return logredact.MaskCredential(credential) }

// RedactAuditQuery masks credential fields in the raw URL query.
func RedactAuditQuery(rawQuery string) string {
	rawQuery = strings.TrimSpace(rawQuery)
	if rawQuery == "" {
		return ""
	}
	return logredact.RedactText(rawQuery, "api_key", "apikey", "token", "secret", "key")
}
