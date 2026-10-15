package openai

import "strings"

// agent_identity_redact.go removes known agent credentials and generated
// assertion tokens from diagnostic response bodies.
// Agent diagnostics can contain stored credentials or a freshly generated
// AgentAssertion value. Redaction replaces known credential substrings and then
// scans assertion tokens independently, so a per-request signature can be
// hidden even when credential lookup does not contain it.

// RedactAgentIdentityBody returns a body copy with known credentials and
// assertion tokens replaced. The credential callback is required and supplies
// each stored value; blank values are ignored. Redaction is textual, so
// embedded occurrences are replaced even outside JSON fields.
//
// Assertions are scanned after stored values, with delimiters marking the token
// end. Advancing past each replacement prevents the inserted redaction label
// from being rescanned. The original byte slice stays available to the caller
// for response handling.
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
	// Assertion values vary per request, so redact their delimited token even
	// when the provider credential lookup has no matching stored signature.
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
