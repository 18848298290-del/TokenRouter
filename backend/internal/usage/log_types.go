package usage

import "time"

// Usage reports separate user charges from standard costs and provider costs,
// while grouping records by model, endpoint, group, payer or key. Query
// filters carry actor and team visibility separately from those reporting
// identities, so a result's user label and a filter's UserID can name
// different participants.

// Model-source labels select the requested, outbound or mapping dimension
// used by usage reporting queries.
const (
	ModelSourceRequested = "requested" // internal request model dimension
	ModelSourceUpstream  = "upstream"  // outbound model reporting dimension
	ModelSourceMapping   = "mapping"   // request to outbound mapping dimension
)

// DashboardStats carries administrator entity counters, usage totals and
// aggregation freshness observations.
type DashboardStats struct {
	TotalUsers        int64 `json:"total_users"`         // registered user total count
	TodayNewUsers     int64 `json:"today_new_users"`     // today registered user count
	ActiveUsers       int64 `json:"active_users"`        // today distinct actor count
	HourlyActiveUsers int64 `json:"hourly_active_users"` // current hour distinct actor count

	// StatsUpdatedAt and StatsStale describe aggregation progress, separately
	// from cache insertion age. DashboardService emits an RFC3339 UTC watermark
	// and reevaluates lag against enablement, interval and lookback on reads.
	StatsUpdatedAt string `json:"stats_updated_at"` // aggregation watermark UTC text
	StatsStale     bool   `json:"stats_stale"`      // aggregation lag stale flag

	TotalAPIKeys  int64 `json:"total_api_keys"`  // current key total count
	ActiveAPIKeys int64 `json:"active_api_keys"` // current active key count

	TotalProviders     int64 `json:"total_providers"`     // current provider total count
	NormalProviders    int64 `json:"normal_providers"`    // active schedulable provider count
	ErrorProviders     int64 `json:"error_providers"`     // error status provider count
	RateLimitProviders int64 `json:"ratelimit_providers"` // currently rate limited provider count
	OverloadProviders  int64 `json:"overload_providers"`  // currently overloaded provider count

	TotalRequests            int64   `json:"total_requests"`              // matching usage record total
	TotalInputTokens         int64   `json:"total_input_tokens"`          // input token total count
	TotalOutputTokens        int64   `json:"total_output_tokens"`         // output token total count
	TotalCacheCreationTokens int64   `json:"total_cache_creation_tokens"` // cache creation token total
	TotalCacheReadTokens     int64   `json:"total_cache_read_tokens"`     // cache read token total
	TotalTokens              int64   `json:"total_tokens"`                // combined general token total
	TotalCost                float64 `json:"total_cost"`                  // standard cost total USD
	TotalActualCost          float64 `json:"total_actual_cost"`           // user charge total USD
	TotalProviderCost        float64 `json:"total_provider_cost"`         // provider cost total USD

	TodayRequests            int64   `json:"today_requests"`              // today usage record count
	TodayInputTokens         int64   `json:"today_input_tokens"`          // today input token count
	TodayOutputTokens        int64   `json:"today_output_tokens"`         // today output token count
	TodayCacheCreationTokens int64   `json:"today_cache_creation_tokens"` // today cache creation token count
	TodayCacheReadTokens     int64   `json:"today_cache_read_tokens"`     // today cache read token count
	TodayTokens              int64   `json:"today_tokens"`                // today combined general tokens
	TodayCost                float64 `json:"today_cost"`                  // today standard cost USD
	TodayActualCost          float64 `json:"today_actual_cost"`           // today user charge USD
	TodayProviderCost        float64 `json:"today_provider_cost"`         // today provider cost USD

	AverageDurationMs float64 `json:"average_duration_ms"` // reported mean duration milliseconds

	Rpm int64 `json:"rpm"` // five minute average request rate
	Tpm int64 `json:"tpm"` // five minute input output token rate
}

