package openai

import "strings"

// A configured Codex client preset needs both a matching originator and all
// required User-Agent markers. Blank requirements reject the preset, so a
// stored allowlist cannot accidentally reduce client admission to a single
// caller-controlled header.

// Provider codex_cli_only_allowed_clients settings select these preset IDs.
// Matching rules are fixed in the registry so configuration cannot broaden them.
const (
	// AllowedClientClaudeCode identifies the Claude Code CLI Codex plugin.
	AllowedClientClaudeCode = "claude_code"
)

// allowedClientRegistry defines the paired headers recognized by named presets.
// The Claude Code entry requires its exact originator plus the Claude Code/
// User-Agent substring; these checks identify the configured format alongside
// the gateway's separate API-key authentication.
var allowedClientRegistry = map[string]AllowedClientEntry{
	AllowedClientClaudeCode: {
		Originator: "Claude Code",
		UAContains: []string{"Claude Code/"},
	},
}

// AllowedClientEntry holds the originator and User-Agent markers required by a
// client preset.
type AllowedClientEntry struct {
	// Originator requires an exact trimmed, case-insensitive match; it is checked
	// together with UAContains before a caller is admitted by this preset.
	Originator string
	// UAContains requires every trimmed, case-insensitive substring in User-Agent;
	// an empty list or any blank entry invalidates the preset.
	UAContains []string
}

// IsAllowedClientMatch compares a trimmed, case-folded originator and every
// required User-Agent marker. An empty originator, empty marker list, or blank
// marker rejects the entry; UA matching uses substrings after the same
// normalization.
func IsAllowedClientMatch(userAgent, originator string, entry AllowedClientEntry) bool {
	wantOriginator := normalizeCodexClientHeader(entry.Originator)
	if wantOriginator == "" {
		return false
	}
	if normalizeCodexClientHeader(originator) != wantOriginator {
		return false
	}
	// Require a UA signature as well as the caller-controlled originator.
	if len(entry.UAContains) == 0 {
		return false
	}
	ua := normalizeCodexClientHeader(userAgent)
	for _, marker := range entry.UAContains {
		normalizedMarker := normalizeCodexClientHeader(marker)
		if normalizedMarker == "" {
			// A blank marker invalidates the preset, even if other markers match.
			return false
		}
		if !strings.Contains(ua, normalizedMarker) {
			return false
		}
	}
	return true
}

// MatchAllowedClients reports whether either header pair matches a registered
// preset ID. IDs are trimmed and case-folded, unknown IDs are skipped, and an
// empty or entirely unknown list rejects the request.
func MatchAllowedClients(userAgent, originator string, clientIDs []string) bool {
	for _, id := range clientIDs {
		entry, ok := allowedClientRegistry[normalizeCodexClientHeader(id)]
		if !ok {
			continue
		}
		if IsAllowedClientMatch(userAgent, originator, entry) {
			return true
		}
	}
	return false
}
