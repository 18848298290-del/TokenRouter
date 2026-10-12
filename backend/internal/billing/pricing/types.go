package pricing

import "time"

// ModelPricing holds per-token settlement prices and model billing rules.
type ModelPricing struct {
	// TimePricing holds catalog time rules used when charging catalog prices.
	TimePricing                        *TimePricingConfig
	CatalogSource                      string
	PriorityInputPresent               bool
	PriorityOutputPresent              bool
	PriorityCacheReadPresent           bool
	PriorityCacheWritePresent          bool
	ContextPrices                      []ContextModelPrice
	InputPricePerToken                 float64  // USD per input token.
	InputPricePerTokenPriority         float64  // USD per priority-tier input token.
	ImageInputPricePerToken            float64  // USD per image input token; zero uses the text input price.
	OutputPricePerToken                float64  // USD per output token.
	OutputPricePerTokenPriority        float64  // USD per priority-tier output token.
	CacheCreationPricePerToken         float64  // USD per cache-write token.
	CacheCreationPricePerTokenPriority float64  // USD per priority-tier cache-write token.
	CacheCreationPriceExplicit         bool     // Marks an explicit cache-write price, including free pricing.
	CacheReadPricePerToken             float64  // USD per cache-read token.
	CacheReadPricePerTokenPriority     float64  // USD per priority-tier cache-read token.
	CacheCreation5mPrice               float64  // USD per 5m cache-write token.
	CacheCreation1hPrice               float64  // USD per 1h cache-write token.
	SupportsCacheBreakdown             bool     // Whether separate cache TTL prices are supported.
	SupportsServiceTier                bool     // Whether service-tier pricing is supported.
	FastModeMultiplier                 *float64 // Fast multiplier; nil inherits the model's default Fast pricing.
	FastMultiplier                     *float64 // Fast/priority multiplier.
	FlexMultiplier                     *float64 // Configured Flex multiplier.
	// MaxReasoningEffortMultiplier applies when the final reasoning effort is max.
	MaxReasoningEffortMultiplier  *float64
	LongContextInputThreshold     int     // Context threshold for pricing the whole usage at long-context rates.
	LongContextThresholdInclusive bool    // True includes the threshold (xAI); false requires usage above it.
	LongContextInputMultiplier    float64 // Input-side multiplier for the whole long-context usage.
	LongContextOutputMultiplier   float64 // Output multiplier for the whole long-context usage.
	ImageOutputPricePerToken      float64 // USD per image output token.
	ImageOutputPriceExplicit      bool    // An explicit image-output price disables text-price inheritance, including at zero.
}

// ContextModelPrice holds absolute prices for the whole usage strictly above Threshold.
// The catalog converter subtracts one from inclusive thresholds so billing and display share (min, max] ranges.
type ContextModelPrice struct {
	Threshold int
	Pricing   *ModelPricing
}

// UsageTokens holds reported text, image, and cache token counts.
type UsageTokens struct {
	InputTokens           int
	ImageInputTokens      int
	OutputTokens          int
	CacheCreationTokens   int
	CacheReadTokens       int
	CacheCreation5mTokens int
	CacheCreation1hTokens int
	ImageOutputTokens     int
}

// CostBreakdown holds base cost buckets and the final charge.
type CostBreakdown struct {
	InputCost                 float64 // Text input cost; image input is recorded separately.
	ImageInputCost            float64 // Image input token cost, such as image-editing input.
	OutputCost                float64
	ImageOutputCost           float64
	CacheCreationCost         float64
	CacheReadCost             float64
	TotalCost                 float64
	ActualCost                float64 // Charge after applying the rate multiplier.
	BillingMode               string  // Billing units: token, per_request, image, or video.
	LongContextBillingApplied bool    // Whether long-context pricing increased the charge.
}

// ModelDisplayPricing is a model price snapshot for frontend display.
// Displayed unit prices include the supplied rate multiplier; effort and time modifiers are separate.
type ModelDisplayPricing struct {
	PricingMode             string
	PriceStatus             string
	InputPricePerToken      float64
	ImageInputPricePerToken float64
	OutputPricePerToken     float64
	CacheWritePricePerToken float64
	// CacheWrite1hPricePerToken is the optional displayed 1h cache-write price.
	CacheWrite1hPricePerToken     float64
	CacheReadPricePerToken        float64
	ImageOutputPricePerToken      float64
	FastInputPricePerToken        float64
	FastImageInputPricePerToken   float64
	FastOutputPricePerToken       float64
	FastCacheWritePricePerToken   float64
	FastCacheWrite1hPricePerToken float64
	FastCacheReadPricePerToken    float64
	FastImageOutputPricePerToken  float64
	ContextIntervals              []ModelDisplayPricingInterval
	// MaxReasoningEffortMultiplier scales token prices at max effort; absent or unit multipliers are nil.
	MaxReasoningEffortMultiplier *float64
	// TimePricing supplies request-time multipliers; the displayed unit prices above exclude them.
	TimePricing *TimePricingConfig
	// ImagePriceSizes records quoted sizes so zero prices can be distinguished from missing prices.
	ImagePriceSizes []string
	ImagePrice1K    float64
	ImagePrice2K    float64
	ImagePrice4K    float64
}

// ModelDisplayPricingInterval holds displayed prices for a context token range.
type ModelDisplayPricingInterval struct {
	MinTokens                     int
	MaxTokens                     *int
	InputPricePerToken            float64
	ImageInputPricePerToken       float64
	OutputPricePerToken           float64
	CacheWritePricePerToken       float64
	CacheWrite1hPricePerToken     float64
	CacheReadPricePerToken        float64
	ImageOutputPricePerToken      float64
	FastInputPricePerToken        float64
	FastImageInputPricePerToken   float64
	FastOutputPricePerToken       float64
	FastCacheWritePricePerToken   float64
	FastCacheWrite1hPricePerToken float64
	FastCacheReadPricePerToken    float64
	FastImageOutputPricePerToken  float64
}

type AudioPriceConfig struct {
	RealtimePerMin *float64
	TTSPerMChars   *float64
	STTPerHour     *float64
}

// CostInput supplies the model, usage, multipliers, tiers, and times for one calculation.
type CostInput struct {
	ModelPricingAt      time.Time
	ModelTimeLocation   *time.Location
	Model               string
	Tokens              UsageTokens
	RequestCount        int
	UsageUnits          float64
	SizeTier            string
	RateMultiplier      float64
	PricingAt           time.Time
	ServiceTier         string
	ReasoningEffort     string
	TimePricingLocation *time.Location
}
