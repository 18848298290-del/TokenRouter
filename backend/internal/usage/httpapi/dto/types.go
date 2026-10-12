package dto

import (
	"time"

	keydto "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billinghttp "github.com/TokenFlux/TokenRouter/internal/billing/httpapi"
	identitydto "github.com/TokenFlux/TokenRouter/internal/identity/httpapi/dto"
	routingdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
)

type (
	User             = identitydto.User[APIKey]
	APIKey           = keydto.APIKey[Group]
	Group            = routingdto.Group
	UserSubscription = billinghttp.UserSubscription
)

// UsageLog is the user-visible usage DTO; FromUsageAdmin populates its upstream endpoint.
type UsageLog struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"user_id"`
	TeamID     *int64 `json:"team_id,omitempty"`
	APIKeyID   int64  `json:"api_key_id"`
	ProviderID int64  `json:"provider_id"`
	Platform   string `json:"platform"`
	RequestID  string `json:"request_id"`
	Model      string `json:"model"`
	// ServiceTier records the billing service tier, such as "priority" or "flex"; Claude Fast also uses "priority".
	ServiceTier *string `json:"service_tier,omitempty"`
	// ReasoningEffort is the effort level forwarded upstream.
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	// RequestedReasoningEffort is the client's effort level before policy and model mapping.
	RequestedReasoningEffort *string `json:"requested_reasoning_effort,omitempty"`
	// InboundEndpoint is the client-facing API endpoint path, e.g. /v1/chat/completions.
	InboundEndpoint *string `json:"inbound_endpoint,omitempty"`
	// UpstreamEndpoint is the normalized upstream endpoint path, e.g. /v1/responses.
	UpstreamEndpoint *string `json:"upstream_endpoint,omitempty"`

	GroupID        *int64 `json:"group_id"`
	SubscriptionID *int64 `json:"subscription_id"`

	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`

	CacheCreation5mTokens int `json:"cache_creation_5m_tokens"`
	CacheCreation1hTokens int `json:"cache_creation_1h_tokens"`

	InputCost             float64                     `json:"input_cost"`
	OutputCost            float64                     `json:"output_cost"`
	CacheCreationCost     float64                     `json:"cache_creation_cost"`
	CacheReadCost         float64                     `json:"cache_read_cost"`
	TotalCost             float64                     `json:"total_cost"`
	ActualCost            float64                     `json:"actual_cost"`
	SubscriptionAmountUSD float64                     `json:"subscription_amount_usd"`
	BalanceAmountUSD      float64                     `json:"balance_amount_usd"`
	BillingAllocations    []billing.BillingAllocation `json:"billing_allocations,omitempty"`
	RateMultiplier        float64                     `json:"rate_multiplier"`
	// LongContextBillingApplied reports whether long-context pricing increased the cost.
	LongContextBillingApplied bool `json:"long_context_billing_applied"`

	BillingType  int8   `json:"billing_type"`
	RequestType  string `json:"request_type"`
	Stream       bool   `json:"stream"`
	OpenAIWSMode bool   `json:"openai_ws_mode"`
	// NativeCompactionV2 marks OpenAI remote compaction v2 requests.
	NativeCompactionV2 bool `json:"native_compaction_v2"`
	DurationMs         *int `json:"duration_ms"`
	FirstTokenMs       *int `json:"first_token_ms"`

	// Image generation and image-token billing metadata.
	ImageCount         int            `json:"image_count"`
	ImageSize          *string        `json:"image_size"`
	ImageInputSize     *string        `json:"image_input_size"`
	ImageOutputSize    *string        `json:"image_output_size"`
	ImageInputTokens   int            `json:"image_input_tokens"`
	ImageInputCost     float64        `json:"image_input_cost"`
	ImageOutputTokens  int            `json:"image_output_tokens"`
	ImageOutputCost    float64        `json:"image_output_cost"`
	ImageSizeSource    *string        `json:"image_size_source"`
	ImageSizeBreakdown map[string]int `json:"image_size_breakdown"`
	MediaType          *string        `json:"media_type"`

	// User-Agent
	UserAgent *string `json:"user_agent"`
	// IPAddress is visible to the usage record's owner.
	IPAddress *string `json:"ip_address,omitempty"`
	// SessionID is a client-supplied correlation identifier from session_id or
	// X-Session-Id; absent values are omitted.
	SessionID *string `json:"session_id,omitempty"`

	// CacheTTLOverridden marks an administrator override of cache-TTL billing.
	CacheTTLOverridden bool `json:"cache_ttl_overridden"`

	// BillingMode identifies the pricing mode, such as token, image, or video.
	BillingMode *string `json:"billing_mode,omitempty"`

	CreatedAt time.Time `json:"created_at"`

	User         *User             `json:"user,omitempty"`
	APIKey       *APIKey           `json:"api_key,omitempty"`
	Group        *Group            `json:"group,omitempty"`
	Subscription *UserSubscription `json:"subscription,omitempty"`
}

