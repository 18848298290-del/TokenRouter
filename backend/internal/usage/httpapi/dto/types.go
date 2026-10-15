package dto

import (
	"time"

	keydto "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	billinghttp "github.com/TokenFlux/TokenRouter/internal/billing/httpapi"
	identitydto "github.com/TokenFlux/TokenRouter/internal/identity/httpapi/dto"
	routingdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
)

// types.go defines user and administrator usage-response fields and
// cleanup-task DTOs.
// User usage responses display client model names and charged amounts
// alongside measured token and image details. AdminUsageLog embeds that
// response and adds outbound model observations, provider cost inputs and
// request timings collected from persisted access logs.

type (
	// User names the account response used for loaded usage display relations.
	User = identitydto.User[APIKey]
	// APIKey names the key response containing routing-group display data for
	// usage.
	APIKey = keydto.APIKey[Group]
	// Group names the routing-group response used in nested usage detail
	// display.
	Group = routingdto.Group
	// UserSubscription names the billing grant response used in nested usage
	// details.
	UserSubscription = billinghttp.UserSubscription
)

// UsageLog carries regular-user request details and charged usage values for
// HTTP encoding.
type UsageLog struct {
	ID         int64  `json:"id"`                // usage row identifier
	UserID     int64  `json:"user_id"`           // acting user identifier
	TeamID     *int64 `json:"team_id,omitempty"` // optional team identifier omitted when nil
	APIKeyID   int64  `json:"api_key_id"`        // used key identifier
	ProviderID int64  `json:"provider_id"`       // execution provider identifier
	Platform   string `json:"platform"`          // stored execution platform
	RequestID  string `json:"request_id"`        // local diagnostic request identifier
	// Model is the client display name selected by FromUsage. Nonempty
	// RequestedModel wins; historical records with an empty display name use
	// their stored Model.
	Model                    string  `json:"model"`                                // client display model name
	ServiceTier              *string `json:"service_tier,omitempty"`               // optional forwarded billing tier
	ReasoningEffort          *string `json:"reasoning_effort,omitempty"`           // optional forwarded effort value
	RequestedReasoningEffort *string `json:"requested_reasoning_effort,omitempty"` // optional requested effort value
	InboundEndpoint          *string `json:"inbound_endpoint,omitempty"`           // optional client facing endpoint
	// UpstreamEndpoint is populated by FromUsageAdmin for administrator
	// responses. The regular-user mapper leaves it nil, so JSON omits the
	// outbound endpoint field there.
	UpstreamEndpoint *string `json:"upstream_endpoint,omitempty"` // optional outbound endpoint observation

	GroupID        *int64 `json:"group_id"`        // optional group identifier JSON null
	SubscriptionID *int64 `json:"subscription_id"` // optional subscription identifier JSON null

	InputTokens         int `json:"input_tokens"`          // input token count
	OutputTokens        int `json:"output_tokens"`         // output token count
	CacheCreationTokens int `json:"cache_creation_tokens"` // cache creation token count
	CacheReadTokens     int `json:"cache_read_tokens"`     // cache read token count

	CacheCreation5mTokens int `json:"cache_creation_5m_tokens"` // five minute cache creation tokens
	CacheCreation1hTokens int `json:"cache_creation_1h_tokens"` // one hour cache creation tokens

	InputCost             float64 `json:"input_cost"`              // standard input cost USD
	OutputCost            float64 `json:"output_cost"`             // standard output cost USD
	CacheCreationCost     float64 `json:"cache_creation_cost"`     // cache creation cost USD
	CacheReadCost         float64 `json:"cache_read_cost"`         // cache read cost USD
	TotalCost             float64 `json:"total_cost"`              // standard total cost USD
	ActualCost            float64 `json:"actual_cost"`             // user charged amount USD
	SubscriptionAmountUSD float64 `json:"subscription_amount_usd"` // subscription allocation amount USD
	BalanceAmountUSD      float64 `json:"balance_amount_usd"`      // balance allocation amount USD
	// BillingAllocations holds copied funding entries from
	// cloneBillingAllocationsDTO. Optional subscription and plan identifiers are
	// copied there; empty input becomes an omitted nil slice.
	BillingAllocations        []billing.BillingAllocation `json:"billing_allocations,omitempty"` // per source funding allocation entries
	RateMultiplier            float64                     `json:"rate_multiplier"`               // applied user price multiplier
	LongContextBillingApplied bool                        `json:"long_context_billing_applied"`  // long context increase flag

	BillingType int8 `json:"billing_type"` // balance or subscription category
	// RequestType, Stream and OpenAIWSMode are resolved together by the mapper
	// from the effective category. Special execution categories retain their
	// source flags to keep recorded transport information available.
	RequestType        string `json:"request_type"`         // effective request category label
	Stream             bool   `json:"stream"`               // resolved stream transport flag
	OpenAIWSMode       bool   `json:"openai_ws_mode"`       // resolved WebSocket transport flag
	NativeCompactionV2 bool   `json:"native_compaction_v2"` // remote compaction v2 flag
	DurationMs         *int   `json:"duration_ms"`          // optional upstream duration milliseconds
	FirstTokenMs       *int   `json:"first_token_ms"`       // optional first token delay milliseconds

	ImageCount        int     `json:"image_count"`         // recorded generated image count
	ImageSize         *string `json:"image_size"`          // optional image billing size tier
	ImageInputSize    *string `json:"image_input_size"`    // optional input image size
	ImageOutputSize   *string `json:"image_output_size"`   // optional output image size
	ImageInputTokens  int     `json:"image_input_tokens"`  // image input token count
	ImageInputCost    float64 `json:"image_input_cost"`    // image input cost USD
	ImageOutputTokens int     `json:"image_output_tokens"` // image output token count
	ImageOutputCost   float64 `json:"image_output_cost"`   // image output cost USD
	ImageSizeSource   *string `json:"image_size_source"`   // optional size selection source
	// ImageSizeBreakdown carries image counts grouped by size tier. Usage
	// mappers retain the source map, so callers leave it unchanged during JSON
	// encoding.
	ImageSizeBreakdown map[string]int `json:"image_size_breakdown"` // size tier image count map
	MediaType          *string        `json:"media_type"`           // optional recorded media category

	UserAgent *string `json:"user_agent"`           // optional client user agent
	IPAddress *string `json:"ip_address,omitempty"` // optional client request address
	SessionID *string `json:"session_id,omitempty"` // optional client session correlation identifier

	CacheTTLOverridden bool `json:"cache_ttl_overridden"` // cache TTL price override flag

	BillingMode *string `json:"billing_mode,omitempty"` // optional recorded pricing mode

	CreatedAt time.Time `json:"created_at"` // usage row creation instant

	User         *User             `json:"user,omitempty"`         // loaded actor account response
	APIKey       *APIKey           `json:"api_key,omitempty"`      // loaded key display response
	Group        *Group            `json:"group,omitempty"`        // loaded group display response
	Subscription *UserSubscription `json:"subscription,omitempty"` // loaded funding grant response
}