// TrendDataPoint carries one formatted time bucket's usage and user charge
// totals.
type TrendDataPoint struct {
	Date                string  `json:"date"`                  // formatted report bucket label
	Requests            int64   `json:"requests"`              // matching usage record count
	InputTokens         int64   `json:"input_tokens"`          // measured input token count
	OutputTokens        int64   `json:"output_tokens"`         // measured output token count
	CacheCreationTokens int64   `json:"cache_creation_tokens"` // measured cache creation token count
	CacheReadTokens     int64   `json:"cache_read_tokens"`     // measured cache read token count
	TotalTokens         int64   `json:"total_tokens"`          // combined general token total
	Cost                float64 `json:"cost"`                  // standard cost sum USD
	ActualCost          float64 `json:"actual_cost"`           // user charge sum USD
}

// ModelStat carries usage totals for one selected model reporting dimension.
type ModelStat struct {
	Model               string  `json:"model"`                 // selected model dimension label
	Requests            int64   `json:"requests"`              // matching usage record count
	InputTokens         int64   `json:"input_tokens"`          // measured input token count
	OutputTokens        int64   `json:"output_tokens"`         // measured output token count
	CacheCreationTokens int64   `json:"cache_creation_tokens"` // measured cache creation token count
	CacheReadTokens     int64   `json:"cache_read_tokens"`     // measured cache read token count
	TotalTokens         int64   `json:"total_tokens"`          // combined general token total
	Cost                float64 `json:"cost"`                  // standard cost sum USD
	ActualCost          float64 `json:"actual_cost"`           // user charge sum USD
	ProviderCost        float64 `json:"provider_cost"`         // provider cost sum USD
}

// EndpointStat carries usage totals for one endpoint label or endpoint-path
// pair.
type EndpointStat struct {
	Endpoint    string  `json:"endpoint"`     // selected endpoint dimension label
	Requests    int64   `json:"requests"`     // matching usage record count
	TotalTokens int64   `json:"total_tokens"` // combined general token total
	Cost        float64 `json:"cost"`         // standard cost sum USD
	ActualCost  float64 `json:"actual_cost"`  // user charge sum USD
}

// GroupUsageSummary carries calendar-day and retained-history user charges
// for one group.
type GroupUsageSummary struct {
	GroupID       int64   `json:"group_id"`       // selected routing group identifier
	TodayCost     float64 `json:"today_cost"`     // today user charge USD
	YesterdayCost float64 `json:"yesterday_cost"` // yesterday user charge USD
	TotalCost     float64 `json:"total_cost"`     // retained user charge USD
}

// GroupStat carries usage totals and display identity for one routing group.
type GroupStat struct {
	GroupID      int64   `json:"group_id"`      // selected routing group identifier
	GroupName    string  `json:"group_name"`    // current group display name
	Requests     int64   `json:"requests"`      // matching usage record count
	TotalTokens  int64   `json:"total_tokens"`  // combined general token total
	Cost         float64 `json:"cost"`          // standard cost sum USD
	ActualCost   float64 `json:"actual_cost"`   // user charge sum USD
	ProviderCost float64 `json:"provider_cost"` // provider cost sum USD
}

// UserUsageTrendPoint carries one payer's usage in a formatted reporting time
// bucket.
type UserUsageTrendPoint struct {
	Date       string  `json:"date"`        // formatted report bucket label
	UserID     int64   `json:"user_id"`     // reported payer user identifier
	Email      string  `json:"email"`       // current payer contact email
	Username   string  `json:"username"`    // current payer display username
	Requests   int64   `json:"requests"`    // matching usage record count
	Tokens     int64   `json:"tokens"`      // combined general token count
	Cost       float64 `json:"cost"`        // standard cost sum USD
	ActualCost float64 `json:"actual_cost"` // user charge sum USD
}

