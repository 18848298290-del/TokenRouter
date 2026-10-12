package pricing

// DefaultPriceValue is a default price or multiplier with a unit; nil means not applicable.
type DefaultPriceValue struct {
	Key   string   `json:"key"`
	Value *float64 `json:"value"`
	Unit  string   `json:"unit"`
}

// DefaultModelPrice is the administrator's base-price view; quotes apply group and user multipliers separately.
type DefaultModelPrice struct {
	Source                        string                 `json:"source,omitempty"`
	PriceSources                  map[string]string      `json:"price_sources,omitempty"`
	ContextIntervals              []DefaultPriceInterval `json:"context_intervals,omitempty"`
	Model                         string                 `json:"model"`
	Platform                      string                 `json:"platform"`
	BillingMode                   string                 `json:"billing_mode"`
	PriceStatus                   string                 `json:"price_status"`
	Prices                        []DefaultPriceValue    `json:"prices"`
	LongContextThreshold          int                    `json:"long_context_threshold,omitempty"`
	LongContextThresholdInclusive bool                   `json:"long_context_threshold_inclusive,omitempty"`
}

// DefaultPriceInterval represents an absolute context price tier for the administration page.
type DefaultPriceInterval struct {
	MinTokens int                 `json:"min_tokens"`
	MaxTokens *int                `json:"max_tokens"`
	Prices    []DefaultPriceValue `json:"prices"`
}

// DefaultBillingMode classifies published prices, using token pricing when media pricing is unavailable.
func DefaultBillingMode(model, mode string, raw *CatalogModelPricing) string {
	if raw != nil && len(raw.VideoPrices) > 0 {
		return "video"
	}
	imageModel := mode == "image" || HasExplicitImageGenerationPricing(raw) || LooksLikeImageModel(model)
	if raw != nil && len(raw.ImagePrices) > 0 || HasImageUnitPrice(raw) && (imageModel || raw.TokenPricingAbsent) {
		return "image"
	}
	if value, err := ResolveModelPricing(model, raw); err == nil && value != nil {
		return "token"
	}
	switch mode {
	case "image", "image_generation":
		return "image"
	case "video":
		return "video"
	default:
		return "token"
	}
}
