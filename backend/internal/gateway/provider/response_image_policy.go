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

// Responses image policy distinguishes automatic bridge injection from tools
// already declared by a client. A group can disable injection while allowing
// explicit tools, or block them altogether; provider and global bridge
// defaults apply when no relevant group policy overrides them.

// ProviderExtraImagesURLToB64JSON names the provider Extra flag enabling image
// URL backfill for OpenAI API Key image responses.
const ProviderExtraImagesURLToB64JSON = "images_url_to_b64_json"

// ResponseImagePolicy supplies the global default used after Responses image
// overrides.
type ResponseImagePolicy struct {
	// DefaultEnabled supplies bridge injection policy when the relevant group and
	// provider inherit. A nil ResponseImagePolicy receiver resolves that default to false.
	DefaultEnabled bool
}

// ImagesURLToB64JSONEnabled reports whether an OpenAI API Key target enables
// image URL backfill through its Extra flag. Nil targets, other platforms, and
// other credential types return false.
func ImagesURLToB64JSONEnabled(provider *ExecutionProvider) bool {
	return provider != nil && provider.Record.Platform == capability.PlatformOpenAI && provider.Record.Type == capability.ProviderTypeAPIKey && ExecutionProtocolRecord(provider).GetExtraBool(ProviderExtraImagesURLToB64JSON)
}

// GroupResponsesExplicitToolPolicy chooses explicit image-tool handling from
// the group policy. Block strips tools, enabled and disabled allow them, and
// nil or inheriting groups keep the supplied policy.
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

// ResponsesPolicyGroup returns group when the client protocol is Responses,
// Responses WS, or absent. Other declared client protocols return nil so their
// requests do not consume Responses-only image policy.
func ResponsesPolicyGroup(ctx context.Context, group *routing.Group) *routing.Group {
	source, _ := requeststate.ClientProtocolFromContext(ctx)
	if source != "" && source != protocolcore.ProtocolOpenAIResponses && source != protocolcore.ProtocolResponsesWebSocket {
		return nil
	}
	return group
}

// APIKeyGroup returns the API Key's borrowed admitted group, or nil when the
// Key is absent. Callers keep that group stable while applying its request
// policy or create a request-owned snapshot.
func APIKeyGroup(apiKey *apikey.APIKey) *routing.Group {
	if apiKey == nil {
		return nil
	}
	return apiKey.Group
}

// Enabled resolves automatic image bridging from the relevant group, provider
// override, and global default in that order. Enabled groups permit injection;
// disabled or blocked groups reject it, and an absent policy receiver defaults
// to false.
func (s *ResponseImagePolicy) Enabled(ctx context.Context, provider *ExecutionProvider, apiKey *apikey.APIKey) bool {
	if group := ResponsesPolicyGroup(ctx, APIKeyGroup(apiKey)); group != nil {
		switch group.ResponsesImagePolicy {
		case "enabled":
			return true
		case "disabled", "block":
			return false
		}
	}

	// A group that inherits image policy reaches the provider override before the
	// process default, so provider-specific injection policy remains effective for inheritance.
	if override := ExecutionProtocolRecord(provider).CodexImageGenerationBridgeOverride(); override != nil {
		return *override
	}
	return s != nil && s.DefaultEnabled
}