// UserSpendingRankingItem carries a payer's spending totals and current
// display identity.
type UserSpendingRankingItem struct {
	UserID     int64   `json:"user_id"`     // reported payer user identifier
	Email      string  `json:"email"`       // current payer contact email
	Username   string  `json:"username"`    // current payer display username
	ActualCost float64 `json:"actual_cost"` // user charge sum USD
	Requests   int64   `json:"requests"`    // matching usage record count
	Tokens     int64   `json:"tokens"`      // combined general token count
}

// UserSpendingRankingResponse combines limited spending rows with totals
// across the queried range.
type UserSpendingRankingResponse struct {
	// Ranking contains the limited payer rows, while TotalActualCost,
	// TotalRequests and TotalTokens summarize the query's full eligible set.
	// Limiting rows does not reduce those accompanying totals.
	Ranking         []UserSpendingRankingItem `json:"ranking"`           // limited spending ranking rows
	TotalActualCost float64                   `json:"total_actual_cost"` // user charge total USD
	TotalRequests   int64                     `json:"total_requests"`    // matching usage record total
	TotalTokens     int64                     `json:"total_tokens"`      // combined general token total
}

// UsageRankingItem carries a payer's public ranking position and measured
// usage values.
type UsageRankingItem struct {
	Rank                int     `json:"rank"`                  // one based ranking position
	UserID              int64   `json:"user_id"`               // reported payer user identifier
	DisplayName         string  `json:"display_name"`          // public payer display name
	AvatarURL           string  `json:"avatar_url"`            // public payer avatar URL
	Requests            int64   `json:"requests"`              // matching usage record count
	InputTokens         int64   `json:"input_tokens"`          // measured input token count
	OutputTokens        int64   `json:"output_tokens"`         // measured output token count
	CacheCreationTokens int64   `json:"cache_creation_tokens"` // measured cache creation token count
	CacheReadTokens     int64   `json:"cache_read_tokens"`     // measured cache read token count
	TotalTokens         int64   `json:"total_tokens"`          // combined general token total
	ActualCost          float64 `json:"actual_cost"`           // user charge sum USD
}

// UsageRankingResponse combines public payer ranking rows with their
// eligible-range usage totals.
type UsageRankingResponse struct {
	// Ranking contains the limited public payer rows, while TotalRequests,
	// TotalTokens and TotalActualCost summarize the query's full eligible set.
	// Ranking metric eligibility determines which payers contribute.
	Ranking         []UsageRankingItem `json:"ranking"`           // limited public ranking rows
	TotalRequests   int64              `json:"total_requests"`    // matching usage record total
	TotalTokens     int64              `json:"total_tokens"`      // combined general token total
	TotalActualCost float64            `json:"total_actual_cost"` // user charge total USD
}

// UserBreakdownItem carries payer totals within selected group, model or
// endpoint dimensions.
type UserBreakdownItem struct {
	UserID       int64   `json:"user_id"`       // reported payer user identifier
	Email        string  `json:"email"`         // current payer contact email
	Requests     int64   `json:"requests"`      // matching usage record count
	InputTokens  int64   `json:"input_tokens"`  // measured input token count
	OutputTokens int64   `json:"output_tokens"` // measured output token count
	CacheTokens  int64   `json:"cache_tokens"`  // creation plus read cache tokens
	TotalTokens  int64   `json:"total_tokens"`  // combined general token total
	Cost         float64 `json:"cost"`          // standard cost sum USD
	ActualCost   float64 `json:"actual_cost"`   // user charge sum USD
	ProviderCost float64 `json:"provider_cost"` // provider cost sum USD
}

