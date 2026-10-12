package pricing

// Administrator price rows need a unit classification even when a catalog
// entry has no usable charge amount. DefaultBillingMode checks declared video
// dimensions before image dimensions or an eligible flat image amount, then
// checks token availability. A chat record's per-image metadata keeps token
// classification unless image-mode or model-name rules select that unit.
//
// The resulting mode and PriceStatus answer separate questions: mode describes
// the row's units, while status describes available prices. Optional amount
// pointers retain free zeros and missing buckets, and context rows keep
// (min,max] endpoints. The billing Calculator adds provenance and default-price
// rows after this classification for the administrator view.

// DefaultPriceValue contains one administrator price amount or factor with its
// unit.
type DefaultPriceValue struct {
	Key string `json:"key"` // catalog price bucket identifier
	// Value is a supplied amount or factor in Unit; nil marks an unavailable or
	// inapplicable bucket. A pointer to zero remains a displayed known value.
	Value *float64 `json:"value"` // optional default amount or factor
	Unit  string   `json:"unit"`  // USD unit or multiplier label
}

// DefaultModelPrice describes catalog base prices for the administrator
// default-price page. Group and user charging factors are applied separately in
// public quotes and settlement; Source and PriceSources describe the original
// catalog data for this view.
type DefaultModelPrice struct {
	// catalog entry provenance label
	Source string `json:"source,omitempty"`
	// per-bucket price provenance map
	PriceSources map[string]string `json:"price_sources,omitempty"`
	// absolute context-tier price rows
	ContextIntervals []DefaultPriceInterval `json:"context_intervals,omitempty"`
	Model            string                 `json:"model"`        // complete catalog model identity
	Platform         string                 `json:"platform"`     // catalog provider display label
	BillingMode      string                 `json:"billing_mode"` // classified price unit mode
	PriceStatus      string                 `json:"price_status"` // priced or unpriced row state
	// base price and modifier rows
	Prices []DefaultPriceValue `json:"prices"`
	// original multiplier-tier context threshold
	LongContextThreshold int `json:"long_context_threshold,omitempty"`
	// LongContextThresholdInclusive identifies whether the original catalog
	// multiplier threshold includes its endpoint. Administrator clients interpret
	// it with LongContextThreshold when displaying that rule.
	// inclusive multiplier-tier threshold flag
	LongContextThresholdInclusive bool `json:"long_context_threshold_inclusive,omitempty"`
}

// DefaultPriceInterval contains absolute administrator prices for one
// (MinTokens,MaxTokens] context range.
type DefaultPriceInterval struct {
	// excluded lower context-token endpoint
	MinTokens int `json:"min_tokens"`
	// MaxTokens is the included upper endpoint for this price row. Nil marks an
	// unbounded final range, and callers retain any referenced endpoint storage.
	// included upper context-token endpoint
	MaxTokens *int                `json:"max_tokens"`
	Prices    []DefaultPriceValue `json:"prices"` // absolute tier price bucket rows
}

// DefaultBillingMode classifies the supplied catalog's published price
// dimensions. A nonempty video map wins, followed by an image map or eligible
// flat image amount. Flat image metadata on a text-priced chat model retains
// token classification unless the image-mode or name conditions apply.
//
// Available token prices then select token mode. When every price lookup is
// unavailable, the supplied image, image_generation, or video mode still
// provides the corresponding classification; other values default to token.
// This function classifies rows independently of their separate priced or
// unpriced status.
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
	// Missing amounts still leave the supplied operation classification useful to
	// administrators; priced status is determined separately from this descriptive
	// mode.
	switch mode {
	case "image", "image_generation":
		return "image"
	case "video":
		return "video"
	default:
		return "token"
	}
}
