package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// OpenAI provider image settings accept current and historical keys at the top
// of Extra before nested openai configuration. Typed presence matters: a
// top-level false or empty string wins over nested values, while absent or
// incompatible types permit the next configured representation.
//
// Bridge overrides return an independent boolean pointer so consumers can
// distinguish inherited policy from a supplied false. Explicit tool policy
// maps strip, remove, and drop to stripping and treats other text as allow.
// ResponseImagePolicy.Enabled applies group policy before the provider bridge
// override and its process default; these readers return the provider choice
// while that consumer owns the final request decision.

// Codex image constants name provider Extra overrides and the allow/strip
// values consumed when image bridging and explicit tool policy are resolved.
const (
	// CodexImageGenerationBridgeKey names the provider Extra switch consumed
	// by automatic Responses image injection policy after group-level selection.
	CodexImageGenerationBridgeKey = "codex_image_generation_bridge"

	// CodexImageGenerationExplicitToolPolicyKey names the provider Extra rule
	// consumed when request adapters filter explicitly supplied Responses image
	// tools.
	CodexImageGenerationExplicitToolPolicyKey = "codex_image_generation_explicit_tool_policy"

	// CodexImagePolicyAllow permits the explicit image tool under this provider
	// rule.
	CodexImagePolicyAllow = "allow"
	// CodexImagePolicyStrip removes the explicit image tool under this provider
	// rule.
	CodexImagePolicyStrip = "strip"
)

// boolOverrideFromMap copies the first typed boolean in key order into a
// pointer. False is a present override, wrong types are skipped, and nil
// reports no applicable value; keep the source map stable.
func boolOverrideFromMap(values map[string]any, keys ...string) *bool {
	if values == nil {
		return nil
	}
	for _, key := range keys {
		if v, ok := values[key].(bool); ok {
			copy := v
			return &copy
		}
	}
	return nil
}

// stringOverrideFromMap returns the first typed string in key order and its
// presence. Empty string is present, wrong types advance to later keys, and
// the source map remains caller-owned.
func stringOverrideFromMap(values map[string]any, keys ...string) (string, bool) {
	if values == nil {
		return "", false
	}
	for _, key := range keys {
		if v, ok := values[key].(string); ok {
			return v, true
		}
	}
	return "", false
}

// normalizeCodexImageGenerationExplicitToolPolicy trims and lowercases text,
// mapping strip/remove/drop to strip. Empty or unknown values return allow, so
// a supplied unrecognized top-level string can override a nested strip
// setting.
func normalizeCodexImageGenerationExplicitToolPolicy(value string) string {
	// Compatible strip/remove/drop spellings select the same request tool action.
	// Other supplied text selects allow and can override a nested strip choice.
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CodexImagePolicyStrip, "remove", "drop":
		return CodexImagePolicyStrip
	default:
		return CodexImagePolicyAllow
	}
}

// CodexImageGenerationBridgeOverride reads the OpenAI provider bridge choice
// into an independent pointer. Nil, other platforms, or missing Extra yield
// nil for consumer inheritance.
//
// The current top-level key precedes its historical enabled alias, then the
// same key order is checked within nested openai values. The first typed
// boolean wins even when false, while incompatible values allow fallback.
// ResponseImagePolicy.Enabled checks group configuration first and uses this
// provider result before its global default; callers keep the borrowed record
// and maps stable while reading.
func (r *Record) CodexImageGenerationBridgeOverride() *bool {
	if r == nil || r.Platform != capability.PlatformOpenAI || r.Extra == nil {
		return nil
	}
	if override := boolOverrideFromMap(r.Extra, CodexImageGenerationBridgeKey, "codex_image_generation_bridge_enabled"); override != nil {
		return override
	}
	// A missing typed top-level override permits the nested compatibility
	// object to supply policy. A non-map object behaves as absent configuration.
	openaiConfig, _ := r.Extra[capability.PlatformOpenAI].(map[string]any)
	return boolOverrideFromMap(openaiConfig, CodexImageGenerationBridgeKey, "codex_image_generation_bridge_enabled")
}

// CodexImageGenerationExplicitToolPolicy selects the OpenAI provider rule for
// explicit image tools. Nil, other platforms, or unavailable configuration
// returns allow.
//
// A typed top-level string wins before nested openai configuration, including
// empty or unknown text that normalizes to allow. Only strip and its
// remove/drop aliases select stripping. Wrong types permit fallback rather
// than supplying an override, keeping compatibility with both configuration
// representations. Callers keep the borrowed Extra maps stable while the rule
// is read and apply the returned policy in their request adapter.
func (r *Record) CodexImageGenerationExplicitToolPolicy() string {
	if r == nil || r.Platform != capability.PlatformOpenAI || r.Extra == nil {
		return CodexImagePolicyAllow
	}
	if policy, ok := stringOverrideFromMap(r.Extra, CodexImageGenerationExplicitToolPolicyKey); ok {
		return normalizeCodexImageGenerationExplicitToolPolicy(policy)
	}
	// Incompatible or absent top-level text permits the nested compatibility
	// object to select policy. A present empty string already returned allow above.
	openaiConfig, _ := r.Extra[capability.PlatformOpenAI].(map[string]any)
	if policy, ok := stringOverrideFromMap(openaiConfig, CodexImageGenerationExplicitToolPolicyKey); ok {
		return normalizeCodexImageGenerationExplicitToolPolicy(policy)
	}
	return CodexImagePolicyAllow
}
