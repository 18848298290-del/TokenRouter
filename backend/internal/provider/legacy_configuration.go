package provider

import (
	"maps"
	"strings"
)

// legacy_configuration.go normalizes legacy OpenAI switches and removes
// deprecated provider settings from administrative input.
// Older administrator clients can submit obsolete OpenAI probe fields alongside
// still-supported compaction switches. The compatibility input path discards
// probe observations and maps every supplied compaction value except force_off
// to force_on, keeping omitted switches absent.
//
// Replacement input needs a separate presence result: nil skips the write, an
// explicitly empty map requests clearing Extra, and a nonempty object made
// entirely of discarded fields also skips the write. A shallow copy protects
// the caller's top-level map while retaining nested values for later ownership
// handling.

// DeprecatedOpenAIProviderExtraKeys lists obsolete probe fields discarded on
// input; provider routing reads the current protocol configuration.
var DeprecatedOpenAIProviderExtraKeys = [...]string{
	"openai_responses_probe_status", "openai_responses_supported",
	"openai_compact_supported", "openai_compact_checked_at",
	"openai_compact_last_status", "openai_compact_last_error",
	"openai_native_compaction_v2_supported", "openai_native_compaction_v2_checked_at",
	"openai_native_compaction_v2_last_status", "openai_native_compaction_v2_last_error",
}

// DiscardDeprecatedProviderExtra normalizes legacy OpenAI switches and removes
// obsolete probe and billing keys in place. Nil maps are accepted, and retained
// keys stay on the same caller-owned map.
func DiscardDeprecatedProviderExtra(extra map[string]any) {
	NormalizeLegacyOpenAIProviderExtra(extra)
	DiscardDeprecatedExtra(extra)
}

// NormalizeDeprecatedProviderExtraUpdate returns normalized Extra and whether
// it requests replacement. Nil input skips replacement; an explicitly empty map
// requests clearing the object.
//
// A nonempty object that becomes empty after obsolete-field removal also skips
// replacement, so a stale client sending just retired probe keys cannot erase
// unrelated saved configuration. Otherwise the shallow copy is returned for
// replacement. Nested values remain shared with the caller and are copied by
// the owning configuration path as needed.
func NormalizeDeprecatedProviderExtraUpdate(extra map[string]any) (map[string]any, bool) {
	if extra == nil {
		return nil, false
	}
	normalized := maps.Clone(extra)
	DiscardDeprecatedProviderExtra(normalized)
	if len(extra) > 0 && len(normalized) == 0 {
		return nil, false
	}
	return normalized, true
}

// DiscardDeprecatedExtra deletes retired billing-probe and long-context keys
// from Extra in place. Nil succeeds through ordinary map deletion, and
// unrelated entries remain on the same map.
func DiscardDeprecatedExtra(extra map[string]any) {
	delete(extra, "upstream_billing_probe")
	delete(extra, "upstream_billing_probe_enabled")
	delete(extra, "openai_long_context_billing_enabled")
}

// NormalizeLegacyOpenAIProviderExtra removes retired probe fields and
// normalizes supplied compaction switches in place. Omitted switches remain
// absent; trimming and case-insensitive matching recognize force_off, while
// every other supplied representation becomes force_on.
// @project-doc docs/interfaces/openai_upstream.md#openai_account_configuration
func NormalizeLegacyOpenAIProviderExtra(extra map[string]any) {
	for _, key := range DeprecatedOpenAIProviderExtraKeys {
		delete(extra, key)
	}
	// Legacy auto and malformed supplied values select the enabled compatibility
	// behavior. Only the recognized disabling value survives as force_off, while
	// missing keys retain omission for the later replacement decision.
	for _, key := range []string{"openai_compact_mode", OpenAINativeCompactionV2ModeExtraKey} {
		if raw, exists := extra[key]; exists {
			mode, _ := raw.(string)
			if strings.EqualFold(strings.TrimSpace(mode), OpenAICompactModeForceOff) {
				extra[key] = OpenAICompactModeForceOff
			} else {
				extra[key] = OpenAICompactModeForceOn
			}
		}
	}
}
