package pricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// ErrNoPricingEntries means no usable prices were found; callers must also check entry diagnostics.
var ErrNoPricingEntries = errors.New("no valid pricing entries found")

// CatalogDiagnostics describes parsing issues for the provider to report.
type CatalogDiagnostics struct {
	Skipped                           int
	InvalidEntries                    []string
	OrphanCacheTiers, LopsidedLadders []string
}

// ValidationError combines entry errors for loaders that require all entries to validate before publication.
func (d CatalogDiagnostics) ValidationError() error {
	if len(d.InvalidEntries) == 0 {
		return nil
	}
	return fmt.Errorf("invalid pricing entries: %s", strings.Join(d.InvalidEntries, "; "))
}

// DecodeCatalogEntries reads price objects and normalizes legacy field names before merging.
func DecodeCatalogEntries(body []byte) (map[string]json.RawMessage, error) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, err
	}
	if entries == nil {
		return nil, fmt.Errorf("expected a pricing JSON object")
	}
	for name, entry := range entries {
		if name == "sample_spec" {
			delete(entries, name)
			continue
		}
		normalized, err := normalizeCatalogEntry(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid pricing entry %s: %w", name, err)
		}
		entries[name] = normalized
	}
	return entries, nil
}

// normalizeCatalogEntry accepts litellm_provider when provider is absent; even an empty or null provider takes precedence.
func normalizeCatalogEntry(raw json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("expected an object")
	}
	legacy, exists := fields["litellm_provider"]
	if !exists {
		return raw, nil
	}
	if _, present := fields["provider"]; !present {
		fields["provider"] = legacy
	}
	delete(fields, "litellm_provider")
	return json.Marshal(fields)
}