// AdminUsageLog carries usage details with administrator model, provider-cost
// and timing observations.
type AdminUsageLog struct {
	UsageLog // embedded regular usage response

	// DetailedTiming is joined by the administrator handler from http.access
	// observations. Nil leaves the timing object omitted when lookup finds no
	// usable data.
	DetailedTiming *UsageLogTiming `json:"detailed_timing,omitempty"` // optional request stage observations

	// UpstreamModel retains observed outbound-model text from the source row.
	// Nil omits this field and does not establish whether a mapping occurred.
	UpstreamModel *string `json:"upstream_model,omitempty"` // optional outbound model observation
	// UpstreamResponseModel and UpstreamModelMismatch retain raw response
	// declaration and comparison observations. Missing historical or upstream
	// declarations leave these pointers nil and their JSON fields omitted.
	UpstreamResponseModel *string `json:"upstream_response_model,omitempty"` // optional upstream response model
	UpstreamModelMismatch *bool   `json:"upstream_model_mismatch,omitempty"` // optional response model difference flag
	PricingConfigID       *int64  `json:"pricing_config_id,omitempty"`       // optional selected pricing configuration
	ModelMappingChain     *string `json:"model_mapping_chain,omitempty"`     // optional recorded model mapping chain
	UpstreamRequestID     *string `json:"upstream_request_id,omitempty"`     // optional immediate upstream request identifier
	BillingTier           *string `json:"billing_tier,omitempty"`            // optional selected price tier

	// ProviderRateMultiplier carries the recorded provider multiplier and
	// remains JSON null when absent. Cost readers use one for absent historical
	// multipliers.
	ProviderRateMultiplier *float64 `json:"provider_rate_multiplier"` // optional provider price multiplier
	// ProviderStatsCost is the provider-cost base before the recorded
	// multiplier. Nil omits this field and lets cost readers use TotalCost; zero
	// remains a supplied base.
	ProviderStatsCost *float64 `json:"provider_stats_cost,omitempty"` // optional provider cost base USD

	IPAddress *string `json:"ip_address,omitempty"` // optional client request address

	Provider *ProviderSummary `json:"provider,omitempty"` // optional provider display identity
}

