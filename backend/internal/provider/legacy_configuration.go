package provider

import (
	"maps"
	"strings"
)

// DeprecatedOpenAIProviderExtraKeys lists obsolete probe fields discarded on input; routing uses protocol
// configuration.
var DeprecatedOpenAIProviderExtraKeys = [...]string{
	"openai_responses_probe_status", "openai_responses_supported",
	"openai_compact_supported", "openai_compact_checked_at",
	"openai_compact_last_status", "openai_compact_last_error",
	"openai_native_compaction_v2_supported", "openai_native_compaction_v2_checked_at",
	"openai_native_compaction_v2_last_status", "openai_native_compaction_v2_last_error",
}

// DiscardDeprecatedProviderExtra removes obsolete extra fields still submitted by older clients.
func DiscardDeprecatedProviderExtra(extra map[string]any) {
	NormalizeLegacyOpenAIProviderExtra(extra)
	DiscardDeprecatedExtra(extra)
}

// NormalizeDeprecatedProviderExtraUpdate prepares a complete replacement of provider extra.
// The boolean requests replacement: an empty object clears extra, while obsolete-only input skips the update.
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

// DiscardDeprecatedExtra removes obsolete billing-probe and long-context fields in place.
func DiscardDeprecatedExtra(extra map[string]any) {
	delete(extra, "upstream_billing_probe")
	delete(extra, "upstream_billing_probe_enabled")
	delete(extra, "openai_long_context_billing_enabled")
}

// NormalizeLegacyOpenAIProviderExtra removes obsolete probe fields and normalizes compaction switches in place.
// Omitted switches stay absent; supplied values other than force_off become force_on, including auto.
// @project-doc docs/interfaces/openai_upstream.md#openai_account_configuration
func NormalizeLegacyOpenAIProviderExtra(extra map[string]any) {
	for _, key := range DeprecatedOpenAIProviderExtraKeys {
		delete(extra, key)
	}
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
