package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// OpenAI OAuth admission rejects known foreign model families before Codex
// forwarding. The filter examines a lowercased model segment while unknown
// names and aliases remain eligible for later whitelist and upstream checks;
// eligibility alone cannot establish execution support.

// oauthForeignModelPrefixes lists model-family prefixes excluded from OpenAI
// OAuth scheduling.
// These known foreign families are filtered before forwarding to Codex.
// Unrecognized names remain eligible for subsequent model and upstream checks.
//
// This list is a rejection filter, not a catalogue of supported models.
var oauthForeignModelPrefixes = []string{
	"deepseek-",
	"glm-",
	"kimi-",
	"moonshot-",
	"qwen-",
	"qwen2-",
	"qwen3-",
	"qwen4-",
	"qwq-",
	"minimax-",
	"gemini-",
	"gemma-",
	"grok-",
	"doubao-",
	"hunyuan-",
	"llama-",
	"llama2-",
	"llama3-",
	"meta-llama",
	"mistral-",
	"mixtral-",
	"baichuan-",
	"ernie-",
	"step-",
	"seed-",
	"yi-",
}

// IsOpenAIOAuthServableModel rejects configured foreign prefixes and bare Kimi
// Code names. Empty and unlisted names return true for later caller
// validation; matching ignores case and examines the final model segment.
func IsOpenAIOAuthServableModel(requestedModel string) bool {
	model := strings.ToLower(capability.LastOpenAIModelSegment(requestedModel))
	// Inspect the path's last segment for eligibility; forwarding and pricing
	// retain the complete name.
	if index := strings.LastIndexByte(model, '/'); index >= 0 {
		model = model[index+1:]
	}
	if model == "" {
		return true // Required-model validation belongs to the caller.
	}
	// Bare Kimi Code names lack the hyphenated family prefixes. Reject those
	// precise IDs separately so the prefix filter cannot admit this known foreign
	// family.
	if model == "k3" || model == "k3-256k" {
		return false
	}
	for _, prefix := range oauthForeignModelPrefixes {
		if strings.HasPrefix(model, prefix) {
			return false
		}
	}
	return true
}
