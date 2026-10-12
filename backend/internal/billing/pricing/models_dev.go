package pricing

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// ModelsDevPrices converts catalog prices into billing fields, distinguishing missing prices from explicit zero.
func ModelsDevPrices(catalog *modelcatalog.Catalog) map[string]json.RawMessage {
	result := map[string]json.RawMessage{}
	for key, entry := range catalog.Entries {
		if entry.Cost.Input == nil && entry.Cost.Output == nil {
			continue
		}
		fields := modelsDevPriceFields(entry, entry.Cost)
		var tiers []map[string]any
		for _, tier := range entry.Tiers {
			cost := entry.Cost
			if tier.Input != nil {
				cost.Input = tier.Input
			}
			if tier.Output != nil {
				cost.Output = tier.Output
			}
			if tier.CacheRead != nil {
				cost.CacheRead = tier.CacheRead
			}
			if tier.CacheWrite != nil {
				cost.CacheWrite = tier.CacheWrite
			}
			tiers = append(tiers, map[string]any{"threshold": tier.Tier.Size, "pricing": modelsDevPriceFields(entry, cost)})
		}
		if len(tiers) > 0 {
			fields["context_prices"] = tiers
		}
		body, _ := json.Marshal(fields)
		result[key] = body
	}
	return result
}

func modelsDevPriceFields(entry modelcatalog.Entry, cost modelcatalog.Cost) map[string]any {
	provider := entry.Provider
	switch provider {
	case "google":
		provider = "gemini"
	case "moonshotai", "moonshotai-cn":
		provider = "moonshot"
	case "zai", "zhipuai":
		provider = "zhipu"
	}
	fields := map[string]any{"source": "models.dev", "provider": provider, "mode": "chat"}
	if entry.Attributes.InputModalities != nil {
		fields["supported_modalities"] = *entry.Attributes.InputModalities
	}
	if entry.Attributes.OutputModalities != nil {
		fields["supported_output_modalities"] = *entry.Attributes.OutputModalities
	}
	// First-party Gemini Image output prices are per image token; local supplements supply text and thinking prices.
	// Omni, Deep Research, and relay entries keep ordinary output prices; image modality alone does not determine units.
	imageOutput := entry.FirstParty && entry.Provider == "google" && strings.HasPrefix(entry.Model, "gemini-") &&
		strings.Contains(entry.Model, "-image") && entry.Attributes.OutputModalities != nil &&
		slices.Contains(*entry.Attributes.OutputModalities, "image")
	put := func(key string, value *float64) {
		if value != nil {
			fields[key] = *value / 1e6
		}
	}
	put("input_cost_per_token", cost.Input)
	if imageOutput {
		put("output_cost_per_image_token", cost.Output)
	} else {
		put("output_cost_per_token", cost.Output)
	}
	put("cache_read_input_token_cost", cost.CacheRead)
	put("cache_creation_input_token_cost", cost.CacheWrite)
	fields["supports_prompt_caching"] = cost.CacheRead != nil || cost.CacheWrite != nil
	// Service-tier fields represent text token prices; image Fast prices need separate handling.
	if entry.Fast != nil && !imageOutput {
		fields["supports_service_tier"] = true
		put("input_cost_per_token_priority", entry.Fast.Input)
		put("output_cost_per_token_priority", entry.Fast.Output)
		put("cache_read_input_token_cost_priority", entry.Fast.CacheRead)
		put("cache_creation_input_token_cost_priority", entry.Fast.CacheWrite)
	}
	return fields
}
