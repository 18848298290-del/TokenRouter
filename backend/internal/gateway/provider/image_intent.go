package provider

import (
	"strings"

	gatewaymedia "github.com/TokenFlux/TokenRouter/internal/gateway/media"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// ImageIntent binds platform tool parsing to media-owned image intent and permission rules.
func ImageIntent() gatewaymedia.ImageIntentPolicy {
	return gatewaymedia.NewImageIntentPolicy(gatewaymedia.ImageToolRules{
		IsImageType:     openai.IsOpenAIImageGenerationType,
		IsNamespaceName: openai.IsOpenAIImageGenNamespaceName,
		HasTool:         openai.HasOpenAIImageGenerationTool,
		ToolChoice:      openai.OpenAIAnyToolChoiceSelectsImageGeneration,
		FirstString:     openai.FirstNonEmptyString,
	})
}

// ImageIntentForPlatform resolves image intent; Grok ignores passive tool declarations.
func ImageIntentForPlatform(endpoint, model string, body []byte, platform string) bool {
	return ImageIntent().IsImageGenerationIntentForPlatform(endpoint, model, body, strings.EqualFold(strings.TrimSpace(platform), capability.PlatformGrok))
}
