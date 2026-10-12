package usage

import "time"

const (
	ModelSourceRequested = "requested"
	ModelSourceUpstream  = "upstream"
	ModelSourceMapping   = "mapping"
)

// DashboardStats contains administrator dashboard counters and totals.
type DashboardStats struct {
	// User counts.
	TotalUsers    int64 `json:"total_users"`
	TodayNewUsers int64 `json:"today_new_users"` // Users created today.
	ActiveUsers   int64 `json:"active_users"`    // Users with requests today.
	// Users active in the current UTC hour.
	HourlyActiveUsers int64 `json:"hourly_active_users"`

	// Aggregation freshness.
	StatsUpdatedAt string `json:"stats_updated_at"`
	StatsStale     bool   `json:"stats_stale"`

	// API key counts.
	TotalAPIKeys  int64 `json:"total_api_keys"`
	ActiveAPIKeys int64 `json:"active_api_keys"` // Keys with active status.

	// Provider counts by status.
	TotalProviders     int64 `json:"total_providers"`
	NormalProviders    int64 `json:"normal_providers"`    // Active, schedulable providers.
	ErrorProviders     int64 `json:"error_providers"`     // Providers with error status.
	RateLimitProviders int64 `json:"ratelimit_providers"` // Rate-limited providers.
	OverloadProviders  int64 `json:"overload_providers"`  // Overloaded providers.

	// Retained usage totals.
	TotalRequests            int64   `json:"total_requests"`
	TotalInputTokens         int64   `json:"total_input_tokens"`
	TotalOutputTokens        int64   `json:"total_output_tokens"`
	TotalCacheCreationTokens int64   `json:"total_cache_creation_tokens"`
	TotalCacheReadTokens     int64   `json:"total_cache_read_tokens"`
	TotalTokens              int64   `json:"total_tokens"`
	TotalCost                float64 `json:"total_cost"`          // Total cost before the user multiplier.
	TotalActualCost          float64 `json:"total_actual_cost"`   // Total amount charged.
	TotalProviderCost        float64 `json:"total_provider_cost"` // Total provider cost.

	// Today's usage totals.
	TodayRequests            int64   `json:"today_requests"`
	TodayInputTokens         int64   `json:"today_input_tokens"`
	TodayOutputTokens        int64   `json:"today_output_tokens"`
	TodayCacheCreationTokens int64   `json:"today_cache_creation_tokens"`
	TodayCacheReadTokens     int64   `json:"today_cache_read_tokens"`
	TodayTokens              int64   `json:"today_tokens"`
	TodayCost                float64 `json:"today_cost"`          // Today's cost before the user multiplier.
	TodayActualCost          float64 `json:"today_actual_cost"`   // Today's amount charged.
	TodayProviderCost        float64 `json:"today_provider_cost"` // Today's provider cost.

	// Request latency.
	AverageDurationMs float64 `json:"average_duration_ms"` // Average duration in milliseconds.

	// Recent request and token rates.
	Rpm int64 `json:"rpm"` // Requests per minute averaged over the last five minutes.
	Tpm int64 `json:"tpm"` // Tokens per minute averaged over the last five minutes.
}

// TrendDataPoint represents a single point in trend data.
type TrendDataPoint struct {
	Date                string  `json:"date"`
	Requests            int64   `json:"requests"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	Cost                float64 `json:"cost"`        // Cost before the user multiplier.
	ActualCost          float64 `json:"actual_cost"` // Amount charged.
}

// ModelStat represents usage statistics for a single model.
type ModelStat struct {
	Model               string  `json:"model"`
	Requests            int64   `json:"requests"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	Cost                float64 `json:"cost"`          // Cost before the user multiplier.
	ActualCost          float64 `json:"actual_cost"`   // Amount charged.
	ProviderCost        float64 `json:"provider_cost"` // Provider cost.
}

// EndpointStat represents usage statistics for a single request endpoint.
type EndpointStat struct {
	Endpoint    string  `json:"endpoint"`
	Requests    int64   `json:"requests"`
	TotalTokens int64   `json:"total_tokens"`
	Cost        float64 `json:"cost"`        // Cost before the user multiplier.
	ActualCost  float64 `json:"actual_cost"` // Amount charged.
}

// GroupUsageSummary contains today's, yesterday's, and retained-total charges for a group.
type GroupUsageSummary struct {
	GroupID       int64   `json:"group_id"`
	TodayCost     float64 `json:"today_cost"`
	YesterdayCost float64 `json:"yesterday_cost"`
	TotalCost     float64 `json:"total_cost"`
}

