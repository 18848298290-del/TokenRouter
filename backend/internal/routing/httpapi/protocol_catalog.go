package httpapi

import (
	"maps"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// protocol_catalog.go builds administrative protocol descriptions from
// capability rules and HTTP endpoint metadata.
// Protocol admission choices come from routing capability rules, while their
// public endpoint text comes from HTTP route wiring. Administrative responses
// combine those sources with provider authentication profiles and one generic
// group profile so editors use the same protocol choices as request routing.

// ProtocolProviderProfile lists native choices for one provider
// authentication combination.
type ProtocolProviderProfile struct {
	Platform string `json:"platform"` // provider platform identifier
	Type     string `json:"type"`     // provider credential type
	// AuthMode identifies an OpenAI OAuth variant; an empty string selects the
	// ordinary authentication profile for the platform and type.
	AuthMode  string                `json:"auth_mode"` // provider authentication mode
	Protocols []protocol.ProtocolID `json:"protocols"` // supported native protocol choices
}

// ProtocolGroupProfile supplies generic group admission choices and ordered
// conversion defaults.
type ProtocolGroupProfile struct {
	ProbeProtocols []protocol.ProtocolID `json:"probe_protocols"` // supported availability probe entries
	Protocols      []protocol.ProtocolID `json:"protocols"`       // available client protocol choices
	Defaults       []protocol.ProtocolID `json:"defaults"`        // new group admission defaults
	// FallbackTargets lists available single-step conversions for each source;
	// target order supplies the automatic selection order used by group editors.
	FallbackTargets map[protocol.ProtocolID][]protocol.ProtocolID `json:"fallback_targets"` // ordered allowed conversion targets
	// DefaultFallbacks starts with no per-entry overrides, so new groups use the
	// automatic target choices until an administrator supplies restrictions.
	DefaultFallbacks map[protocol.ProtocolID][]protocol.ProtocolID `json:"default_fallbacks"` // new group conversion overrides
}

// ProtocolCatalogResponse combines protocol definitions, editor choices, and
// auxiliary authorization rules.
type ProtocolCatalogResponse struct {
	Protocols           []Protocol                `json:"protocols"`            // displayed protocol capability entries
	Providers           []ProtocolProviderProfile `json:"providers"`            // provider authentication option profiles
	Groups              []ProtocolGroupProfile    `json:"groups"`               // generic group editor profile
	AuxiliaryOperations []AuxiliaryOperation      `json:"auxiliary_operations"` // auxiliary action authorization descriptions
}

// AuxiliaryOperation identifies a protocol-linked action and its
// authorization rule.
type AuxiliaryOperation struct {
	Operation string `json:"operation"` // auxiliary action or resource
	// Protocol names the owning capability when one applies; local model and
	// usage queries carry an empty value that JSON omits.
	Protocol      protocol.ProtocolID `json:"protocol,omitempty"` // owning protocol identifier
	Authorization string              `json:"authorization"`      // authorization rule category
}

// Protocol exposes a capability definition with its configured public
// endpoint.
type Protocol struct {
	ID   protocol.ProtocolID `json:"id"`   // stable protocol identifier
	Name string              `json:"name"` // protocol display name
	// Endpoint comes from the supplied HTTP endpoint map; omitted entries remain
	// empty, including capabilities used solely by upstream adapters.
	Endpoint     string   `json:"endpoint"`      // configured public endpoint text
	UpstreamOnly bool     `json:"upstream_only"` // upstream-only capability marker
	Platforms    []string `json:"platforms"`     // platforms implementing this capability
}

// AdminProtocolCatalog combines capability rules with the supplied endpoint
// descriptions for administrator forms. It includes provider type and
// authentication combinations with their native protocol sets, and returns
// one generic group profile with admission defaults and ordered conversion
// choices. Missing endpoint keys produce empty endpoint text.
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
	groups = append(groups, ProtocolGroupProfile{Protocols: supported, ProbeProtocols: provider.GroupProbeProtocols(), Defaults: capability.DefaultGroupClientProtocols(""), FallbackTargets: targets, DefaultFallbacks: capability.DefaultProtocolFallbacks("")})

	return ProtocolCatalogResponse{
		Protocols: publicProtocols(endpoints), Providers: providers, Groups: groups,
		AuxiliaryOperations: AuxiliaryOperations(),
	}
}

// AuxiliaryOperations returns the protocol and authorization categories for
// supported auxiliary actions.
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

// publicProtocols adds supplied endpoint text to copied capability
// definitions for HTTP responses. Capability ordering and platform lists come
// from ProtocolCatalog, while absent map entries leave their endpoint text
// empty.
func publicProtocols(endpoints map[protocol.ProtocolID]string) []Protocol {
	catalog := capability.ProtocolCatalog()
	out := make([]Protocol, len(catalog))
	for i, p := range catalog {
		out[i] = Protocol{ID: p.ID, Name: p.Name, Endpoint: endpoints[p.ID], UpstreamOnly: p.UpstreamOnly, Platforms: p.Platforms}
	}
	return out
}

// NewProtocolCatalogHandler snapshots endpoint descriptions and returns the
// administrator catalog query handler. Later caller changes to the supplied
// map do not alter responses because construction clones that map before the
// closure captures it.
func NewProtocolCatalogHandler(endpoints map[protocol.ProtocolID]string) gin.HandlerFunc {
	snapshot := maps.Clone(endpoints)
	return func(c *gin.Context) { httpx.Success(c, AdminProtocolCatalog(snapshot)) }
}
