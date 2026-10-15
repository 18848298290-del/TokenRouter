package logredact

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// redact.go redacts recognized secrets from structured payloads and diagnostic
// text.
// Diagnostic input may contain OAuth tokens, passwords, or provider credentials
// inside JSON or plain key/value text. Structured redaction replaces recognized
// values recursively, while plain text uses compiled key patterns and two
// credential-shaped patterns; callers add sensitive field names needed by their
// payloads.

// maxRedactDepth caps traversal of nested map and slice values so recursive
// redaction returns a marker when further descent could continue through deep
// or cyclic data.
const maxRedactDepth = 32

var (
	// defaultSensitiveKeys supplies normalized field names checked during structured
	// traversal. Matching values are masked regardless of their contained type.
	defaultSensitiveKeys = map[string]struct{}{
		"authorization_code": {},
		"code":               {},
		"code_verifier":      {},
		"access_token":       {},
		"refresh_token":      {},
		"id_token":           {},
		"client_secret":      {},
		"password":           {},
	}

	// defaultSensitiveKeyList supplies the same credential names in an ordered list
	// used to construct regular expression alternatives for non-JSON diagnostic text.
	defaultSensitiveKeyList = []string{
		"authorization_code",
		"code",
		"code_verifier",
		"access_token",
		"refresh_token",
		"id_token",
		"client_secret",
		"password",
	}

	// reGOCSPX and reAIza recognize credential-shaped text even when no sensitive
	// field name is present; their replacements retain only a recognizable prefix.
	reGOCSPX = regexp.MustCompile(`GOCSPX-[0-9A-Za-z_-]{24,}`)
	reAIza   = regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)

	// defaultTextRedactPatterns is reused when no extra keys are supplied.
	// extraTextPatternCache publishes compiled patterns under normalized comma-joined
	// key lists so repeated custom-field redaction can reuse its expressions.
	defaultTextRedactPatterns = compileTextRedactPatterns(nil)
	extraTextPatternCache     sync.Map // map[string]*textRedactPatterns
)

// textRedactPatterns holds compiled expressions for the supported sensitive
// key/value text forms.
type textRedactPatterns struct {
	reJSONLike  *regexp.Regexp // quoted JSON-like field form
	reQueryLike *regexp.Regexp // URL query assignment form
	rePlain     *regexp.Regexp // unquoted plain assignment form
}

// RedactMap copies ordinary map[string]any and []any containers while masking
// recognized keys. Nil input returns an allocated empty map; other contained
// value types remain shared or copied by value, and deep recursion is replaced
// by a marker.
func RedactMap(input map[string]any, extraKeys ...string) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	keys := buildKeySet(extraKeys)
	redacted, ok := redactValueWithDepth(input, keys, 0).(map[string]any)
	if !ok {
		// A failed result assertion has no structured output safe to expose. Return
		// an empty map rather than giving diagnostic callers the supplied input.
		return map[string]any{}
	}
	return redacted
}

// RedactJSON parses and re-encodes JSON after masking sensitive fields and
// nested containers. Empty input returns empty text, malformed input returns a
// redaction marker, and an encoding failure returns a generic marker rather
// than the raw payload.
func RedactJSON(raw []byte, extraKeys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	// An undecodable payload cannot be traversed by sensitive field name. Replace
	// it with a marker so diagnostic output does not include the unexamined bytes.
	if err := json.Unmarshal(raw, &value); err != nil {
		return "<non-json payload redacted>"
	}
	keys := buildKeySet(extraKeys)
	redacted := redactValueWithDepth(value, keys, 0)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		// Encoding failure prevents a usable structured result. Keep diagnostic output
		// redacted by returning a marker rather than the supplied raw payload.
		return "<redacted>"
	}
	return string(encoded)
}

// RedactText trims diagnostic text and masks recognized sensitive key/value
// forms or credential patterns. Valid JSON uses structured redaction; other
// text uses expressions for quoted fields, query assignments, and plain
// assignments, which cover their recognized forms rather than arbitrary
// secrets.
func RedactText(input string, extraKeys ...string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	raw := []byte(input)
	if json.Valid(raw) {
		return RedactJSON(raw, extraKeys...)
	}

	patterns := getTextRedactPatterns(extraKeys)

	out := input
	out = reGOCSPX.ReplaceAllString(out, "GOCSPX-***")
	out = reAIza.ReplaceAllString(out, "AIza***")
	out = patterns.reJSONLike.ReplaceAllString(out, `$1***$3`)
	out = patterns.reQueryLike.ReplaceAllString(out, `$1=***`)
	out = patterns.rePlain.ReplaceAllString(out, `$1$2***`)
	return out
}

// compileTextRedactPatterns compiles case-insensitive expressions for built-in
// and supplied sensitive field names. Names are normalized and escaped as
// literal expression alternatives, while each expression matches a particular
// textual value form.
func compileTextRedactPatterns(extraKeys []string) *textRedactPatterns {
	keyAlt := buildKeyAlternation(extraKeys)
	return &textRedactPatterns{
		// JSON-like: "access_token":"..."
		reJSONLike: regexp.MustCompile(`(?i)("(?:` + keyAlt + `)"\s*:\s*")([^"]*)(")`),
		// Query-like: access_token=...
		reQueryLike: regexp.MustCompile(`(?i)\b((?:` + keyAlt + `))=([^&\s]+)`),
		// Plain: access_token: ... / access_token = ...
		rePlain: regexp.MustCompile(`(?i)\b((?:` + keyAlt + `))\b(\s*[:=]\s*)([^,\s]+)`),
	}
}

