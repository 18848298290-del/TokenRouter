package provider

import (
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/bedrock"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// DefaultProviderModels lists platform model candidates, with a separate set for Spark quota shadows.
func DefaultProviderModels(value *provider.Record) []string {
	if value == nil {
		return nil
	}
	if value.IsShadow() && value.Platform == provider.PlatformOpenAI {
		return sparkModelVariants()
	}
	if value.IsBedrock() {
		var ids []string
		for alias, target := range bedrock.DefaultBedrockModelMapping {
			ids = append(ids, alias, target)
		}
		slices.Sort(ids)
		return slices.Compact(ids)
	}
	if value.Platform == provider.PlatformQoder {
		site, err := qoder.ParseSite(value.GetCredential("site"))
		if err != nil {
			return nil
		}
		return qoder.DefaultRequestModelIDsForSite(site)
	}
	return nil
}

// sparkModelVariants returns the models supported by Spark shadows.
func sparkModelVariants() []string {
	return []string{"gpt-5.3-codex-spark"}
}

// DefaultSparkShadowModels returns a fresh identity mapping for the Spark model set.
func DefaultSparkShadowModels() map[string]any {
	variants := sparkModelVariants()
	mapping := make(map[string]any, len(variants))
	for _, m := range variants {
		mapping[m] = m
	}
	return mapping
}
