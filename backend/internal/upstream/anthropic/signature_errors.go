package anthropic

import "strings"

// Thinking recovery is selected from upstream error text using configurable
// substrings and built-in message patterns. Broad signature matches and several
// malformed-history cases report eligibility to the exchange caller, which
// separately controls whether a repair attempt can run.

// MatchSignaturePatterns reports a case-insensitive substring match for any
// nonblank configured pattern. Pattern whitespace is trimmed, empty patterns
// are skipped, and arbitrary response bytes are interpreted as text for
// matching.
func MatchSignaturePatterns(respBody []byte, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	bodyLower := strings.ToLower(string(respBody))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(bodyLower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// IsThinkingBlockSignatureError classifies broad signature and
// malformed-thinking history messages for exchange recovery. Blank or unmatched
// text returns false and empty diagnostic; signature matches also return empty
// diagnostic, while other recognized families provide a fixed message.
func IsThinkingBlockSignatureError(message string) (bool, string) {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false, ""
	}

	// These textual families admit the exchange's history repair path, including
	// broad signature mentions and empty content; the exchange separately decides
	// retry eligibility and attempt limits.
	// Signature errors include invalid thinking signatures and nested signature paths.
	if strings.Contains(msg, "signature") {
		return true, ""
	}

	// Match errors expecting thinking or redacted_thinking before other content.
	if strings.Contains(msg, "expected") && (strings.Contains(msg, "thinking") || strings.Contains(msg, "redacted_thinking")) {
		return true, "[SignatureCheck] Detected thinking block type error"
	}

	// Match rejection of modified thinking blocks in assistant history.
	if strings.Contains(msg, "cannot be modified") && (strings.Contains(msg, "thinking") || strings.Contains(msg, "redacted_thinking")) {
		return true, "[SignatureCheck] Detected thinking block modification error"
	}

	// Filtering or empty client text can leave messages without valid content.
	if strings.Contains(msg, "non-empty content") || strings.Contains(msg, "empty content") ||
		strings.Contains(msg, "content blocks must be non-empty") {
		return true, "[SignatureCheck] Detected empty content error"
	}

	// A missing-content message is another malformed-history signal admitted by
	// this classifier, allowing the exchange to consider the same repair path.
	if strings.Contains(msg, "thinking block must contain") {
		return true, "[SignatureCheck] Detected thinking block missing content error"
	}

	return false, ""
}
