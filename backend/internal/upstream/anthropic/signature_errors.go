package anthropic

import "strings"

// MatchSignaturePatterns reports any nonempty, case-insensitive substring match.
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

// IsThinkingBlockSignatureError recognizes errors eligible for thinking-block
// recovery and returns an optional diagnostic message.
func IsThinkingBlockSignatureError(message string) (bool, string) {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false, ""
	}

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

	// History from another model can contain a thinking block without its
	// required thinking text.
	if strings.Contains(msg, "thinking block must contain") {
		return true, "[SignatureCheck] Detected thinking block missing content error"
	}

	return false, ""
}
