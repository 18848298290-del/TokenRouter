package pricing

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// models_dev.go converts models.dev entries into billing catalog price JSON.
// models.dev quotes prices in USD per million tokens, while settlement consumes
// USD per token. Conversion retains optional-value presence so a declared zero
// remains distinct from a missing text, cache, or Fast price, including within
// absolute context tiers.
//
// Tier amounts replace supplied buckets and inherit omitted buckets from the
// entry's base cost. Original Google Gemini image output is routed to the
// image-output token bucket only when its identity and output modalities meet
// that specific rule; text and thinking prices then require their own
// supplemental data. Other entries retain ordinary output token units, and
// provider labels are mapped for existing billing consumers.

// ModelsDevPrices converts each priced catalog identity into raw billing JSON
// fields. Callers supply a non-nil catalog and keep entries stable during
// conversion. Entries missing both input and output amounts are skipped;
// supplied zero amounts still produce records.
//
// Each absolute tier starts with base costs and replaces only its supplied
// input, output, and cache buckets. Threshold values and entry identities
// retain their catalog meaning for later parsing and billing conversion. JSON
// encoding failures leave nil entry bytes for the publication parser to
// diagnose rather than producing a partial valid price object.
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
		// Encoding failures leave nil entry bytes, which catalog parsing rejects
		// before the candidate can become published prices.
		body, _ := json.Marshal(fields)
		result[key] = body
	}
	return result
}

// modelsDevPriceFields returns billing fields for one entry and selected cost
// record. Present amounts are converted from USD per million tokens to USD per
// token, retaining explicit zero and modality metadata. Provider labels are
// mapped to names already used by billing.
//
// Original Google Gemini image identities with image output modalities place
// output into the image-output token field. Other records use text output; Fast
// amounts are emitted only on that text-output path. The returned map contains
// supplied modality slices by reference until JSON encoding, so callers keep
// entry data stable.
func modelsDevPriceFields(entry modelcatalog.Entry, cost modelcatalog.Cost) map[string]any {
	provider := entry.Provider
	// Billing consumers use these established provider labels, so map catalog
	// spellings before deriving provider-sensitive pricing behavior.
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
	// Original Google Gemini image output uses image-token units when its full
	// identity and output modalities qualify. Text and thinking prices remain
	// independent supplemental buckets.
	imageOutput := entry.FirstParty && entry.Provider == "google" && strings.HasPrefix(entry.Model, "gemini-") &&
		strings.Contains(entry.Model, "-image") && entry.Attributes.OutputModalities != nil &&
		slices.Contains(*entry.Attributes.OutputModalities, "image")
	put := func(key string, value *float64) {
		if value != nil {
			// Convert the catalog's million-token quote into settlement's per-token
			// unit.
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
	// These Fast fields represent text-token amounts; image-output conversion
	// omits them so text-tier pricing is not misassigned to image tokens.
	if entry.Fast != nil && !imageOutput {
		fields["supports_service_tier"] = true
		put("input_cost_per_token_priority", entry.Fast.Input)
		put("output_cost_per_token_priority", entry.Fast.Output)
		put("cache_read_input_token_cost_priority", entry.Fast.CacheRead)
		put("cache_creation_input_token_cost_priority", entry.Fast.CacheWrite)
	}
	return fields
}
