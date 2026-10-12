package pricing

import (
	"encoding/json"
	"maps"
	"slices"
)

// CatalogModelPricing holds token prices and catalog metadata; presence flags distinguish missing prices from explicit zero.
type CatalogModelPricing struct {
	CatalogRules
	InputPricePresent           bool                  `json:"-"`
	CacheCreation1hPricePresent bool                  `json:"-"`
	PriorityInputPresent        bool                  `json:"-"`
	PriorityOutputPresent       bool                  `json:"-"`
	PriorityCacheReadPresent    bool                  `json:"-"`
	PriorityCacheWritePresent   bool                  `json:"-"`
	Source                      string                `json:"source,omitempty"`
	PriceSources                map[string]string     `json:"price_sources,omitempty"`
	ContextPrices               []CatalogContextPrice `json:"context_prices,omitempty"`
	// Presence flags let the default-price page distinguish free from unavailable buckets.
	CacheCreationPricePresent           bool    `json:"-"`
	CacheReadPricePresent               bool    `json:"-"`
	ImagePricePresent                   bool    `json:"-"`
	ImageInputPricePresent              bool    `json:"-"`
	ImageOutputPricePresent             bool    `json:"-"`
	InputCostPerToken                   float64 `json:"input_cost_per_token"`
	InputCostPerTokenPriority           float64 `json:"input_cost_per_token_priority"`
	OutputCostPerToken                  float64 `json:"output_cost_per_token"`
	OutputCostPerTokenPriority          float64 `json:"output_cost_per_token_priority"`
	CacheCreationInputTokenCost         float64 `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostPriority float64 `json:"cache_creation_input_token_cost_priority"`
	CacheCreationInputTokenCostAbove1hr float64 `json:"cache_creation_input_token_cost_above_1hr"`
	CacheReadInputTokenCost             float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostPriority     float64 `json:"cache_read_input_token_cost_priority"`
	LongContextInputTokenThreshold      int     `json:"long_context_input_token_threshold,omitempty"`
	LongContextInputCostMultiplier      float64 `json:"long_context_input_cost_multiplier,omitempty"`
	LongContextOutputCostMultiplier     float64 `json:"long_context_output_cost_multiplier,omitempty"`
	SupportsServiceTier                 bool    `json:"supports_service_tier"`
	Provider                            string  `json:"provider"`
	Mode                                string  `json:"mode"`
	SupportsPromptCaching               bool    `json:"supports_prompt_caching"`
	OutputCostPerImage                  float64 `json:"output_cost_per_image"`       // Price per generated image.
	OutputCostPerImageToken             float64 `json:"output_cost_per_image_token"` // Price per image output token.
	InputCostPerImageToken              float64 `json:"input_cost_per_image_token"`  // Price per image input token, such as image-editing input.

	// Capability metadata supplies marketplace input/output modality displays.
	SupportedModalities       []string `json:"supported_modalities"`
	SupportedOutputModalities []string `json:"supported_output_modalities"`
	SupportsVision            bool     `json:"supports_vision"`
	SupportsAudioInput        bool     `json:"supports_audio_input"`
	SupportsAudioOutput       bool     `json:"supports_audio_output"`
	SupportsVideoInput        bool     `json:"supports_video_input"`

	// TokenPricingAbsent indicates missing token prices; models.dev requires both input and output prices.
	// Legacy files accept one-sided token prices, while incomplete models.dev entries remain unpriced.
	// Media prices in these entries remain available for separate image billing.
	TokenPricingAbsent bool `json:"-"`
}

// CatalogRawEntry uses pointers to distinguish omitted price fields from explicit zero.
type CatalogRawEntry struct {
	CatalogRules
	Source                              string                `json:"source,omitempty"`
	PriceSources                        map[string]string     `json:"price_sources,omitempty"`
	ContextPrices                       []CatalogContextPrice `json:"context_prices,omitempty"`
	InputCostPerToken                   *float64              `json:"input_cost_per_token"`
	InputCostPerTokenPriority           *float64              `json:"input_cost_per_token_priority"`
	OutputCostPerToken                  *float64              `json:"output_cost_per_token"`
	OutputCostPerTokenPriority          *float64              `json:"output_cost_per_token_priority"`
	CacheCreationInputTokenCost         *float64              `json:"cache_creation_input_token_cost"`
	CacheCreationInputTokenCostPriority *float64              `json:"cache_creation_input_token_cost_priority"`
	CacheCreationInputTokenCostAbove1hr *float64              `json:"cache_creation_input_token_cost_above_1hr"`
	CacheReadInputTokenCost             *float64              `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostPriority     *float64              `json:"cache_read_input_token_cost_priority"`
	LongContextInputTokenThreshold      *int                  `json:"long_context_input_token_threshold"`
	LongContextInputCostMultiplier      *float64              `json:"long_context_input_cost_multiplier"`
	LongContextOutputCostMultiplier     *float64              `json:"long_context_output_cost_multiplier"`
	SupportsServiceTier                 bool                  `json:"supports_service_tier"`
	Provider                            string                `json:"provider"`
	Mode                                string                `json:"mode"`
	SupportsPromptCaching               bool                  `json:"supports_prompt_caching"`
	OutputCostPerImage                  *float64              `json:"output_cost_per_image"`
	OutputCostPerImageToken             *float64              `json:"output_cost_per_image_token"`
	InputCostPerImageToken              *float64              `json:"input_cost_per_image_token"`
	SupportedModalities                 []string              `json:"supported_modalities"`
	SupportedInputModalities            []string              `json:"supported_input_modalities"`
	SupportedOutputModalities           []string              `json:"supported_output_modalities"`
	SupportsVision                      bool                  `json:"supports_vision"`
	SupportsAudioInput                  bool                  `json:"supports_audio_input"`
	SupportsAudioOutput                 bool                  `json:"supports_audio_output"`
	SupportsVideoInput                  bool                  `json:"supports_video_input"`
}

