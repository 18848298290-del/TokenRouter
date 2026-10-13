package usage

import (
	"context"
	"time"
)

// Usage query repositories can expose unified filters or extra model dimensions
// beyond the base interface. UsageService selects those capabilities at
// construction and uses reduced-argument repository queries when a selected
// capability is absent. Callers choose a reader whose supported selectors match
// the scope of their query.

// UsageTrendWithFiltersRepo supplies trend queries accepting the unified usage
// filter object.
type UsageTrendWithFiltersRepo interface {
	// GetUsageTrendWithUsageFilters accepts a half-open range and unified
	// selectors carrying team, personal, owned-team, and request-ID fields beyond
	// the base signature. Unknown granularities use daily formatting in
	// PostgreSQL. When other filters allow its legacy dashboard-bucket path,
	// PostgreSQL can return those buckets before applying RequestID.
	GetUsageTrendWithUsageFilters(ctx context.Context, startTime, endTime time.Time, granularity string, filters UsageLogFilters) ([]TrendDataPoint, error)
}

// ModelStatsWithUsageFiltersRepo supplies selected-model totals accepting the
// unified usage filter object.
type ModelStatsWithUsageFiltersRepo interface {
	// GetModelStatsWithUsageFiltersBySource returns model totals for a selected
	// dimension and unified filters. UsageService normalizes source before
	// calling; this capability carries scope restrictions beyond the base scalar
	// method.
	GetModelStatsWithUsageFiltersBySource(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters, source string) ([]ModelStat, error)
}

// ModelStatsBySourceRepo supplies model grouping for the base scalar filter
// set.
type ModelStatsBySourceRepo interface {
	// GetModelStatsWithFiltersBySource groups records by the selected requested,
	// upstream, or mapping model dimension. The arguments represent base
	// selectors; additional unified scope fields require
	// ModelStatsWithUsageFiltersRepo. PostgreSQL uses requested-model grouping
	// for an unrecognized source value.
	GetModelStatsWithFiltersBySource(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, requestType *int16, stream *bool, billingType *int8, source string) ([]ModelStat, error)
}

// GroupStatsWithUsageFiltersRepo supplies group totals accepting the unified
// usage filter object.
type GroupStatsWithUsageFiltersRepo interface {
	// GetGroupStatsWithUsageFilters returns group totals for a range with unified
	// selectors. This capability carries team, personal, owned-team, and
	// request-ID restrictions that are absent from the base scalar method. PostgreSQL's
	// model text selector uses the request-model dimension regardless of the
	// supplied ModelFilterSource.
	GetGroupStatsWithUsageFilters(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters) ([]GroupStat, error)
}

// UserStatsReader supplies user summaries with reduced upstream endpoint
// detail.
type UserStatsReader interface {
	// GetUserStatsWithFilters returns the user summary under the supplied unified
	// scope. Its PostgreSQL implementation skips upstream endpoint and path-detail
	// queries, and the HTTP caller also removes provider cost from the response.
	GetUserStatsWithFilters(context.Context, UsageLogFilters) (*UsageStats, error)
}

// QueryReaders carries optional query capabilities selected when UsageService
// is constructed.
type QueryReaders struct {
	// UsageTrendWithFiltersRepo, ModelStatsWithUsageFiltersRepo,
	// ModelStatsBySourceRepo, GroupStatsWithUsageFiltersRepo, and UserStatsReader
	// are optional query capabilities. Nil lets the service try its remaining
	// paths; model queries can try another capability before the base method.
	// Supplying QueryReaders to NewUsageService replaces the discovered set in
	// full, including fields left nil.
	UsageTrendWithFiltersRepo      UsageTrendWithFiltersRepo      // unified filtered trend reader
	ModelStatsWithUsageFiltersRepo ModelStatsWithUsageFiltersRepo // unified model totals reader
	ModelStatsBySourceRepo         ModelStatsBySourceRepo         // scalar model dimension reader
	GroupStatsWithUsageFiltersRepo GroupStatsWithUsageFiltersRepo // unified group totals reader
	UserStatsReader                UserStatsReader                // reduced user summary reader
}

// queryReaders discovers optional query capabilities implemented by the
// supplied repository. Failed capability assertions leave individual fields
// nil, letting UsageService try its remaining optional capabilities or the
// base repository path for each query.
func queryReaders(repo UsageLogRepository) QueryReaders {
	out := QueryReaders{}
	out.UsageTrendWithFiltersRepo, _ = repo.(UsageTrendWithFiltersRepo)
	out.ModelStatsWithUsageFiltersRepo, _ = repo.(ModelStatsWithUsageFiltersRepo)
	out.ModelStatsBySourceRepo, _ = repo.(ModelStatsBySourceRepo)
	out.GroupStatsWithUsageFiltersRepo, _ = repo.(GroupStatsWithUsageFiltersRepo)
	out.UserStatsReader, _ = repo.(UserStatsReader)
	return out
}
