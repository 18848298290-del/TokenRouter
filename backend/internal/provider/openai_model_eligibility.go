package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// oauthForeignModelPrefixes lists model-family prefixes excluded from OpenAI OAuth scheduling.
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

// IsOpenAIOAuthServableModel rejects known foreign model families for OpenAI OAuth.
// Unknown names and custom aliases remain eligible for later validation.
// A true result alone does not establish upstream support.
func IsOpenAIOAuthServableModel(requestedModel string) bool {
	model := strings.ToLower(capability.LastOpenAIModelSegment(requestedModel))
	// Inspect the path's last segment for eligibility; forwarding and pricing retain the complete name.
	if index := strings.LastIndexByte(model, '/'); index >= 0 {
		model = model[index+1:]
	}
	if model == "" {
		return true // Required-model validation belongs to the caller.
	}
	// Bare Kimi Code IDs lack the family prefix used by the filter below.
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