// AdminUsageLog adds administrator-only diagnostics and provider costs.
type AdminUsageLog struct {
	UsageLog

	// DetailedTiming contains request timings joined from http.access logs for administrators.
	DetailedTiming *UsageLogTiming `json:"detailed_timing,omitempty"`

	// UpstreamModel is the actual model sent to the upstream provider after mapping.
	// Omitted when no mapping was applied (requested model was used as-is).
	UpstreamModel *string `json:"upstream_model,omitempty"`
	// Upstream response-model observations are administrator-only.
	UpstreamResponseModel *string `json:"upstream_response_model,omitempty"`
	UpstreamModelMismatch *bool   `json:"upstream_model_mismatch,omitempty"`
	// PricingConfigID identifies the shared pricing configuration.
	PricingConfigID *int64 `json:"pricing_config_id,omitempty"`
	// ModelMappingChain records model mappings, for example "a→b→c".
	ModelMappingChain *string `json:"model_mapping_chain,omitempty"`
	// UpstreamRequestID is the immediate upstream's declared request ID, visible to administrators.
	UpstreamRequestID *string `json:"upstream_request_id,omitempty"`
	// BillingTier labels the selected per-request or image price tier.
	BillingTier *string `json:"billing_tier,omitempty"`

	// ProviderRateMultiplier snapshots the provider multiplier; nil means 1.0.
	ProviderRateMultiplier *float64 `json:"provider_rate_multiplier"`
	// ProviderStatsCost is the provider-cost base before its multiplier; nil uses total_cost.
	ProviderStatsCost *float64 `json:"provider_stats_cost,omitempty"`

	// IPAddress is the client's request IP.
	IPAddress *string `json:"ip_address,omitempty"`

	// Provider exposes the provider ID and name for display.
	Provider *ProviderSummary `json:"provider,omitempty"`
}

// UsageLogTiming contains stage offsets in milliseconds from gateway ingress, counts, and connection metadata.
type UsageLogTiming struct {
	RequestContentLength           *int64 `json:"request_content_length,omitempty"`
	ProviderSlotAcquiredMs         *int64 `json:"provider_slot_acquired_ms,omitempty"`
	UpstreamGetConnMs              *int64 `json:"upstream_get_conn_ms,omitempty"`
	UpstreamGotConnMs              *int64 `json:"upstream_got_conn_ms,omitempty"`
	UpstreamWroteRequestMs         *int64 `json:"upstream_wrote_request_ms,omitempty"`
	UpstreamFirstResponseByteMs    *int64 `json:"upstream_first_response_byte_ms,omitempty"`
	UpstreamFirstSSEDataMs         *int64 `json:"upstream_first_sse_data_ms,omitempty"`
	FirstVisibleOutputMs           *int64 `json:"first_visible_output_ms,omitempty"`
	FirstDownstreamFlushMs         *int64 `json:"first_downstream_flush_ms,omitempty"`
	UpstreamGetConnCount           *int64 `json:"upstream_get_conn_count,omitempty"`
	UpstreamGotConnCount           *int64 `json:"upstream_got_conn_count,omitempty"`
	UpstreamAttemptCount           *int64 `json:"upstream_attempt_count,omitempty"`
	UpstreamFirstResponseByteCount *int64 `json:"upstream_first_response_byte_count,omitempty"`
	UpstreamConnectionReused       bool   `json:"upstream_connection_reused,omitempty"`
	UpstreamWroteRequestError      bool   `json:"upstream_wrote_request_error,omitempty"`
}
type UsageCleanupFilters struct {
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	UserID      *int64    `json:"user_id,omitempty"`
	APIKeyID    *int64    `json:"api_key_id,omitempty"`
	ProviderID  *int64    `json:"provider_id,omitempty"`
	GroupID     *int64    `json:"group_id,omitempty"`
	Model       *string   `json:"model,omitempty"`
	RequestType *string   `json:"request_type,omitempty"`
	Stream      *bool     `json:"stream,omitempty"`
	BillingType *int8     `json:"billing_type,omitempty"`
}
type UsageCleanupTask struct {
	ID           int64               `json:"id"`
	Status       string              `json:"status"`
	Filters      UsageCleanupFilters `json:"filters"`
	CreatedBy    int64               `json:"created_by"`
	DeletedRows  int64               `json:"deleted_rows"`
	ErrorMessage *string             `json:"error_message,omitempty"`
	CanceledBy   *int64              `json:"canceled_by,omitempty"`
	CanceledAt   *time.Time          `json:"canceled_at,omitempty"`
	StartedAt    *time.Time          `json:"started_at,omitempty"`
	FinishedAt   *time.Time          `json:"finished_at,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

// ProviderSummary contains the provider ID and name for usage display.
// Credentials and proxy configuration are excluded from this response.
type ProviderSummary struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