// ParsePricingEntries parses supplied entries and returns prices with validation diagnostics.
func ParsePricingEntries(rawData map[string]json.RawMessage) (map[string]*CatalogModelPricing, CatalogDiagnostics, error) {
	result := make(map[string]*CatalogModelPricing)
	skipped := 0
	var invalidEntries []string
	var orphanCacheTiers, lopsidedLadders []string

	for modelName, rawEntry := range rawData {
		// sample_spec contains documentation rather than a model price.
		if modelName == "sample_spec" {
			continue
		}

		// Normalize legacy fields before decoding the price entry.
		normalized, decodeErr := normalizeCatalogEntry(rawEntry)
		var entry CatalogRawEntry
		if decodeErr == nil {
			decodeErr = json.Unmarshal(normalized, &entry)
		}
		if decodeErr != nil {
			skipped++
			invalidEntries = append(invalidEntries, fmt.Sprintf("%s: %v", modelName, decodeErr))
			continue
		}

		// Keep explicit zeros; report negative or nonfinite amounts so loaders can reject publication.
		invalidAmount := false
		value := reflect.ValueOf(entry)
		fields := value.Type()
		for i := range value.NumField() {
			field := value.Field(i)
			if field.Kind() == reflect.Pointer && field.Type().Elem().Kind() == reflect.Float64 && !field.IsNil() && !validAmount(field.Elem().Float()) {
				invalidEntries = append(invalidEntries, fmt.Sprintf("%s: invalid %s", modelName, fields.Field(i).Tag.Get("json")))
				invalidAmount = true
			}
		}
		if invalidAmount {
			continue
		}
		// Validate rules and patches even when they supply no base prices.
		if err := entry.Validate(); err != nil {
			invalidEntries = append(invalidEntries, fmt.Sprintf("%s: %v", modelName, err))
			continue
		}
		if modelName == BillingDefaultsKey {
			var defaults OperationPrices
			if err := json.Unmarshal(rawEntry, &defaults); err != nil {
				invalidEntries = append(invalidEntries, fmt.Sprintf("%s: %v", modelName, err))
			} else if err := defaults.Validate(); err != nil {
				invalidEntries = append(invalidEntries, fmt.Sprintf("%s: %v", modelName, err))
			}
			continue
		}
		// Entries need at least one token or media price.
		if entry.InputCostPerToken == nil && entry.OutputCostPerToken == nil && entry.OutputCostPerImage == nil && entry.OutputCostPerImageToken == nil && entry.InputCostPerImageToken == nil && len(entry.ImagePrices) == 0 && len(entry.VideoPrices) == 0 {
			continue
		}

		pricing := &CatalogModelPricing{
			InputPricePresent:           entry.InputCostPerToken != nil,
			CatalogRules:                entry.Clone(),
			CacheCreation1hPricePresent: entry.CacheCreationInputTokenCostAbove1hr != nil,
			PriorityInputPresent:        entry.Source != "" && entry.InputCostPerTokenPriority != nil,
			PriorityOutputPresent:       entry.Source != "" && entry.OutputCostPerTokenPriority != nil,
			PriorityCacheReadPresent:    entry.Source != "" && entry.CacheReadInputTokenCostPriority != nil,
			PriorityCacheWritePresent:   entry.Source != "" && entry.CacheCreationInputTokenCostPriority != nil,
			Source:                      entry.Source,
			PriceSources:                entry.PriceSources,
			ContextPrices:               entry.ContextPrices,
			CacheCreationPricePresent:   entry.CacheCreationInputTokenCost != nil,
			CacheReadPricePresent:       entry.CacheReadInputTokenCost != nil,
			ImagePricePresent:           entry.OutputCostPerImage != nil,
			ImageInputPricePresent:      entry.InputCostPerImageToken != nil,
			ImageOutputPricePresent:     entry.OutputCostPerImageToken != nil,
			Provider:                    entry.Provider,
			Mode:                        entry.Mode,
			SupportsPromptCaching:       entry.SupportsPromptCaching,
			SupportsServiceTier:         entry.SupportsServiceTier,
			SupportedModalities:         entry.SupportedModalities,
			SupportedOutputModalities:   entry.SupportedOutputModalities,
			SupportsVision:              entry.SupportsVision,
			SupportsAudioInput:          entry.SupportsAudioInput,
			SupportsAudioOutput:         entry.SupportsAudioOutput,
			SupportsVideoInput:          entry.SupportsVideoInput,
			TokenPricingAbsent:          entry.InputCostPerToken == nil && entry.OutputCostPerToken == nil,
		}
		// models.dev requires both text price buckets; an image price cannot imply free text output.
		if entry.Source == "models.dev" && (entry.InputCostPerToken == nil || entry.OutputCostPerToken == nil) {
			pricing.TokenPricingAbsent = true
		}
		// Use the alternate input-modality field when supported_modalities is empty.
		if len(pricing.SupportedModalities) == 0 {
			pricing.SupportedModalities = entry.SupportedInputModalities
		}

		if entry.InputCostPerToken != nil {
			pricing.InputCostPerToken = *entry.InputCostPerToken
		}
		if entry.InputCostPerTokenPriority != nil {
			pricing.InputCostPerTokenPriority = *entry.InputCostPerTokenPriority
		}
		if entry.OutputCostPerToken != nil {
			pricing.OutputCostPerToken = *entry.OutputCostPerToken
		}
		if entry.OutputCostPerTokenPriority != nil {
			pricing.OutputCostPerTokenPriority = *entry.OutputCostPerTokenPriority
		}
		if entry.CacheCreationInputTokenCost != nil {
			pricing.CacheCreationInputTokenCost = *entry.CacheCreationInputTokenCost
		}
		if entry.CacheCreationInputTokenCostPriority != nil {
			pricing.CacheCreationInputTokenCostPriority = *entry.CacheCreationInputTokenCostPriority
		}
		if entry.CacheCreationInputTokenCostAbove1hr != nil {
			pricing.CacheCreationInputTokenCostAbove1hr = *entry.CacheCreationInputTokenCostAbove1hr
		}
		if entry.CacheReadInputTokenCost != nil {
			pricing.CacheReadInputTokenCost = *entry.CacheReadInputTokenCost
		}
		if entry.CacheReadInputTokenCostPriority != nil {
			pricing.CacheReadInputTokenCostPriority = *entry.CacheReadInputTokenCostPriority
		}
		if entry.LongContextInputTokenThreshold != nil {
			pricing.LongContextInputTokenThreshold = *entry.LongContextInputTokenThreshold
		}
		if entry.LongContextInputCostMultiplier != nil {
			pricing.LongContextInputCostMultiplier = *entry.LongContextInputCostMultiplier
		}
		if entry.LongContextOutputCostMultiplier != nil {
			pricing.LongContextOutputCostMultiplier = *entry.LongContextOutputCostMultiplier
		}
		if entry.OutputCostPerImage != nil {
			pricing.OutputCostPerImage = *entry.OutputCostPerImage
		}
		if entry.OutputCostPerImageToken != nil {
			pricing.OutputCostPerImageToken = *entry.OutputCostPerImageToken
		}
		if entry.InputCostPerImageToken != nil {
			pricing.InputCostPerImageToken = *entry.InputCostPerImageToken
		}

		if err := deriveCatalogCachePrices(pricing); err != nil {
			invalidEntries = append(invalidEntries, fmt.Sprintf("%s: %v", modelName, err))
			continue
		}
		// Explicit long_context fields, including zero, take precedence over catalog tiers and above prices.
		hasExplicitLongContext := entry.LongContextInputTokenThreshold != nil ||
			entry.LongContextInputCostMultiplier != nil ||
			entry.LongContextOutputCostMultiplier != nil
		if hasExplicitLongContext {
			// Clear absolute tiers so they cannot bypass an administrator's threshold or multiplier override.
			pricing.ContextPrices = nil
		} else {
			DeriveLongContextFromAboveTierFields(rawEntry, pricing)
			if IsLopsidedLongContextLadder(pricing) {
				lopsidedLadders = append(lopsidedLadders, fmt.Sprintf("%s(input x%.2f, output x%.2f)", modelName,
					pricing.LongContextInputCostMultiplier, pricing.LongContextOutputCostMultiplier))
			}
		}
		if orphans := OrphanCacheTierFields(rawEntry); len(orphans) > 0 {
			orphanCacheTiers = append(orphanCacheTiers, modelName+"("+strings.Join(orphans, ",")+")")
		}

		result[modelName] = pricing
	}

	sort.Strings(invalidEntries)
	diagnostics := CatalogDiagnostics{Skipped: skipped, InvalidEntries: invalidEntries, OrphanCacheTiers: orphanCacheTiers, LopsidedLadders: lopsidedLadders}

	if len(result) == 0 {
		return nil, diagnostics, ErrNoPricingEntries
	}

	return result, diagnostics, nil
}
