package pricing

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	// AboveTierPricePattern matches absolute long-context token prices in catalog entries.
	// Service-tier suffixes and cache fields do not contribute to this threshold conversion.
	AboveTierPricePattern = regexp.MustCompile(`^(input|output)_cost_per_token_above_(\d+)k_tokens$`)
	// CacheTierPricePattern matches long-context cache prices for validation diagnostics.
	// Groups capture the base cache field, optional 1h TTL segment, and service-tier suffix.
	CacheTierPricePattern = regexp.MustCompile(`^(cache_(?:creation|read)_input_token_cost)(_above_1hr)?_above_\d+k_tokens((?:_[a-z]+)?)$`)

	// MarketplaceModalityOrder lists supported display modalities in output order.
	MarketplaceModalityOrder = []string{"text", "image", "audio", "video"}
)

// DeriveLongContextFromAboveTierFields converts above_XXXk prices to a threshold and multipliers.
// When several thresholds exist, it selects the smallest. Billing applies the input
// multiplier to cache prices, so this conversion does not copy cache-specific above prices.
func DeriveLongContextFromAboveTierFields(rawEntry json.RawMessage, pricing *CatalogModelPricing) {
	if pricing == nil ||
		pricing.LongContextInputTokenThreshold > 0 ||
		pricing.LongContextInputCostMultiplier > 0 ||
		pricing.LongContextOutputCostMultiplier > 0 {
		return
	}
	if !bytes.Contains(rawEntry, []byte("_above_")) {
		return
	}
	var fields map[string]any
	if err := json.Unmarshal(rawEntry, &fields); err != nil {
		return
	}
	type tierPrices struct{ input, output float64 }
	tiers := make(map[int]*tierPrices)
	for key, value := range fields {
		match := AboveTierPricePattern.FindStringSubmatch(key)
		if match == nil {
			continue
		}
		price, ok := value.(float64)
		if !ok || price <= 0 {
			continue
		}
		thousands, err := strconv.Atoi(match[2])
		if err != nil || thousands <= 0 {
			continue
		}
		threshold := thousands * 1000
		tier := tiers[threshold]
		if tier == nil {
			tier = &tierPrices{}
			tiers[threshold] = tier
		}
		if match[1] == "input" {
			tier.input = price
		} else {
			tier.output = price
		}
	}
	if len(tiers) == 0 {
		return
	}
	threshold := 0
	for candidate := range tiers {
		if threshold == 0 || candidate < threshold {
			threshold = candidate
		}
	}
	tier := tiers[threshold]
	inputMultiplier, outputMultiplier := 1.0, 1.0
	if tier.input > 0 && pricing.InputCostPerToken > 0 {
		inputMultiplier = tier.input / pricing.InputCostPerToken
	}
	if tier.output > 0 && pricing.OutputCostPerToken > 0 {
		outputMultiplier = tier.output / pricing.OutputCostPerToken
	}
	// Create a context tier only when at least one price increases over its base price.
	if inputMultiplier <= 1 && outputMultiplier <= 1 {
		return
	}
	pricing.LongContextInputTokenThreshold = threshold
	pricing.LongContextInputCostMultiplier = inputMultiplier
	pricing.LongContextOutputCostMultiplier = outputMultiplier
}

// IsLopsidedLongContextLadder reports whether a context tier increases only one of input and output prices.
func IsLopsidedLongContextLadder(pricing *CatalogModelPricing) bool {
	if pricing == nil || pricing.LongContextInputTokenThreshold <= 0 {
		return false
	}
	return (pricing.LongContextInputCostMultiplier > 1) != (pricing.LongContextOutputCostMultiplier > 1)
}

