package httpapi

import (
	"maps"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// ProtocolProviderProfile describes native protocol choices for a provider type and auth mode.
type ProtocolProviderProfile struct {
	Platform  string                `json:"platform"`
	Type      string                `json:"type"`
	AuthMode  string                `json:"auth_mode"`
	Protocols []protocol.ProtocolID `json:"protocols"`
}

type ProtocolGroupProfile struct {
	Protocols        []protocol.ProtocolID                         `json:"protocols"`
	Defaults         []protocol.ProtocolID                         `json:"defaults"`
	FallbackTargets  map[protocol.ProtocolID][]protocol.ProtocolID `json:"fallback_targets"`
	DefaultFallbacks map[protocol.ProtocolID][]protocol.ProtocolID `json:"default_fallbacks"`
}

// ProtocolCatalogResponse contains the protocol, provider, and group capability catalogue.
type ProtocolCatalogResponse struct {
	Protocols           []Protocol                `json:"protocols"`
	Providers           []ProtocolProviderProfile `json:"providers"`
	Groups              []ProtocolGroupProfile    `json:"groups"`
	AuxiliaryOperations []AuxiliaryOperation      `json:"auxiliary_operations"`
}

// AuxiliaryOperation associates a resource operation with its protocol and authorization rule.
type AuxiliaryOperation struct {
	Operation     string              `json:"operation"`
	Protocol      protocol.ProtocolID `json:"protocol,omitempty"`
	Authorization string              `json:"authorization"`
}

// Protocol defines the fields and JSON field order of an admin catalogue entry.
type Protocol struct {
	ID           protocol.ProtocolID `json:"id"`
	Name         string              `json:"name"`
	Endpoint     string              `json:"endpoint"`
	UpstreamOnly bool                `json:"upstream_only"`
	Platforms    []string            `json:"platforms"`
}

// AdminProtocolCatalog builds the admin protocol capability catalogue.
func AdminProtocolCatalog(endpoints map[protocol.ProtocolID]string) ProtocolCatalogResponse {
	providers := []ProtocolProviderProfile{}
	groups := []ProtocolGroupProfile{}
	for _, platform := range capability.ProviderPlatforms() {
		for _, providerType := range []string{capability.ProviderTypeOAuth, capability.ProviderTypeSetupToken, capability.ProviderTypeAPIKey, capability.ProviderTypeUpstream, capability.ProviderTypeBedrock, capability.ProviderTypeServiceAccount, capability.ProviderTypeCosy} {
			if platform == capability.PlatformAntigravity && providerType != capability.ProviderTypeOAuth {
				continue
			}
			modes := []string{""}
			if platform == capability.PlatformOpenAI && providerType == capability.ProviderTypeOAuth {
				modes = append(modes, capability.OpenAIAuthModePersonalAccessToken, capability.OpenAIAuthModeAgentIdentity)
			}
			for _, mode := range modes {
				providers = append(providers, ProtocolProviderProfile{platform, providerType, mode, capability.NativeProtocolOptions(platform, providerType, mode)})
			}
		}
	}
	supported := capability.SupportedGroupClientProtocols("")
	targets := map[protocol.ProtocolID][]protocol.ProtocolID{}
	for _, source := range supported {
		targets[source] = capability.AutomaticProtocolFallbackTargets(source)
	}
	groups = append(groups, ProtocolGroupProfile{Protocols: supported, Defaults: capability.DefaultGroupClientProtocols(""), FallbackTargets: targets, DefaultFallbacks: capability.DefaultProtocolFallbacks("")})

	return ProtocolCatalogResponse{
		Protocols: publicProtocols(endpoints), Providers: providers, Groups: groups,
		AuxiliaryOperations: AuxiliaryOperations(),
	}
}

func AuxiliaryOperations() []AuxiliaryOperation {
	return []AuxiliaryOperation{
		{"/messages/count_tokens", protocol.ProtocolAnthropicMessages, "protocol"},
		{"/responses/input_tokens", protocol.ProtocolOpenAIResponses, "protocol"},
		{"Gemini :countTokens", protocol.ProtocolGeminiGenerateContent, "protocol"},
		{"native_compaction_v2", protocol.ProtocolOpenAIResponses, "capability"},
		{"http_continuation", protocol.ProtocolOpenAIResponses, "capability"},
		{"responses_image_tools", protocol.ProtocolOpenAIResponses, "image_policy"},
		{"/models, /usage", "", "local"},
		{"video status/content", protocol.ProtocolVideosGenerations, "resource"},
		{"batch status/download/cancel/delete", protocol.ProtocolImageBatches, "resource"},
		{"Live sideband", protocol.ProtocolLive, "session"},
		{"custom voice read/update/delete/audio", protocol.ProtocolCustomVoices, "protocol_and_resource"},
	}
}

func publicProtocols(endpoints map[protocol.ProtocolID]string) []Protocol {
	catalog := capability.ProtocolCatalog()
	out := make([]Protocol, len(catalog))
	for i, p := range catalog {
		out[i] = Protocol{ID: p.ID, Name: p.Name, Endpoint: endpoints[p.ID], UpstreamOnly: p.UpstreamOnly, Platforms: p.Platforms}
	}
	return out
}

// NewProtocolCatalogHandler copies the supplied endpoint map for subsequent responses.
func NewProtocolCatalogHandler(endpoints map[protocol.ProtocolID]string) gin.HandlerFunc {
	snapshot := maps.Clone(endpoints)
	return func(c *gin.Context) { httpx.Success(c, AdminProtocolCatalog(snapshot)) }
}
