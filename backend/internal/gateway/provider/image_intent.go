package provider

import (
	"strings"

	gatewaymedia "github.com/TokenFlux/TokenRouter/internal/gateway/media"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// Image permission checks use media-owned intent rules together with OpenAI
// tool recognition. Platform selection gives Grok its stricter intent
// interpretation, so a passive tool declaration can be distinguished from a
// request that actually selects image generation.

// ImageIntent binds OpenAI tool recognition to the gateway media image-intent
// policy.
func ImageIntent() gatewaymedia.ImageIntentPolicy {
	return gatewaymedia.NewImageIntentPolicy(gatewaymedia.ImageToolRules{
		IsImageType:     openai.IsOpenAIImageGenerationType,
		IsNamespaceName: openai.IsOpenAIImageGenNamespaceName,
		HasTool:         openai.HasOpenAIImageGenerationTool,
		ToolChoice:      openai.OpenAIAnyToolChoiceSelectsImageGeneration,
		FirstString:     openai.FirstNonEmptyString,
	})
}

// ImageIntentForPlatform resolves image intent using endpoint, model, body,
// and the normalized platform name. Grok activates the media policy's stricter
// interpretation, where passive image-tool declarations alone do not select
// image generation.
func ImageIntentForPlatform(endpoint, model string, body []byte, platform string) bool {
	return ImageIntent().IsImageGenerationIntentForPlatform(endpoint, model, body, strings.EqualFold(strings.TrimSpace(platform), capability.PlatformGrok))
}
