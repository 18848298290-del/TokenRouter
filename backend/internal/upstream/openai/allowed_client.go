package openai

import "strings"

// Provider codex_cli_only_allowed_clients settings select these preset IDs.
// Matching rules are fixed in the registry so configuration cannot broaden them.
const (
	// AllowedClientClaudeCode identifies the Claude Code CLI Codex plugin.
	AllowedClientClaudeCode = "claude_code"
)

// allowedClientRegistry defines the signatures for named client presets.
// The Claude Code plugin initializes app-server with clientInfo.name="Claude Code",
// which supplies both the originator and the "Claude Code/" User-Agent prefix.
// Update this entry if the plugin changes that name.
var allowedClientRegistry = map[string]AllowedClientEntry{
	AllowedClientClaudeCode: {
		Originator: "Claude Code",
		UAContains: []string{"Claude Code/"},
	},
}

// AllowedClientEntry describes an additional non-official Codex client signature.
// Originator must match exactly after normalization. Every UAContains marker
// must be nonblank and present in the User-Agent; an empty list is invalid.
type AllowedClientEntry struct {
	Originator string
	UAContains []string
}

// IsAllowedClientMatch checks the normalized originator and every required UA marker.
// Missing originator or UA requirements make the entry fail closed.
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

// MatchAllowedClients checks whether the headers match any named preset.
// Unknown IDs are ignored; an empty list denies access.
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