// UserBreakdownDimension selects intersecting report dimensions and
// actor-level query restrictions.
type UserBreakdownDimension struct {
	GroupID int64  // positive group equality selector
	Model   string // nonempty model equality selector
	// ModelType selects the model expression used when Model is supplied.
	// Unsupported or empty values normalize to the internal requested-model
	// dimension.
	ModelType string // selected model reporting dimension
	Endpoint  string // nonempty endpoint equality selector
	// EndpointType selects inbound, upstream or path matching when Endpoint is
	// supplied. Unsupported or empty values resolve to the inbound endpoint
	// column.
	EndpointType string // selected endpoint reporting dimension
	UserID       int64  // positive actor equality selector
	APIKeyID     int64  // positive key equality selector
	ProviderID   int64  // positive provider equality selector
	// RequestType and Stream are independent conditions in PostgreSQL
	// user-breakdown queries. When both pointers are supplied, their conditions
	// intersect, so request-type selection does not discard the stream
	// requirement.
	RequestType        *int16 // optional request category selector
	Stream             *bool  // optional stream equality selector
	NativeCompactionV2 *bool  // optional compaction v2 selector
	BillingType        *int8  // optional funding category selector
	// SortBy selects a fixed repository allowlist column for descending
	// breakdown order. Empty and unsupported names select actual_cost before SQL
	// ordering is formed.
	SortBy string // descending breakdown order column
}

// APIKeyUsageTrendPoint carries one selected key's usage in a formatted time
// bucket.
type APIKeyUsageTrendPoint struct {
	Date     string `json:"date"`       // formatted report bucket label
	APIKeyID int64  `json:"api_key_id"` // reported key identifier
	KeyName  string `json:"key_name"`   // current key display name
	Requests int64  `json:"requests"`   // matching usage record count
	Tokens   int64  `json:"tokens"`     // combined general token count
}

// APIKeyDailyUsagePoint carries one key's daily counts, token classes and
// user charges.
type APIKeyDailyUsagePoint struct {
	Date             string  `json:"date"`               // formatted report bucket label
	Requests         int64   `json:"requests"`           // matching usage record count
	InputTokens      int64   `json:"input_tokens"`       // measured input token count
	OutputTokens     int64   `json:"output_tokens"`      // measured output token count
	CacheReadTokens  int64   `json:"cache_read_tokens"`  // measured cache read token count
	CacheWriteTokens int64   `json:"cache_write_tokens"` // recorded cache creation token count
	TotalTokens      int64   `json:"total_tokens"`       // combined general token total
	Cost             float64 `json:"cost"`               // standard cost sum USD
	ActualCost       float64 `json:"actual_cost"`        // user charge sum USD
}

// UserDashboardStats carries visible usage totals, key counters and platform
// summaries for a user or key.
type UserDashboardStats struct {
	TotalAPIKeys  int64 `json:"total_api_keys"`  // current key total count
	ActiveAPIKeys int64 `json:"active_api_keys"` // current active key count

	TotalRequests            int64   `json:"total_requests"`              // matching usage record total
	TotalInputTokens         int64   `json:"total_input_tokens"`          // input token total count
	TotalOutputTokens        int64   `json:"total_output_tokens"`         // output token total count
	TotalCacheCreationTokens int64   `json:"total_cache_creation_tokens"` // cache creation token total
	TotalCacheReadTokens     int64   `json:"total_cache_read_tokens"`     // cache read token total
	TotalTokens              int64   `json:"total_tokens"`                // combined general token total
	TotalCost                float64 `json:"total_cost"`                  // standard cost total USD
	TotalActualCost          float64 `json:"total_actual_cost"`           // user charge total USD

	TodayRequests            int64   `json:"today_requests"`              // today usage record count
	TodayInputTokens         int64   `json:"today_input_tokens"`          // today input token count
	TodayOutputTokens        int64   `json:"today_output_tokens"`         // today output token count
	TodayCacheCreationTokens int64   `json:"today_cache_creation_tokens"` // today cache creation token count
	TodayCacheReadTokens     int64   `json:"today_cache_read_tokens"`     // today cache read token count
	TodayTokens              int64   `json:"today_tokens"`                // today combined general tokens
	TodayCost                float64 `json:"today_cost"`                  // today standard cost USD
	TodayActualCost          float64 `json:"today_actual_cost"`           // today user charge USD

	AverageDurationMs float64 `json:"average_duration_ms"` // reported mean duration milliseconds

	Rpm int64 `json:"rpm"` // five minute average request rate
	Tpm int64 `json:"tpm"` // five minute input output token rate

	// ByPlatform groups visible usage by the platform stored on each row.
	// Current provider or group configuration changes do not redefine this
	// recorded reporting attribution.
	ByPlatform []PlatformDashboardStats `json:"by_platform,omitempty"` // stored platform usage summaries
}

