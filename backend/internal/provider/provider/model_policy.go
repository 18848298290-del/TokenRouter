package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// model_policy.go supplies platform model defaults and eligibility callbacks
// for provider admission. Antigravity defaults are read through a callback,
// Qoder compatibility follows the credential site at check time, and Grok
// endpoint checks can consult media-generation eligibility only when needed.

// ModelDefaults returns callbacks for finite provider candidates and
// Antigravity mapping defaults. The Antigravity callback exposes the platform's
// shared map; callers use it as read-only input to provider mapping resolution.
func ModelDefaults() provider.ModelMappingDefaults {
	return provider.ModelMappingDefaults{
		Models:      DefaultProviderModels,
		Antigravity: func() map[string]string { return antigravity.DefaultAntigravityModelMapping },
	}
}

// ModelRules returns Qoder normalization, Qoder site admission, and OpenAI
// OAuth eligibility callbacks. Qoder compatibility reads the captured record's
// current credentials on each call; nil records and invalid sites reject that
// model.
func ModelRules(value *provider.Record) provider.ModelPlatformRules {
	return provider.ModelPlatformRules{
		NormalizeQoder:      qoder.NormalizeModelForWhitelist,
		OpenAIOAuthServable: provider.IsOpenAIOAuthServableModel,
		QoderCompatible: func(model string) bool {
			if value == nil {
				return false
			}
			// A site parse failure becomes failed model admission because no recognized
			// Qoder route can establish compatibility for this credential configuration.
			site, err := qoder.ParseSite(value.GetCredential("site"))
			return err == nil && qoder.ModelCompatibleWithSite(site, model)
		},
	}
}

// SupportsOpenAIEndpoint reports endpoint eligibility using provider rules and
// optional Grok media admission. It delegates platform defaults to the record
// and supplies the Grok tier check when that capability requires it.
func SupportsOpenAIEndpoint(value *provider.Record, capability provider.OpenAIEndpointCapability) bool {
	return value.SupportsOpenAIEndpointCapability(capability, func() (bool, string) {
		return provider.GrokMediaGenerationEligibility(value, GrokTierRules())
	})
}
