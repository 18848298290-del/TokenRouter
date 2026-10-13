package provider

import (
	"strings"
)

// OpenAI OAuth and Setup Token providers can select which outgoing Codex
// identifiers are tied to a provider seed. Missing, malformed, or unknown mode
// values select off, so a saved provider stays on client identifiers until
// administration opts in.
//
// Device, session, and full modes each require the system-managed seed. Mode
// parsing and provider eligibility are evaluated separately, allowing Extra
// patch writers to decide when to retain or generate a seed while request
// readers return off for other provider types. Request construction separately
// validates the stored seed before applying a selected mode.

// Fingerprint mode values identify the outgoing identifiers rewritten per
// provider; the Extra keys store the administrator selection and its
// system-managed seed.
const (
	// CodexFingerprintOff passes client identifiers through unchanged.
	// This is the default until an administrator enables a fingerprint mode.
	CodexFingerprintOff CodexFingerprintMode = "off"
	// CodexFingerprintDevice gives installation_id a stable provider-specific value.
	// Client sessions remain distinct under the shared installation.
	CodexFingerprintDevice CodexFingerprintMode = "device"
	// CodexFingerprintSession gives installation_id and session_id stable
	// provider-specific values.
	// It derives thread_id from the client session ID, or uses the provider
	// session ID when absent.
	CodexFingerprintSession CodexFingerprintMode = "session"
	// CodexFingerprintFull gives installation_id, session_id, and thread_id stable
	// provider-specific values.
	CodexFingerprintFull CodexFingerprintMode = "full"

	CodexFingerprintModeExtraKey = "codex_fingerprint_mode" // administrator mode selection key
	CodexFingerprintSeedExtraKey = "codex_fingerprint_seed" // system-managed provider seed key
)

// CodexFingerprintMode selects the outgoing Codex identifiers rewritten for
// eligible providers. Device mode shares installation identity, session mode
// also shares a provider session while deriving threads from client sessions,
// and full mode shares the provider thread. Rewriting uses a valid provider
// seed; installation identity prefers a configured OpenAI OAuth device ID.
type CodexFingerprintMode string

// CodexFingerprintModeFromExtra reads a trimmed mode string from Extra. Missing
// values, nonstrings, and unknown or differently cased names return off,
// keeping fingerprint changes an administrator opt-in.
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

// CodexFingerprintModeRequiresSeed reports whether a recognized mode needs a
// provider seed. Device, session, and full return true; off and unknown values
// leave seed generation unnecessary for that setting.
func CodexFingerprintModeRequiresSeed(mode CodexFingerprintMode) bool {
	switch mode {
	case CodexFingerprintDevice, CodexFingerprintSession, CodexFingerprintFull:
		return true
	default:
		return false
	}
}

// ShouldEnsureCodexFingerprintSeedForExtraUpdates reports whether an Extra
// patch requests a mode needing a seed. When true, the storage update retains a
// valid stored seed or generates one under its write lock.
func ShouldEnsureCodexFingerprintSeedForExtraUpdates(updates map[string]any) bool {
	if updates == nil {
		return false
	}
	return CodexFingerprintModeRequiresSeed(CodexFingerprintModeFromExtra(updates))
}

// GetCodexFingerprintMode reads the configured mode for an OpenAI OAuth or
// Setup Token record. Nil records and other platform/type combinations return
// off even when their Extra contains a recognized mode.
func (r *Record) GetCodexFingerprintMode() CodexFingerprintMode {
	if r == nil || !r.IsOpenAIOAuthLike() {
		return CodexFingerprintOff
	}
	return CodexFingerprintModeFromExtra(r.Extra)
}