// UsageLogTiming carries observed gateway-stage offsets, event counts and
// upstream connection flags.
type UsageLogTiming struct {
	RequestContentLength *int64 `json:"request_content_length,omitempty"` // observed request body byte count
	// ProviderSlotAcquiredMs, UpstreamGetConnMs, UpstreamGotConnMs,
	// UpstreamWroteRequestMs, UpstreamFirstResponseByteMs,
	// UpstreamFirstSSEDataMs, FirstVisibleOutputMs and FirstDownstreamFlushMs
	// measure offsets from gateway ingress in milliseconds. Nil leaves each
	// unobserved stage omitted.
	ProviderSlotAcquiredMs         *int64 `json:"provider_slot_acquired_ms,omitempty"`          // provider slot offset milliseconds
	UpstreamGetConnMs              *int64 `json:"upstream_get_conn_ms,omitempty"`               // upstream connection request offset milliseconds
	UpstreamGotConnMs              *int64 `json:"upstream_got_conn_ms,omitempty"`               // upstream connection acquisition offset milliseconds
	UpstreamWroteRequestMs         *int64 `json:"upstream_wrote_request_ms,omitempty"`          // upstream write completion offset milliseconds
	UpstreamFirstResponseByteMs    *int64 `json:"upstream_first_response_byte_ms,omitempty"`    // upstream first byte offset milliseconds
	UpstreamFirstSSEDataMs         *int64 `json:"upstream_first_sse_data_ms,omitempty"`         // upstream first SSE offset milliseconds
	FirstVisibleOutputMs           *int64 `json:"first_visible_output_ms,omitempty"`            // first visible output offset milliseconds
	FirstDownstreamFlushMs         *int64 `json:"first_downstream_flush_ms,omitempty"`          // first downstream flush offset milliseconds
	UpstreamGetConnCount           *int64 `json:"upstream_get_conn_count,omitempty"`            // upstream connection request event count
	UpstreamGotConnCount           *int64 `json:"upstream_got_conn_count,omitempty"`            // upstream connection acquired event count
	UpstreamAttemptCount           *int64 `json:"upstream_attempt_count,omitempty"`             // recorded upstream attempt count
	UpstreamFirstResponseByteCount *int64 `json:"upstream_first_response_byte_count,omitempty"` // upstream first byte event count
	UpstreamConnectionReused       bool   `json:"upstream_connection_reused,omitempty"`         // observed upstream connection reuse flag
	UpstreamWroteRequestError      bool   `json:"upstream_wrote_request_error,omitempty"`       // observed upstream write error flag
}

// UsageCleanupFilters carries the filter fields exposed in cleanup-task HTTP
// responses.
type UsageCleanupFilters struct {
	// StartTime and EndTime represent the cleanup store's inclusive bounds. The
	// creation handler expands an end date by a fixed twenty-four hours minus
	// one nanosecond before persisting these instants.
	StartTime   time.Time `json:"start_time"`             // inclusive cleanup start instant
	EndTime     time.Time `json:"end_time"`               // inclusive cleanup end instant
	UserID      *int64    `json:"user_id,omitempty"`      // optional acting user selector
	APIKeyID    *int64    `json:"api_key_id,omitempty"`   // optional key equality selector
	ProviderID  *int64    `json:"provider_id,omitempty"`  // optional provider equality selector
	GroupID     *int64    `json:"group_id,omitempty"`     // optional group equality selector
	Model       *string   `json:"model,omitempty"`        // optional stored model selector
	RequestType *string   `json:"request_type,omitempty"` // optional normalized category label
	Stream      *bool     `json:"stream,omitempty"`       // optional legacy stream selector
	BillingType *int8     `json:"billing_type,omitempty"` // optional funding category selector
}

// UsageCleanupTask carries persisted cleanup status, reported progress and
// operator lifecycle metadata.
type UsageCleanupTask struct {
	ID        int64               `json:"id"`         // persisted cleanup task identifier
	Status    string              `json:"status"`     // persisted cleanup state label
	Filters   UsageCleanupFilters `json:"filters"`    // exposed cleanup selection fields
	CreatedBy int64               `json:"created_by"` // creating administrator user identifier
	// DeletedRows is the task's reported cumulative deletion count. Progress
	// saves occur after batch deletion, so this persisted value can trail
	// committed work when a save fails.
	DeletedRows  int64      `json:"deleted_rows"`            // reported cumulative removed row count
	ErrorMessage *string    `json:"error_message,omitempty"` // optional recorded failure diagnostic
	CanceledBy   *int64     `json:"canceled_by,omitempty"`   // optional canceling administrator identifier
	CanceledAt   *time.Time `json:"canceled_at,omitempty"`   // optional cancellation recording instant
	StartedAt    *time.Time `json:"started_at,omitempty"`    // optional task execution start
	FinishedAt   *time.Time `json:"finished_at,omitempty"`   // optional terminal recording instant
	CreatedAt    time.Time  `json:"created_at"`              // task creation recording instant
	UpdatedAt    time.Time  `json:"updated_at"`              // latest task update instant
}

// ProviderSummary carries the loaded provider identifier and name for
// administrator usage display.
type ProviderSummary struct {
	ID   int64  `json:"id"`   // loaded provider identifier
	Name string `json:"name"` // loaded provider display name
}