// CatalogContextPrice holds absolute catalog prices for the whole usage above its provider's threshold.
type CatalogContextPrice struct {
	Threshold int                  `json:"threshold"`
	Pricing   *CatalogModelPricing `json:"pricing"`
}

// CloneCatalogPrice recursively copies tiers and source labels while retaining price presence flags.
func CloneCatalogPrice(value *CatalogModelPricing) *CatalogModelPricing {
	if value == nil {
		return nil
	}
	result := *value
	result.CatalogRules = value.Clone()
	result.SupportedModalities = slices.Clone(value.SupportedModalities)
	result.SupportedOutputModalities = slices.Clone(value.SupportedOutputModalities)
	if value.PriceSources != nil {
		result.PriceSources = make(map[string]string, len(value.PriceSources))
		maps.Copy(result.PriceSources, value.PriceSources)
	}
	if value.ContextPrices != nil {
		result.ContextPrices = make([]CatalogContextPrice, len(value.ContextPrices))
	}
	for i, tier := range value.ContextPrices {
		result.ContextPrices[i] = CatalogContextPrice{Threshold: tier.Threshold, Pricing: CloneCatalogPrice(tier.Pricing)}
	}
	return &result
}

// UnmarshalJSON parses a context tier through the catalog parser, retaining absent versus zero cache prices.
func (c *CatalogContextPrice) UnmarshalJSON(body []byte) error {
	var raw struct {
		Threshold int             `json:"threshold"`
		Pricing   json.RawMessage `json:"pricing"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return err
	}
	entries, diagnostics, err := ParsePricingEntries(map[string]json.RawMessage{"tier": raw.Pricing})
	if validationErr := diagnostics.ValidationError(); validationErr != nil {
		return validationErr
	}
	if err != nil {
		return err
	}
	c.Threshold, c.Pricing = raw.Threshold, entries["tier"]
	return nil
}
