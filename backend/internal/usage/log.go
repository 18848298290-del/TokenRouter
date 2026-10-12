package usage

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

type UsageLog struct {
	ID            int64
	UserID        int64
	BillingUserID int64
	TeamID        *int64
	APIKeyID      int64
	ProviderID    int64
	// Platform snapshots the execution provider's platform; migrations retain historical reporting attribution.
	Platform  string
	RequestID string
	// BillingKey is the financial-operation deduplication key linked from the request record.
	BillingKey string
	Model      string
	// RequestedModel is the client-requested model name recorded for stable user/admin display.
	// Empty should be treated as Model for backward compatibility with historical rows.
	RequestedModel string
	// UpstreamModel is the actual model sent to the upstream provider after mapping.
	// Nil means no mapping was applied (requested model was used as-is).
	UpstreamModel *string
	// UpstreamResponseModel stores the raw response model declaration for diagnostics, not billing.
	UpstreamResponseModel *string
	// UpstreamModelMismatch is nil when the upstream did not declare a model.
	UpstreamModelMismatch *bool
	// PricingConfigID identifies the shared pricing configuration.
	PricingConfigID *int64
	// ModelMappingChain records model mappings, for example "a→b→c".
	ModelMappingChain *string
	// BillingTier labels the selected per-request or image price tier.
	BillingTier *string
	// BillingMode identifies the pricing mode, such as token, image, or video.
	BillingMode *string
	// ServiceTier stores the normalized billing tier; Claude Fast also uses "priority".
	ServiceTier *string
	// ReasoningEffort is the forwarded effort after group policy and model-family normalization.
	// Examples include OpenAI "low", "medium", "high", "xhigh", and "max", or Claude "low", "medium", "high", and "max".
	// Nil means absent or inapplicable.
	ReasoningEffort *string
	// RequestedReasoningEffort is the client's effort before policy rewriting.
	// Historical values may be absent; display code then uses ReasoningEffort.
	RequestedReasoningEffort *string
	// InboundEndpoint is the client-facing API endpoint path, e.g. /v1/chat/completions.
	InboundEndpoint *string
	// UpstreamEndpoint is the normalized upstream endpoint path, e.g. /v1/responses.
	UpstreamEndpoint *string

	GroupID        *int64
	SubscriptionID *int64

	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int

	CacheCreation5mTokens int `gorm:"column:cache_creation_5m_tokens"`
	CacheCreation1hTokens int `gorm:"column:cache_creation_1h_tokens"`

	ImageInputTokens  int
	ImageInputCost    float64
	ImageOutputTokens int
	ImageOutputCost   float64

	InputCost                 float64
	OutputCost                float64
	CacheCreationCost         float64
	CacheReadCost             float64
	TotalCost                 float64
	ActualCost                float64
	SubscriptionAmountUSD     float64
	BalanceAmountUSD          float64
	BillingAllocations        []billing.BillingAllocation
	RateMultiplier            float64
	LongContextBillingApplied bool // Whether long-context rules increased the cost.
	// ProviderRateMultiplier snapshots the provider multiplier; historical nil values mean 1.0.
	ProviderRateMultiplier *float64
	// ProviderStatsCost is the precomputed provider-cost base; nil uses total_cost before applying the provider multiplier.
	ProviderStatsCost *float64

	BillingType  int8
	RequestType  RequestType
	Stream       bool
	OpenAIWSMode bool
	// NativeCompactionV2 marks OpenAI remote compaction v2 requests identified at runtime.
	NativeCompactionV2 bool
	DurationMs         *int
	FirstTokenMs       *int
	UserAgent          *string
	IPAddress          *string
	// SessionID is a client-supplied correlation ID from session_id or X-Session-Id.
	// Absent or invalid values stay nil; prompt_cache_key and request content are not sources.
	SessionID *string
	// UpstreamRequestID is the immediate upstream's response-header request ID.
	// The header is selected by provider extra.upstream_request_id_header; unset configuration,
	// WebSocket turns, and responses without that header leave it nil.
	UpstreamRequestID *string

	// CacheTTLOverridden marks an administrator override of cache-TTL billing.
	CacheTTLOverridden bool

	// Image generation metadata.
	ImageCount         int
	ImageSize          *string
	ImageInputSize     *string
	ImageOutputSize    *string
	ImageSizeSource    *string
	ImageSizeBreakdown map[string]int
	MediaType          *string

	// Grok video is billed per second; rows with video_count > 0 do not require image_size.
	VideoCount           int
	VideoResolution      *string
	VideoDurationSeconds *int

	CreatedAt time.Time

	User         *UserView
	APIKey       *KeyView
	Provider     *ProviderView
	Group        *GroupView
	Subscription *billing.UserSubscription
}

func (u *UsageLog) TotalTokens() int {
	return u.InputTokens + u.OutputTokens + u.CacheCreationTokens + u.CacheReadTokens
}

func (u *UsageLog) EffectiveRequestType() RequestType {
	if u == nil {
		return RequestTypeUnknown
	}
	if normalized := u.RequestType.Normalize(); normalized != RequestTypeUnknown {
		return normalized
	}
	return RequestTypeFromLegacy(u.Stream, u.OpenAIWSMode)
}

func (u *UsageLog) SyncRequestTypeAndLegacyFields() {
	if u == nil {
		return
	}
	requestType := u.EffectiveRequestType()
	u.RequestType = requestType
	u.Stream, u.OpenAIWSMode = ApplyLegacyRequestFields(requestType, u.Stream, u.OpenAIWSMode)
}