// OrphanCacheTierFields finds positive cache above prices with no positive base price for loader diagnostics.
func OrphanCacheTierFields(rawEntry json.RawMessage) []string {
	if !bytes.Contains(rawEntry, []byte("_above_")) {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal(rawEntry, &fields); err != nil {
		return nil
	}
	positive := func(key string) bool {
		price, ok := fields[key].(float64)
		return ok && price > 0
	}
	var orphans []string
	for key := range fields {
		match := CacheTierPricePattern.FindStringSubmatch(key)
		if match == nil || !positive(key) {
			continue
		}
		stem, hourly, tier := match[1], match[2], match[3]
		if positive(stem+hourly+tier) || positive(stem+hourly) || positive(stem+tier) || positive(stem) {
			continue
		}
		orphans = append(orphans, key)
	}
	sort.Strings(orphans)
	return orphans
}

// SanitizeModalities filters and deduplicates modalities in the fixed display order.
func SanitizeModalities(values []string) []string {
	present := make(map[string]bool, len(values))
	for _, value := range values {
		present[strings.ToLower(strings.TrimSpace(value))] = true
	}
	out := make([]string, 0, len(MarketplaceModalityOrder))
	for _, modality := range MarketplaceModalityOrder {
		if present[modality] {
			out = append(out, modality)
		}
	}
	return out
}

// DeriveModalities fills missing input or output modalities from mode, then supplements them
// with supports_* flags and image-input pricing, which can indicate image editing.
func DeriveModalities(p *CatalogModelPricing) ([]string, []string) {
	if p == nil {
		return nil, nil
	}

	// Copy before appending because concurrent catalog queries share the source slice backing arrays.
	input := append([]string{}, p.SupportedModalities...)
	output := append([]string{}, p.SupportedOutputModalities...)
	if len(input) == 0 || len(output) == 0 {
		modeInput, modeOutput := ModalitiesFromMode(p.Mode)
		if len(input) == 0 {
			input = modeInput
		}
		if len(output) == 0 {
			output = modeOutput
		}
	}
	if p.SupportsVision {
		input = append(input, "image")
	}
	if p.SupportsAudioInput {
		input = append(input, "audio")
	}
	if p.SupportsAudioOutput {
		output = append(output, "audio")
	}
	if p.SupportsVideoInput {
		input = append(input, "video")
	}
	if p.InputCostPerImageToken > 0 {
		input = append(input, "image")
	}

	in := SanitizeModalities(input)
	out := SanitizeModalities(output)
	if len(in) == 0 || len(out) == 0 {
		return nil, nil
	}
	return in, out
}

// ModalitiesFromMode infers base modalities from mode, defaulting unknown modes to text input and output.
func ModalitiesFromMode(mode string) ([]string, []string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "image_generation":
		return []string{"text"}, []string{"image"}
	case "audio_transcription":
		return []string{"audio"}, []string{"text"}
	case "audio_speech":
		return []string{"text"}, []string{"audio"}
	case "realtime":
		return []string{"text", "audio"}, []string{"text", "audio"}
	default:
		// Conversational modes use text as their base input and output modality.
		return []string{"text"}, []string{"text"}
	}
}

// BuildModelLookupCandidates supplies the same full identity candidates for prices and attributes.
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#model_catalog_metadata_lookup
func BuildModelLookupCandidates(model string) []string {
	return BuildModelIdentityCandidates(model)
}

// BuildModelIdentityCandidates normalizes known Google resource paths while retaining vendor qualifiers and versions.
func BuildModelIdentityCandidates(model string) []string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return nil
	}
	normalized := NormalizeModelNameForPricing(model)
	if normalized == model {
		return []string{model}
	}
	return []string{model, normalized}
}

func NormalizeModelNameForPricing(model string) string {
	// Strip protocol resource prefixes while retaining model names and tier suffixes.
	model = strings.TrimSpace(model)
	model = strings.TrimLeft(model, "/")
	model = strings.TrimPrefix(model, "models/")
	model = strings.TrimPrefix(model, "publishers/google/models/")

	if idx := strings.LastIndex(model, "/publishers/google/models/"); idx != -1 {
		model = model[idx+len("/publishers/google/models/"):]
	}

	model = strings.TrimLeft(model, "/")
	return model
}