// PlatformDashboardStats carries retained and today user-charge totals for a
// stored platform.
type PlatformDashboardStats struct {
	Platform        string  `json:"platform"`          // stored execution platform label
	TotalRequests   int64   `json:"total_requests"`    // matching usage record total
	TotalTokens     int64   `json:"total_tokens"`      // combined general token total
	TotalActualCost float64 `json:"total_actual_cost"` // user charge total USD
	TodayRequests   int64   `json:"today_requests"`    // today usage record count
	TodayTokens     int64   `json:"today_tokens"`      // today combined general tokens
	TodayActualCost float64 `json:"today_actual_cost"` // today user charge USD
}

// UsageLogFilters selects record visibility, reporting dimensions and
// optional time bounds for usage reads.
type UsageLogFilters struct {
	// EndpointSource selects inbound, upstream or path breakdowns in
	// GetStatsWithFilters. Empty text requests each available endpoint chart for
	// the same filtered totals.
	EndpointSource string // selected endpoint chart source
	UserID         int64  // positive actor visibility identifier
	APIKeyID       int64  // positive key equality identifier
	ProviderID     int64  // positive provider equality identifier
	GroupID        int64  // positive group equality identifier
	TeamID         int64  // positive team equality identifier
	// IncludeOwnedTeam extends positive UserID visibility to its owned team's
	// other members. PostgreSQL joins those records in disjoint branches so the
	// actor's own rows are counted once.
	IncludeOwnedTeam bool // owned team visibility enable flag
	// PersonalOnly adds a personal-key predicate to the selected visibility.
	// Supplying it alongside a positive TeamID therefore intersects incompatible
	// personal and team scopes.
	PersonalOnly bool // personal key scope selector
	// RequestID selects local diagnostic identity or an external alias through
	// request_lookup_ids. Repository filtering retains user and team
	// restrictions while matching the resolved identifiers.
	RequestID string // request lookup identity text
	Model     string // model equality selector text
	// ModelFilterSource selects the expression used for Model equality. Empty
	// text uses the stored model column; requested, upstream and mapping use
	// their reporting expressions.
	ModelFilterSource string // selected model filter expression
	// RequestType and Stream are optional category and transport selectors.
	// Ordinary raw filters prefer RequestType; analytics filters can apply both,
	// so HTTP ordinary handlers clear Stream when supplying a category.
	RequestType        *int16 // optional request category selector
	Stream             *bool  // optional stream equality selector
	NativeCompactionV2 *bool  // optional compaction v2 selector
	BillingType        *int8  // optional funding category selector
	// BillingMode selects recorded pricing mode, with raw-query compatibility
	// for legacy image, video and token rows. Empty text leaves that selector
	// absent.
	BillingMode string // pricing mode equality selector
	// StartTime and EndTime form optional half-open query bounds: start is
	// inclusive and end is exclusive. Nil pointers leave that side unrestricted
	// for retained-history request lookup and list queries.
	StartTime *time.Time // optional inclusive start instant
	EndTime   *time.Time // optional exclusive end instant
	// ExactTotal requests a complete count for pagination. False permits the
	// repository's faster total strategy only when its user, key, provider and
	// team scope conditions allow it.
	ExactTotal bool // exact pagination total request flag
}

