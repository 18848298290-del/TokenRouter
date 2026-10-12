package provider

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	protocolcore "github.com/TokenFlux/TokenRouter/internal/protocol"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// ProviderExtraImagesURLToB64JSON is the provider setting key for image URL conversion.
const ProviderExtraImagesURLToB64JSON = "images_url_to_b64_json"

// ResponseImagePolicy resolves the group policy, provider override, then global default for image bridging.
type ResponseImagePolicy struct {
	DefaultEnabled bool
}

// ImagesURLToB64JSONEnabled reports whether an OpenAI API Key provider enables URL-to-base64 conversion.
func ImagesURLToB64JSONEnabled(provider *ExecutionProvider) bool {
	return provider != nil && provider.Record.Platform == capability.PlatformOpenAI && provider.Record.Type == capability.ProviderTypeAPIKey && ExecutionProtocolRecord(provider).GetExtraBool(ProviderExtraImagesURLToB64JSON)
}

func GroupResponsesExplicitToolPolicy(group *routing.Group, inherited string) string {
	if group == nil {
		return inherited
	}
	switch group.ResponsesImagePolicy {
	case "block":
		return providercore.CodexImagePolicyStrip
	case "enabled", "disabled":
		return providercore.CodexImagePolicyAllow
	default:
		return inherited
	}
}

func ResponsesPolicyGroup(ctx context.Context, group *routing.Group) *routing.Group {
	source, _ := requeststate.ClientProtocolFromContext(ctx)
	if source != "" && source != protocolcore.ProtocolOpenAIResponses && source != protocolcore.ProtocolResponsesWebSocket {
		return nil
	}
	return group
}

func APIKeyGroup(apiKey *apikey.APIKey) *routing.Group {
	if apiKey == nil {
		return nil
	}
	return apiKey.Group
}

func (s *ResponseImagePolicy) Enabled(ctx context.Context, provider *ExecutionProvider, apiKey *apikey.APIKey) bool {
	if group := ResponsesPolicyGroup(ctx, APIKeyGroup(apiKey)); group != nil {
		switch group.ResponsesImagePolicy {
		case "enabled":
			return true
		case "disabled", "block":
			return false
		}
	}

	if override := ExecutionProtocolRecord(provider).CodexImageGenerationBridgeOverride(); override != nil {
		return *override
	}
	return s != nil && s.DefaultEnabled
}