// GroupStat represents usage statistics for a single group.
type GroupStat struct {
	GroupID      int64   `json:"group_id"`
	GroupName    string  `json:"group_name"`
	Requests     int64   `json:"requests"`
	TotalTokens  int64   `json:"total_tokens"`
	Cost         float64 `json:"cost"`          // Cost before the user multiplier.
	ActualCost   float64 `json:"actual_cost"`   // Amount charged.
	ProviderCost float64 `json:"provider_cost"` // Provider cost.
}

// UserUsageTrendPoint represents user usage trend data point.
type UserUsageTrendPoint struct {
	Date       string  `json:"date"`
	UserID     int64   `json:"user_id"`
	Email      string  `json:"email"`
	Username   string  `json:"username"`
	Requests   int64   `json:"requests"`
	Tokens     int64   `json:"tokens"`
	Cost       float64 `json:"cost"`        // Cost before the user multiplier.
	ActualCost float64 `json:"actual_cost"` // Amount charged.
}

// UserSpendingRankingItem represents a user spending ranking row.
type UserSpendingRankingItem struct {
	UserID     int64   `json:"user_id"`
	Email      string  `json:"email"`
	Username   string  `json:"username"`
	ActualCost float64 `json:"actual_cost"` // Amount charged.
	Requests   int64   `json:"requests"`
	Tokens     int64   `json:"tokens"`
}

// UserSpendingRankingResponse represents ranking rows plus total spend for the time range.
type UserSpendingRankingResponse struct {
	Ranking         []UserSpendingRankingItem `json:"ranking"`
	TotalActualCost float64                   `json:"total_actual_cost"`
	TotalRequests   int64                     `json:"total_requests"`
	TotalTokens     int64                     `json:"total_tokens"`
}

// UsageRankingItem is one row in the user-visible usage ranking.
type UsageRankingItem struct {
	Rank                int     `json:"rank"`
	UserID              int64   `json:"user_id"`
	DisplayName         string  `json:"display_name"`
	AvatarURL           string  `json:"avatar_url"`
	Requests            int64   `json:"requests"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	ActualCost          float64 `json:"actual_cost"` // Amount charged.
}

// UsageRankingResponse contains user-visible rankings and aggregate totals.
type UsageRankingResponse struct {
	Ranking         []UsageRankingItem `json:"ranking"`
	TotalRequests   int64              `json:"total_requests"`
	TotalTokens     int64              `json:"total_tokens"`
	TotalActualCost float64            `json:"total_actual_cost"`
}

// UserBreakdownItem represents per-user usage breakdown within a dimension (group, model, endpoint).
type UserBreakdownItem struct {
	UserID       int64   `json:"user_id"`
	Email        string  `json:"email"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"input_tokens"`  // Total input tokens.
	OutputTokens int64   `json:"output_tokens"` // Total output tokens.
	CacheTokens  int64   `json:"cache_tokens"`  // Cache creation plus cache read tokens.
	TotalTokens  int64   `json:"total_tokens"`  // Input, output, and cache tokens combined.
	Cost         float64 `json:"cost"`          // Cost before the user multiplier.
	ActualCost   float64 `json:"actual_cost"`   // Amount charged.
	ProviderCost float64 `json:"provider_cost"` // Provider cost.
}

// UserBreakdownDimension specifies the dimension to filter for user breakdown.
type UserBreakdownDimension struct {
	GroupID      int64  // filter by group_id (>0 to enable)
	Model        string // filter by model name (non-empty to enable)
	ModelType    string // "requested", "upstream", or "mapping"
	Endpoint     string // filter by endpoint value (non-empty to enable)
	EndpointType string // "inbound", "upstream", or "path"
	// Additional filter conditions
	UserID      int64  // filter by user_id (>0 to enable)
	APIKeyID    int64  // filter by api_key_id (>0 to enable)
	ProviderID  int64  // filter by provider_id (>0 to enable)
	RequestType *int16 // filter by request_type (non-nil to enable)
	Stream      *bool  // filter by stream flag (non-nil to enable)
	// NativeCompactionV2 filters OpenAI remote compaction v2 requests.
	NativeCompactionV2 *bool
	BillingType        *int8 // filter by billing_type (non-nil to enable)
	// SortBy selects an allowlisted column; empty or invalid values default to actual_cost.
	SortBy string
}

// APIKeyUsageTrendPoint represents API key usage trend data point.
type APIKeyUsageTrendPoint struct {
	Date     string `json:"date"`
	APIKeyID int64  `json:"api_key_id"`
	KeyName  string `json:"key_name"`
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
}

// APIKeyDailyUsagePoint summarizes one API key's usage for a day.
type APIKeyDailyUsagePoint struct {
	Date             string  `json:"date"`
	Requests         int64   `json:"requests"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	Cost             float64 `json:"cost"`        // Cost before the user multiplier.
	ActualCost       float64 `json:"actual_cost"` // Amount charged.
}