// UsageStats carries filtered usage totals, latency and optional endpoint
// chart breakdowns.
type UsageStats struct {
	TotalRequests            int64   `json:"total_requests"`              // matching usage record total
	TotalInputTokens         int64   `json:"total_input_tokens"`          // input token total count
	TotalOutputTokens        int64   `json:"total_output_tokens"`         // output token total count
	TotalCacheTokens         int64   `json:"total_cache_tokens"`          // creation plus read cache tokens
	TotalCacheCreationTokens int64   `json:"total_cache_creation_tokens"` // cache creation token total
	TotalCacheReadTokens     int64   `json:"total_cache_read_tokens"`     // cache read token total
	TotalTokens              int64   `json:"total_tokens"`                // combined general token total
	TotalCost                float64 `json:"total_cost"`                  // standard cost total USD
	TotalActualCost          float64 `json:"total_actual_cost"`           // user charge total USD
	// TotalProviderCost exposes provider cost when the supplying reader
	// populates it. User HTTP statistics clear this pointer before encoding
	// their response.
	TotalProviderCost *float64       `json:"total_provider_cost,omitempty"` // optional provider cost total USD
	AverageDurationMs float64        `json:"average_duration_ms"`           // reported mean duration milliseconds
	Endpoints         []EndpointStat `json:"endpoints,omitempty"`           // inbound endpoint usage breakdown
	UpstreamEndpoints []EndpointStat `json:"upstream_endpoints,omitempty"`  // upstream endpoint usage breakdown
	EndpointPaths     []EndpointStat `json:"endpoint_paths,omitempty"`      // endpoint path usage breakdown
}

// PlatformUsage carries today's and selected-range user charges grouped by
// recorded execution platform.
type PlatformUsage struct {
	Platform        string  `json:"platform"`          // stored execution platform label
	TodayActualCost float64 `json:"today_actual_cost"` // today user charge USD
	TotalActualCost float64 `json:"total_actual_cost"` // selected range user charge USD
}

// BatchUserUsageStats carries one actor user's batch charges and platform
// breakdowns.
type BatchUserUsageStats struct {
	UserID          int64           `json:"user_id"`               // queried actor user identifier
	TodayActualCost float64         `json:"today_actual_cost"`     // today user charge USD
	TotalActualCost float64         `json:"total_actual_cost"`     // selected range user charge USD
	ByPlatform      []PlatformUsage `json:"by_platform,omitempty"` // recorded platform cost breakdowns
}

// BatchAPIKeyUsageStats carries one key's batch charges for today and the
// selected range.
type BatchAPIKeyUsageStats struct {
	APIKeyID        int64   `json:"api_key_id"`        // reported key identifier
	TodayActualCost float64 `json:"today_actual_cost"` // today user charge USD
	TotalActualCost float64 `json:"total_actual_cost"` // selected range key charge USD
}

// ProviderUsageHistory carries a provider's daily standard costs, provider
// costs and user charges.
type ProviderUsageHistory struct {
	Date       string  `json:"date"`        // formatted report bucket label
	Label      string  `json:"label"`       // provider day display label
	Requests   int64   `json:"requests"`    // matching usage record count
	Tokens     int64   `json:"tokens"`      // combined general token count
	Cost       float64 `json:"cost"`        // standard cost sum USD
	ActualCost float64 `json:"actual_cost"` // provider cost sum USD
	UserCost   float64 `json:"user_cost"`   // user charge sum USD
}

