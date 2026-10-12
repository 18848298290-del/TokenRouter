package openai

import "strings"

// RedactAgentIdentityBody replaces known credentials and AgentAssertion values in a body copy.
// The caller supplies credential lookup; the input bytes remain unchanged.
func RedactAgentIdentityBody(body []byte, credential func(string) string) []byte {
	redacted := string(body)
	for _, key := range []string{
		"agent_private_key",
		"agent_runtime_id",
		"task_id",
		"access_token",
		"refresh_token",
		"id_token",
		"api_key",
		"session_key",
		"cookie",
	} {
		if value := strings.TrimSpace(credential(key)); value != "" {
			redacted = strings.ReplaceAll(redacted, value, "[redacted]")
		}
	}
	const assertionPrefix = "AgentAssertion "
	for offset := 0; offset < len(redacted); {
		relativeStart := strings.Index(redacted[offset:], assertionPrefix)
		if relativeStart < 0 {
			break
		}
		start := offset + relativeStart
		valueStart := start + len(assertionPrefix)
		end := valueStart
		for end < len(redacted) && !strings.ContainsRune(" \t\r\n\"',}", rune(redacted[end])) {
			end++
		}
		redacted = redacted[:valueStart] + "[redacted]" + redacted[end:]
		offset = valueStart + len("[redacted]")
	}
	return []byte(redacted)
}