// UserDashboardStats contains user dashboard counters and totals.
type UserDashboardStats struct {
	// API key counts.
	TotalAPIKeys  int64 `json:"total_api_keys"`
	ActiveAPIKeys int64 `json:"active_api_keys"`

	// Retained usage totals.
	TotalRequests            int64   `json:"total_requests"`
	TotalInputTokens         int64   `json:"total_input_tokens"`
	TotalOutputTokens        int64   `json:"total_output_tokens"`
	TotalCacheCreationTokens int64   `json:"total_cache_creation_tokens"`
	TotalCacheReadTokens     int64   `json:"total_cache_read_tokens"`
	TotalTokens              int64   `json:"total_tokens"`
	TotalCost                float64 `json:"total_cost"`        // Total cost before the user multiplier.
	TotalActualCost          float64 `json:"total_actual_cost"` // Total amount charged.

	// Today's usage totals.
	TodayRequests            int64   `json:"today_requests"`
	TodayInputTokens         int64   `json:"today_input_tokens"`
	TodayOutputTokens        int64   `json:"today_output_tokens"`
	TodayCacheCreationTokens int64   `json:"today_cache_creation_tokens"`
	TodayCacheReadTokens     int64   `json:"today_cache_read_tokens"`
	TodayTokens              int64   `json:"today_tokens"`
	TodayCost                float64 `json:"today_cost"`        // Today's cost before the user multiplier.
	TodayActualCost          float64 `json:"today_actual_cost"` // Today's amount charged.

	// Request latency in milliseconds.
	AverageDurationMs float64 `json:"average_duration_ms"`

	// Recent request and token rates.
	Rpm int64 `json:"rpm"` // Requests per minute averaged over the last five minutes.
	Tpm int64 `json:"tpm"` // Tokens per minute averaged over the last five minutes.

	// Group by the platform snapshot stored on usage records, as in Ops reports.
	ByPlatform []PlatformDashboardStats `json:"by_platform,omitempty"`
}

// PlatformDashboardStats summarizes usage for one platform.
type PlatformDashboardStats struct {
	Platform        string  `json:"platform"`
	TotalRequests   int64   `json:"total_requests"`
	TotalTokens     int64   `json:"total_tokens"`
	TotalActualCost float64 `json:"total_actual_cost"`
	TodayRequests   int64   `json:"today_requests"`
	TodayTokens     int64   `json:"today_tokens"`
	TodayActualCost float64 `json:"today_actual_cost"`
}

// UsageLogFilters selects usage records for queries.
type UsageLogFilters struct {
	// EndpointSource selects an endpoint chart; empty includes all endpoint dimensions.
	EndpointSource string
	UserID         int64
	APIKeyID       int64
	ProviderID     int64
	GroupID        int64
	TeamID         int64
	// IncludeOwnedTeam includes records from teams owned by the queried user.
	IncludeOwnedTeam bool
	// PersonalOnly selects personal-key records; team scope uses a separate query.
	PersonalOnly bool
	// RequestID resolves local request IDs or external aliases through request_lookup_ids.
	RequestID string
	Model     string
	// ModelFilterSource selects the model dimension; empty matches usage_logs.model.
	ModelFilterSource string
	RequestType       *int16
	Stream            *bool
	// NativeCompactionV2 filters remote compaction v2 requests.
	NativeCompactionV2 *bool
	BillingType        *int8
	BillingMode        string
	StartTime          *time.Time
	EndTime            *time.Time
	// ExactTotal requests an exact COUNT(*); false permits faster pagination on large tables.
	ExactTotal bool
}

// UsageStats contains usage totals and endpoint breakdowns.
type UsageStats struct {
	TotalRequests            int64          `json:"total_requests"`
	TotalInputTokens         int64          `json:"total_input_tokens"`
	TotalOutputTokens        int64          `json:"total_output_tokens"`
	TotalCacheTokens         int64          `json:"total_cache_tokens"`
	TotalCacheCreationTokens int64          `json:"total_cache_creation_tokens"`
	TotalCacheReadTokens     int64          `json:"total_cache_read_tokens"`
	TotalTokens              int64          `json:"total_tokens"`
	TotalCost                float64        `json:"total_cost"`
	TotalActualCost          float64        `json:"total_actual_cost"`
	TotalProviderCost        *float64       `json:"total_provider_cost,omitempty"`
	AverageDurationMs        float64        `json:"average_duration_ms"`
	Endpoints                []EndpointStat `json:"endpoints,omitempty"`
	UpstreamEndpoints        []EndpointStat `json:"upstream_endpoints,omitempty"`
	EndpointPaths            []EndpointStat `json:"endpoint_paths,omitempty"`
}

