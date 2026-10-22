package moderation

import (
	"regexp"
	"strings"
)

// redact.go redacts recognized credentials and URLs from moderation summary
// text.
// Administrator summaries remove recognized URLs and credential-shaped strings
// before excerpt truncation. Redaction is a sequence of regular-expression
// substitutions, so later patterns operate on the text produced by earlier
// patterns. Labels such as token assignments and bearer prefixes remain visible
// while their matched values become a marker, helping a reviewer interpret the
// excerpt.
//
// The same patterns also cover long hexadecimal values, encoded-looking
// strings, and UUIDs. They can redact ordinary identifiers and leave unfamiliar
// secret formats visible. Summary filtering therefore has a narrower role than
// the full-input retention controls applied by buildStructuredLog and the raw
// Cyber review fields.

// contentModerationSecretPatterns applies URL, labeled credential, bearer,
// token, long-string, and UUID substitutions in order for excerpt redaction.
var contentModerationSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>，。；、]+`),
	regexp.MustCompile(`(?i)\b((?:api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|id[_-]?token|session[_-]?token|token|session|cookie|set[_-]?cookie|authorization|bearer|password|passwd|pwd|secret|client[_-]?secret|private[_-]?key)\s*[:=]\s*)(["']?)[^"'\s,;，。；、]{6,}`),
	regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9._~+/=-]{12,}`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
	regexp.MustCompile(`(?i)\b(?:sk|sk-proj|sk-ant|sess|rk|pk|ak|api|key|token|secret)[_-][A-Za-z0-9._~+/=-]{12,}\b`),
	regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`),
	regexp.MustCompile(`\b[A-Za-z0-9_-]{48,}\b`),
	regexp.MustCompile(`\b[A-Za-z0-9+/]{48,}={0,2}\b`),
	regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`),
}

// redactContentModerationSecrets replaces recognized secret-like text for
// administrator summaries and trims surrounding whitespace. The patterns run in
// list order, so each pass receives earlier substitutions rather than the
// unmodified text. Labeled credential and bearer matches retain their
// descriptive prefixes while replacing the value; other matches become a
// redaction marker.
//
// This is pattern-based filtering: it can replace ordinary long identifiers and
// does not prove every possible credential has been removed. Complete input
// items and Cyber raw excerpts follow their own retention rules.
func redactContentModerationSecrets(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	out := text
	// Retain captured credential labels during replacement so the summary can show
	// what kind of value was removed while each later pattern sees the redacted text.
	for idx, pattern := range contentModerationSecretPatterns {
		switch idx {
		case 1:
			out = pattern.ReplaceAllString(out, `${1}${2}[REDACTED]`)
		case 2:
			out = pattern.ReplaceAllString(out, `${1}[REDACTED]`)
		default:
			out = pattern.ReplaceAllString(out, `[REDACTED]`)
		}
	}
	return out
}
