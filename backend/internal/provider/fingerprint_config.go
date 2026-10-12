package provider

import (
	"strings"
)

const (
	// CodexFingerprintOff passes client identifiers through unchanged.
	// This is the default until an administrator enables a fingerprint mode.
	CodexFingerprintOff CodexFingerprintMode = "off"
	// CodexFingerprintDevice gives installation_id a stable provider-specific value.
	// Client sessions remain distinct under the shared installation.
	CodexFingerprintDevice CodexFingerprintMode = "device"
	// CodexFingerprintSession gives installation_id and session_id stable provider-specific values.
	// It derives thread_id from the client session ID, or uses the provider session ID when absent.
	CodexFingerprintSession CodexFingerprintMode = "session"
	// CodexFingerprintFull gives installation_id, session_id, and thread_id stable provider-specific values.
	CodexFingerprintFull CodexFingerprintMode = "full"

	CodexFingerprintModeExtraKey = "codex_fingerprint_mode"
	CodexFingerprintSeedExtraKey = "codex_fingerprint_seed"
)

// CodexFingerprintMode selects which outgoing OpenAI OAuth identifiers are shared per provider.
// Clients sharing a provider may send different installation_id, session_id, and thread_id values.
// Rewritten identifiers use the provider seed; installation_id prefers an explicitly configured device ID.
type CodexFingerprintMode string

func CodexFingerprintModeFromExtra(extra map[string]any) CodexFingerprintMode {
	if extra == nil {
		return CodexFingerprintOff
	}
	raw, _ := extra[CodexFingerprintModeExtraKey].(string)
	switch CodexFingerprintMode(strings.TrimSpace(raw)) {
	case CodexFingerprintOff, CodexFingerprintDevice, CodexFingerprintSession, CodexFingerprintFull:
		return CodexFingerprintMode(strings.TrimSpace(raw))
	default:
		return CodexFingerprintOff
	}
}

func CodexFingerprintModeRequiresSeed(mode CodexFingerprintMode) bool {
	switch mode {
	case CodexFingerprintDevice, CodexFingerprintSession, CodexFingerprintFull:
		return true
	default:
		return false
	}
}

// ShouldEnsureCodexFingerprintSeedForExtraUpdates reports whether the update enables fingerprinting.
// When true, the repository must atomically retain or generate the system-managed provider seed.
func ShouldEnsureCodexFingerprintSeedForExtraUpdates(updates map[string]any) bool {
	if updates == nil {
		return false
	}
	return CodexFingerprintModeRequiresSeed(CodexFingerprintModeFromExtra(updates))
}

// GetCodexFingerprintMode returns the mode for OpenAI OAuth-like providers and off for other types.
func (r *Record) GetCodexFingerprintMode() CodexFingerprintMode {
	if r == nil || !r.IsOpenAIOAuthLike() {
		return CodexFingerprintOff
	}
	return CodexFingerprintModeFromExtra(r.Extra)
}
