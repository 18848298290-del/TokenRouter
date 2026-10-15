package provider

import (
	"slices"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/bedrock"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// default_models.go supplies finite platform model candidates for provider
// mappings and catalogues. OpenAI shadows expose the native Spark ID, Bedrock
// contributes aliases and targets in sorted distinct order, and Qoder chooses
// its list from the credential site.

// DefaultProviderModels returns platform candidates for a record, with OpenAI
// shadows checked first. Nil records, unsupported platforms, and invalid Qoder
// sites return nil; Bedrock results are sorted and deduplicated across mapping
// aliases and targets.
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
		// An invalid Qoder site has no verified default route list. Returning no
		// candidates prevents catalogue construction from advertising models for an
		// unrecognized site.
		if err != nil {
			return nil
		}
		return qoder.DefaultRequestModelIDsForSite(site)
	}
	return nil
}

// sparkModelVariants returns a fresh slice containing the native Spark model
// ID.
func sparkModelVariants() []string {
	return []string{"gpt-5.3-codex-spark"}
}

// DefaultSparkShadowModels creates an identity mapping for the finite Spark
// candidate set. Each call returns its own map, so provider configuration can
// modify the entries independently of subsequent defaults.
func DefaultSparkShadowModels() map[string]any {
	variants := sparkModelVariants()
	mapping := make(map[string]any, len(variants))
	for _, m := range variants {
		mapping[m] = m
	}
	return mapping
}