// ProviderUsageSummary carries provider report totals, populated-day averages
// and selected history rows.
type ProviderUsageSummary struct {
	// Days is the PostgreSQL provider range's elapsed twenty-four-hour interval
	// count plus one. Nonpositive derived counts display thirty; the records
	// still come from the supplied query range.
	Days int `json:"days"` // reporting period day count
	// ActualDaysUsed counts populated history rows used as the daily-average
	// denominator. Empty history uses one so zero totals produce defined
	// averages.
	ActualDaysUsed    int     `json:"actual_days_used"`    // daily average populated day denominator
	TotalCost         float64 `json:"total_cost"`          // provider cost total USD
	TotalUserCost     float64 `json:"total_user_cost"`     // user charge total USD
	TotalStandardCost float64 `json:"total_standard_cost"` // standard cost total USD
	TotalRequests     int64   `json:"total_requests"`      // matching usage record total
	TotalTokens       int64   `json:"total_tokens"`        // combined general token total
	// AvgDailyCost, AvgDailyUserCost, AvgDailyRequests and AvgDailyTokens divide
	// their respective totals by ActualDaysUsed. This denominator counts
	// populated history dates rather than every date in the requested period.
	AvgDailyCost     float64 `json:"avg_daily_cost"`      // daily mean provider cost USD
	AvgDailyUserCost float64 `json:"avg_daily_user_cost"` // daily mean user charge USD
	AvgDailyRequests float64 `json:"avg_daily_requests"`  // daily mean request count
	AvgDailyTokens   float64 `json:"avg_daily_tokens"`    // daily mean token count
	AvgDurationMs    float64 `json:"avg_duration_ms"`     // reported mean duration milliseconds
	// Today points to the history row whose date label matches the repository
	// calendar's current date. Nil means no matching history date was returned.
	Today *struct { // optional current date summary
		Date     string  `json:"date"`      // selected history date label
		Cost     float64 `json:"cost"`      // selected day provider cost USD
		UserCost float64 `json:"user_cost"` // selected day user charge USD
		Requests int64   `json:"requests"`  // selected day request count
		Tokens   int64   `json:"tokens"`    // selected day token count
	} `json:"today"`
	// HighestCostDay identifies the first encountered history row at the
	// greatest provider cost. Empty history leaves nil for this selected-day
	// summary.
	HighestCostDay *struct { // optional greatest cost day summary
		Date     string  `json:"date"`      // selected history date label
		Label    string  `json:"label"`     // selected history display label
		Cost     float64 `json:"cost"`      // selected day provider cost USD
		UserCost float64 `json:"user_cost"` // selected day user charge USD
		Requests int64   `json:"requests"`  // selected day request count
	} `json:"highest_cost_day"`
	// HighestRequestDay identifies the first encountered history row at the
	// greatest request count. Empty history leaves nil for this selected-day
	// summary.
	HighestRequestDay *struct { // optional greatest request day summary
		Date     string  `json:"date"`      // selected history date label
		Label    string  `json:"label"`     // selected history display label
		Requests int64   `json:"requests"`  // selected day request count
		Cost     float64 `json:"cost"`      // selected day provider cost USD
		UserCost float64 `json:"user_cost"` // selected day user charge USD
	} `json:"highest_request_day"`
}

// ProviderUsageStatsResponse combines provider history and summary with model
// and endpoint breakdowns.
type ProviderUsageStatsResponse struct {
	History           []ProviderUsageHistory `json:"history"`            // ordered daily provider history
	Summary           ProviderUsageSummary   `json:"summary"`            // provider reporting period summary
	Models            []ModelStat            `json:"models"`             // provider model cost breakdown
	Endpoints         []EndpointStat         `json:"endpoints"`          // provider inbound endpoint breakdown
	UpstreamEndpoints []EndpointStat         `json:"upstream_endpoints"` // provider outbound endpoint breakdown
}

// IsValidModelSource reports whether source names a supported model reporting
// dimension.
func IsValidModelSource(source string) bool {
	switch source {
	case ModelSourceRequested, ModelSourceUpstream, ModelSourceMapping:
		return true
	default:
		return false
	}
}

// NormalizeModelSource returns a supported model reporting dimension or
// requested for other text. It checks exact labels, so callers needing
// whitespace or case normalization handle that before this helper.
func NormalizeModelSource(source string) string {
	if IsValidModelSource(source) {
		return source
	}
	// Unsupported labels keep grouping on the internal request-model expression used
	// by ordinary charts, giving repository dimension selection one predictable default.
	return ModelSourceRequested
}