// getTextRedactPatterns returns reusable expressions for normalized and sorted
// extra keys. Empty normalized input uses the built-in set; custom sets are
// cached under comma-joined names. Names containing commas can alias different
// key sets, so callers use comma-free extra field names for distinct cached
// matching rules.
func getTextRedactPatterns(extraKeys []string) *textRedactPatterns {
	normalizedExtraKeys := normalizeAndSortExtraKeys(extraKeys)
	if len(normalizedExtraKeys) == 0 {
		return defaultTextRedactPatterns
	}

	cacheKey := strings.Join(normalizedExtraKeys, ",")
	if cached, ok := extraTextPatternCache.Load(cacheKey); ok {
		if patterns, ok := cached.(*textRedactPatterns); ok {
			return patterns
		}
	}

	compiled := compileTextRedactPatterns(normalizedExtraKeys)
	// Another caller can publish this key while expressions are being compiled.
	// Use the published set so concurrent lookups reuse one stored cache entry.
	actual, _ := extraTextPatternCache.LoadOrStore(cacheKey, compiled)
	if patterns, ok := actual.(*textRedactPatterns); ok {
		return patterns
	}
	// An unexpected stored type has no usable pattern set. Use this call's
	// compiled expressions so custom fields still receive their requested masking.
	return compiled
}

// normalizeAndSortExtraKeys returns sorted, distinct nonblank normalized
// sensitive field names. Empty input returns nil, while a nonempty input
// containing only blank names returns an allocated empty slice.
func normalizeAndSortExtraKeys(extraKeys []string) []string {
	if len(extraKeys) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(extraKeys))
	keys := make([]string, 0, len(extraKeys))
	for _, key := range extraKeys {
		normalized := normalizeKey(key)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		keys = append(keys, normalized)
	}
	sort.Strings(keys)
	return keys
}

// buildKeyAlternation joins escaped, distinct sensitive key names for regular
// expression matching. Built-in names appear first, and supplied names are
// trimmed and lowercased before deduplication and literal escaping.
func buildKeyAlternation(extraKeys []string) string {
	seen := make(map[string]struct{}, len(defaultSensitiveKeyList)+len(extraKeys))
	keys := make([]string, 0, len(defaultSensitiveKeyList)+len(extraKeys))
	for _, k := range defaultSensitiveKeyList {
		seen[k] = struct{}{}
		keys = append(keys, regexp.QuoteMeta(k))
	}
	for _, k := range extraKeys {
		n := normalizeKey(k)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		keys = append(keys, regexp.QuoteMeta(n))
	}
	return strings.Join(keys, "|")
}

// buildKeySet returns a new lookup map of built-in and supplied sensitive field
// names. Extra names are trimmed and lowercased, and blank extra values
// contribute no matching key.
func buildKeySet(extraKeys []string) map[string]struct{} {
	keys := make(map[string]struct{}, len(defaultSensitiveKeys)+len(extraKeys))
	for k := range defaultSensitiveKeys {
		keys[k] = struct{}{}
	}
	for _, key := range extraKeys {
		normalized := normalizeKey(key)
		if normalized == "" {
			continue
		}
		keys[normalized] = struct{}{}
	}
	return keys
}

// redactValueWithDepth copies recognized JSON-style containers and masks values
// under sensitive names. Depth above maxRedactDepth returns a marker;
// unsupported contained kinds retain their original references, and callers
// supply the normalized key set.
func redactValueWithDepth(value any, keys map[string]struct{}, depth int) any {
	// Further traversal can follow cyclic map or slice references. Bound descent
	// with a marker so redaction terminates while withholding deeper payload values.
	if depth > maxRedactDepth {
		return "<depth limit exceeded>"
	}

	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			if isSensitiveKey(k, keys) {
				out[k] = "***"
				continue
			}
			out[k] = redactValueWithDepth(val, keys, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactValueWithDepth(item, keys, depth+1)
		}
		return out
	default:
		return value
	}
}

// isSensitiveKey reports whether the normalized field name exists in the
// supplied key set.
func isSensitiveKey(key string, keys map[string]struct{}) bool {
	_, ok := keys[normalizeKey(key)]
	return ok
}

// normalizeKey trims surrounding whitespace and lowercases a sensitive field
// name.
func normalizeKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// MaskCredential trims a credential and returns a masked display value for it.
// Empty text stays empty, values through fourteen bytes become four asterisks,
// and longer values retain six leading and four trailing bytes; byte slicing
// can split UTF-8.
func MaskCredential(credential string) string {
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return ""
	}
	// Short credentials provide too little text for a partial identifying preview.
	if len(credential) <= 14 {
		return "****"
	}
	return credential[:6] + "****" + credential[len(credential)-4:]
}