// PlatformUsage summarizes a user's or API key's costs on one platform.
// Platform uses the snapshot stored when usage was recorded, as in Ops reports.
type PlatformUsage struct {
	Platform        string  `json:"platform"`
	TodayActualCost float64 `json:"today_actual_cost"`
	TotalActualCost float64 `json:"total_actual_cost"`
}

// BatchUserUsageStats is one user's result in a batch usage query.
type BatchUserUsageStats struct {
	UserID          int64           `json:"user_id"`
	TodayActualCost float64         `json:"today_actual_cost"`
	TotalActualCost float64         `json:"total_actual_cost"`
	ByPlatform      []PlatformUsage `json:"by_platform,omitempty"`
}

// BatchAPIKeyUsageStats represents usage stats for a single API key.
type BatchAPIKeyUsageStats struct {
	APIKeyID        int64   `json:"api_key_id"`
	TodayActualCost float64 `json:"today_actual_cost"`
	TotalActualCost float64 `json:"total_actual_cost"`
}

// ProviderUsageHistory contains a provider's daily usage totals.
type ProviderUsageHistory struct {
	Date       string  `json:"date"`
	Label      string  `json:"label"`
	Requests   int64   `json:"requests"`
	Tokens     int64   `json:"tokens"`
	Cost       float64 `json:"cost"`        // Cost before the user multiplier (total_cost).
	ActualCost float64 `json:"actual_cost"` // Provider-cost base multiplied by provider_rate_multiplier.
	UserCost   float64 `json:"user_cost"`   // User charge (actual_cost), including the group multiplier.
}

// ProviderUsageSummary aggregates provider usage over a reporting period.
type ProviderUsageSummary struct {
	Days              int     `json:"days"`
	ActualDaysUsed    int     `json:"actual_days_used"`
	TotalCost         float64 `json:"total_cost"`      // Provider cost.
	TotalUserCost     float64 `json:"total_user_cost"` // User charges.
	TotalStandardCost float64 `json:"total_standard_cost"`
	TotalRequests     int64   `json:"total_requests"`
	TotalTokens       int64   `json:"total_tokens"`
	AvgDailyCost      float64 `json:"avg_daily_cost"` // Average daily provider cost.
	AvgDailyUserCost  float64 `json:"avg_daily_user_cost"`
	AvgDailyRequests  float64 `json:"avg_daily_requests"`
	AvgDailyTokens    float64 `json:"avg_daily_tokens"`
	AvgDurationMs     float64 `json:"avg_duration_ms"`
	Today             *struct {
		Date     string  `json:"date"`
		Cost     float64 `json:"cost"`
		UserCost float64 `json:"user_cost"`
		Requests int64   `json:"requests"`
		Tokens   int64   `json:"tokens"`
	} `json:"today"`
	HighestCostDay *struct {
		Date     string  `json:"date"`
		Label    string  `json:"label"`
		Cost     float64 `json:"cost"`
		UserCost float64 `json:"user_cost"`
		Requests int64   `json:"requests"`
	} `json:"highest_cost_day"`
	HighestRequestDay *struct {
		Date     string  `json:"date"`
		Label    string  `json:"label"`
		Requests int64   `json:"requests"`
		Cost     float64 `json:"cost"`
		UserCost float64 `json:"user_cost"`
	} `json:"highest_request_day"`
}

// ProviderUsageStatsResponse combines provider history, totals, and model/endpoint breakdowns.
type ProviderUsageStatsResponse struct {
	History           []ProviderUsageHistory `json:"history"`
	Summary           ProviderUsageSummary   `json:"summary"`
	Models            []ModelStat            `json:"models"`
	Endpoints         []EndpointStat         `json:"endpoints"`
	UpstreamEndpoints []EndpointStat         `json:"upstream_endpoints"`
}

func IsValidModelSource(source string) bool {
	switch source {
	case ModelSourceRequested, ModelSourceUpstream, ModelSourceMapping:
		return true
	default:
		return false
	}
}

func NormalizeModelSource(source string) string {
	if IsValidModelSource(source) {
		return source
	}
	return ModelSourceRequested
}
