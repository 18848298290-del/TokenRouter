package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const (
	CodexImageGenerationBridgeKey = "codex_image_generation_bridge"

	CodexImageGenerationExplicitToolPolicyKey = "codex_image_generation_explicit_tool_policy"

	CodexImagePolicyAllow = "allow"
	CodexImagePolicyStrip = "strip"
)

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

// stringOverrideFromMap returns the first string value in candidate-key order.
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

// normalizeCodexImageGenerationExplicitToolPolicy accepts legacy remove/drop aliases and allows unknown values.
func normalizeCodexImageGenerationExplicitToolPolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CodexImagePolicyStrip, "remove", "drop":
		return CodexImagePolicyStrip
	default:
		return CodexImagePolicyAllow
	}
}

// CodexImageGenerationBridgeOverride returns the provider's Codex image bridge override.
// Nil inherits the global default; group protocol settings take precedence.
func (r *Record) CodexImageGenerationBridgeOverride() *bool {
	if r == nil || r.Platform != capability.PlatformOpenAI || r.Extra == nil {
		return nil
	}
	if override := boolOverrideFromMap(r.Extra, CodexImageGenerationBridgeKey, "codex_image_generation_bridge_enabled"); override != nil {
		return override
	}
	openaiConfig, _ := r.Extra[capability.PlatformOpenAI].(map[string]any)
	return boolOverrideFromMap(openaiConfig, CodexImageGenerationBridgeKey, "codex_image_generation_bridge_enabled")
}

// CodexImageGenerationExplicitToolPolicy returns the provider's policy for explicit /responses image tools.
// Missing and unknown values allow the tool.
func (r *Record) CodexImageGenerationExplicitToolPolicy() string {
	if r == nil || r.Platform != capability.PlatformOpenAI || r.Extra == nil {
		return CodexImagePolicyAllow
	}
	if policy, ok := stringOverrideFromMap(r.Extra, CodexImageGenerationExplicitToolPolicyKey); ok {
		return normalizeCodexImageGenerationExplicitToolPolicy(policy)
	}
	openaiConfig, _ := r.Extra[capability.PlatformOpenAI].(map[string]any)
	if policy, ok := stringOverrideFromMap(openaiConfig, CodexImageGenerationExplicitToolPolicyKey); ok {
		return normalizeCodexImageGenerationExplicitToolPolicy(policy)
	}
	return CodexImagePolicyAllow
}
