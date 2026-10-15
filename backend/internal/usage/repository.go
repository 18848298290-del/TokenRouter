package usage

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// repository.go defines persistence and reporting operations for recorded
// usage.
// Recorded usage supplies analytic rows and reports after completion writes,
// with separate acting-user, billing-user, provider, and cost meanings.
// Repository callers select their authorized scope; report totals describe
// retained records and rollups, while billing persistence determines financial
// transaction outcomes.

// UsageLogRepository provides recorded usage persistence and the report queries
// used by services. Callers authenticate and authorize the selected IDs and
// filter scopes before invoking these operations.
type UsageLogRepository interface {
	// Create writes a usage record and reports whether this call inserted it.
	// PostgreSQL accepts nil as an unwritten successful result; a duplicate
	// financial key or request/key identity returns false with the existing row
	// ID and creation time populated. The repository fills mutable row fields.
	//
	// Callers keep inputs stable while any started batch finishes, including
	// after cancellation returns an unresolved persistence outcome.
	Create(ctx context.Context, log *UsageLog) (inserted bool, err error)
	// GetByID returns the usage row identified by its database row ID. An absent
	// row returns ErrUsageLogNotFound; callers authorize the returned record's
	// acting user before exposing request metadata.
	GetByID(ctx context.Context, id int64) (*UsageLog, error)
	// Delete removes the selected analytic row; PostgreSQL succeeds when that
	// row is already absent. Callers authorize deletion and arrange report or
	// cache repair, since deleting this row does not undo billing.
	Delete(ctx context.Context, id int64) error

	// ListByUser returns paginated usage records for an acting-user ID.
	ListByUser(ctx context.Context, userID int64, params pagination.PaginationParams) ([]UsageLog, *pagination.PaginationResult, error)
	// ListByAPIKey returns paginated usage records for an API key ID.
	ListByAPIKey(ctx context.Context, apiKeyID int64, params pagination.PaginationParams) ([]UsageLog, *pagination.PaginationResult, error)
	// ListByProvider returns paginated usage records for an execution-provider ID.
	ListByProvider(ctx context.Context, providerID int64, params pagination.PaginationParams) ([]UsageLog, *pagination.PaginationResult, error)

	// ListByUserAndTimeRange returns acting-user records in the half-open supplied
	// range. The PostgreSQL implementation caps rows at ten thousand and returns
	// nil pagination, so callers needing a complete listing choose pagination.
	ListByUserAndTimeRange(ctx context.Context, userID int64, startTime, endTime time.Time) ([]UsageLog, *pagination.PaginationResult, error)
	// ListByAPIKeyAndTimeRange returns key records in the half-open supplied
	// range. PostgreSQL caps the result at ten thousand rows and returns nil
	// pagination; a larger complete listing needs paginated queries.
	ListByAPIKeyAndTimeRange(ctx context.Context, apiKeyID int64, startTime, endTime time.Time) ([]UsageLog, *pagination.PaginationResult, error)
	// ListByProviderAndTimeRange returns provider records in the half-open
	// supplied range. PostgreSQL caps the result at ten thousand rows and supplies
	// nil pagination, which callers distinguish from a complete count.
	ListByProviderAndTimeRange(ctx context.Context, providerID int64, startTime, endTime time.Time) ([]UsageLog, *pagination.PaginationResult, error)
	// ListByModelAndTimeRange selects the stored model column within a half-open
	// range. PostgreSQL returns at most ten thousand rows with nil pagination;
	// dimension-aware requested or upstream model grouping uses separate report
	// methods.
	ListByModelAndTimeRange(ctx context.Context, modelName string, startTime, endTime time.Time) ([]UsageLog, *pagination.PaginationResult, error)

	// GetProviderWindowStats summarizes provider records at or after startTime.
	// The PostgreSQL window has no upper bound and includes future-dated rows;
	// provider cost, standard cost, and user charges remain separate.
	GetProviderWindowStats(ctx context.Context, providerID int64, startTime time.Time) (*ProviderStats, error)
	// GetProviderTodayStats summarizes provider records since the injected server
	// calendar's midnight. Its open upper bound includes future-dated rows, and
	// each monetary field retains the cost meaning defined by ProviderStats.
	GetProviderTodayStats(ctx context.Context, providerID int64) (*ProviderStats, error)

	// GetDashboardStats returns global entity counts, recorded totals, activity,
	// and recent request rates.
	GetDashboardStats(ctx context.Context) (*DashboardStats, error)
	// GetUsageTrendWithFilters returns usage buckets over the supplied half-open
	// range with the base scalar selectors. Nil request selectors leave them
	// unrestricted; richer scopes require the unified-filter capability.
	GetUsageTrendWithFilters(ctx context.Context, startTime, endTime time.Time, granularity string, userID, apiKeyID, providerID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) ([]TrendDataPoint, error)
	// GetModelStatsWithFilters groups matching records by their internal
	// request-model dimension. It reads the supplied half-open range and base scalar
	// selectors, with optional request-type filtering taking precedence over
	// legacy stream filtering.
	GetModelStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, requestType *int16, stream *bool, billingType *int8) ([]ModelStat, error)
	// GetEndpointStatsWithFilters groups matching records by the inbound endpoint
	// in the supplied half-open range. Empty endpoint values group as unknown;
	// ActualCost continues to mean user charges when provider filtering is
	// supplied.
	GetEndpointStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) ([]EndpointStat, error)
	// GetUpstreamEndpointStatsWithFilters groups matching records by their
	// upstream endpoint over a half-open range. Empty endpoint values group as
	// unknown, and monetary fields retain standard cost and user-charge meanings.
	GetUpstreamEndpointStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) ([]EndpointStat, error)
	// GetGroupStatsWithFilters returns routed-group totals for a half-open range
	// and base selectors. PostgreSQL reads current group names for presentation,
	// with an absent group ID represented by the zero group.
	GetGroupStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, requestType *int16, stream *bool, billingType *int8) ([]GroupStat, error)
	// GetUserBreakdownStats ranks billing-user totals within the selected model,
	// group, or endpoint restrictions. A positive limit bounds rows; nonpositive
	// limits leave the result unlimited, and invalid sort labels select
	// actual_cost.
	GetUserBreakdownStats(ctx context.Context, startTime, endTime time.Time, dim UserBreakdownDimension, limit int) ([]UserBreakdownItem, error)
	// GetAllGroupUsageSummary returns each group's today, yesterday, and retained
	// total charges. Callers pass the server calendar's local midnight as
	// todayStart so all administrators use the same date range.
	GetAllGroupUsageSummary(ctx context.Context, todayStart time.Time) ([]GroupUsageSummary, error)
	// GetAPIKeyUsageTrend returns date buckets for keys ranked by total tokens in
	// a half-open range. Callers pass a positive limit: PostgreSQL's raw query
	// uses it directly, while its analytics path substitutes twelve for
	// nonpositive values. Unsupported granularity uses daily formatting.
	GetAPIKeyUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) ([]APIKeyUsageTrendPoint, error)
	// GetUserUsageTrend returns date buckets for billing users ranked by total
	// tokens in a half-open range. Callers pass a positive limit: PostgreSQL's raw
	// query uses it directly, while its analytics path substitutes twelve for
	// nonpositive values. Unsupported granularity uses daily formatting.
	GetUserUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) ([]UserUsageTrendPoint, error)
	// GetUserSpendingRanking ranks billing users by actual user charges and
	// returns range totals. PostgreSQL uses twelve rows for nonpositive limits and
	// computes the totals before limiting the ranked rows.
	GetUserSpendingRanking(ctx context.Context, startTime, endTime time.Time, limit int) (*UserSpendingRankingResponse, error)
	// GetUsageRanking returns public rankings using the selected metric and
	// positive-value eligibility. PostgreSQL defaults nonpositive limits to twenty
	// rows and supplies current display names with masked-email fallback.
	GetUsageRanking(ctx context.Context, startTime, endTime time.Time, limit int, sortBy UsageRankingSortBy) (*UsageRankingResponse, error)
	// GetBatchUserUsageStats returns recorded charges for requested positive
	// acting-user IDs with platform breakdowns. Zero bounds select the preceding
	// thirty days through now; the today fields use the server calendar's date
	// independently.
	GetBatchUserUsageStats(ctx context.Context, userIDs []int64, startTime, endTime time.Time) (map[int64]*BatchUserUsageStats, error)
	// GetBatchAPIKeyUsageStats returns charges for requested positive key IDs,
	// including zero results for keys without usage. Zero bounds select the
	// preceding thirty days through now, while today fields use server midnight.
	GetBatchAPIKeyUsageStats(ctx context.Context, apiKeyIDs []int64, startTime, endTime time.Time) (map[int64]*BatchAPIKeyUsageStats, error)

	// GetUserDashboardStats returns a user's records and current owned-team usage
	// without double counting owner rows. API-key counts use that same scope, and
	// duration averages count records with non-null durations.
	GetUserDashboardStats(ctx context.Context, userID int64) (*UserDashboardStats, error)
	// GetAPIKeyDashboardStats returns retained totals and today's values for a
	// key, including future-dated records. Recent RPM and TPM use raw records, and
	// duration averages count records with non-null durations.
	GetAPIKeyDashboardStats(ctx context.Context, apiKeyID int64) (*UserDashboardStats, error)
	// GetUserUsageTrendByUserID returns bucketed usage for the user and current
	// owned team over the supplied half-open range. The repository retains
	// acting-user identity for scope selection before computing totals.
	GetUserUsageTrendByUserID(ctx context.Context, userID int64, startTime, endTime time.Time, granularity string) ([]TrendDataPoint, error)
	// GetUserModelStats groups the user and current owned-team records by internal
	// request model. It accepts a half-open range and keeps owner rows in one
	// branch so team inclusion does not duplicate them.
	GetUserModelStats(ctx context.Context, userID int64, startTime, endTime time.Time) ([]ModelStat, error)

	// ListWithFilters returns matching records and hydrated display associations
	// under the supplied scope. ExactTotal requests a complete count; eligible
	// broad queries can return pagination totals that prove another page rather
	// than count every row.
	ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters UsageLogFilters) ([]UsageLog, *pagination.PaginationResult, error)
	// GetGlobalStats returns raw usage totals over the supplied half-open range.
	// Its duration average excludes NULL duration values, while zero results
	// represent an empty matching source.
	GetGlobalStats(ctx context.Context, startTime, endTime time.Time) (*UsageStats, error)
	// GetStatsWithFilters returns matching totals with selected endpoint charts. A
	// summary failure fails the request; individual endpoint failures can return
	// empty charts, so their absence does not establish zero endpoint traffic.
	GetStatsWithFilters(ctx context.Context, filters UsageLogFilters) (*UsageStats, error)

	// GetProviderUsageStats returns daily provider history, cost summaries, and
	// model and endpoint totals. Its daily averages divide by populated history
	// dates, using one as the denominator for an empty report.
	GetProviderUsageStats(ctx context.Context, providerID int64, startTime, endTime time.Time) (*ProviderUsageStatsResponse, error)

	// GetUserStatsAggregated computes raw acting-user totals in SQL over a
	// half-open range. Its duration average treats NULL durations as zero, which
	// differs from summaries counting only non-null observations.
	GetUserStatsAggregated(ctx context.Context, userID int64, startTime, endTime time.Time) (*UsageStats, error)
	// GetAPIKeyStatsAggregated computes raw key totals in SQL over a half-open
	// range. Its duration average treats NULL durations as zero rather than
	// omitting those records from the denominator.
	GetAPIKeyStatsAggregated(ctx context.Context, apiKeyID int64, startTime, endTime time.Time) (*UsageStats, error)
	// GetProviderStatsAggregated computes raw provider-selected usage totals over
	// a half-open range. ActualCost remains the user charge, and the duration
	// average treats records with NULL durations as zero.
	GetProviderStatsAggregated(ctx context.Context, providerID int64, startTime, endTime time.Time) (*UsageStats, error)
	// GetModelStatsAggregated computes raw totals for the stored model column over
	// a half-open range. It treats NULL durations as zero and does not select a
	// requested/upstream model dimension.
	GetModelStatsAggregated(ctx context.Context, modelName string, startTime, endTime time.Time) (*UsageStats, error)
	// GetDailyStatsAggregated returns daily acting-user totals as maps over a
	// half-open range. PostgreSQL groups by the resolved server timezone and
	// treats NULL durations as zero in each average.
	GetDailyStatsAggregated(ctx context.Context, userID int64, startTime, endTime time.Time) ([]map[string]any, error)
}
