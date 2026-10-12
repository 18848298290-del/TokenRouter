package provider

import (
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// ModelDefaults supplies on-demand platform model candidates and Antigravity defaults.
func ModelDefaults() provider.ModelMappingDefaults {
	return provider.ModelMappingDefaults{
		Models:      DefaultProviderModels,
		Antigravity: func() map[string]string { return antigravity.DefaultAntigravityModelMapping },
	}
}

// ModelRules reads site credentials when evaluating Qoder compatibility.
func ModelRules(value *provider.Record) provider.ModelPlatformRules {
	return provider.ModelPlatformRules{
		NormalizeQoder:      qoder.NormalizeModelForWhitelist,
		OpenAIOAuthServable: provider.IsOpenAIOAuthServableModel,
		QoderCompatible: func(model string) bool {
			if value == nil {
				return false
			}
			site, err := qoder.ParseSite(value.GetCredential("site"))
			return err == nil && qoder.ModelCompatibleWithSite(site, model)
		},
	}
}

// SupportsOpenAIEndpoint supplies Grok media eligibility when the endpoint check needs it.
func SupportsOpenAIEndpoint(value *provider.Record, capability provider.OpenAIEndpointCapability) bool {
	return value.SupportsOpenAIEndpointCapability(capability, func() (bool, string) {
		return provider.GrokMediaGenerationEligibility(value, GrokTierRules())
	})
}
