package usage

import (
	"context"
	"time"
)

type UsageTrendWithFiltersRepo interface {
	GetUsageTrendWithUsageFilters(ctx context.Context, startTime, endTime time.Time, granularity string, filters UsageLogFilters) ([]TrendDataPoint, error)
}
type ModelStatsWithUsageFiltersRepo interface {
	GetModelStatsWithUsageFiltersBySource(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters, source string) ([]ModelStat, error)
}
type ModelStatsBySourceRepo interface {
	GetModelStatsWithFiltersBySource(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, requestType *int16, stream *bool, billingType *int8, source string) ([]ModelStat, error)
}
type GroupStatsWithUsageFiltersRepo interface {
	GetGroupStatsWithUsageFilters(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters) ([]GroupStat, error)
}
type UserStatsReader interface {
	GetUserStatsWithFilters(context.Context, UsageLogFilters) (*UsageStats, error)
}

// QueryReaders contains optional query capabilities. Nil fields select the
// UsageLogRepository methods used by the corresponding service operation.
type QueryReaders struct {
	UsageTrendWithFiltersRepo      UsageTrendWithFiltersRepo
	ModelStatsWithUsageFiltersRepo ModelStatsWithUsageFiltersRepo
	ModelStatsBySourceRepo         ModelStatsBySourceRepo
	GroupStatsWithUsageFiltersRepo GroupStatsWithUsageFiltersRepo
	UserStatsReader                UserStatsReader
}

func queryReaders(repo UsageLogRepository) QueryReaders {
	out := QueryReaders{}
	out.UsageTrendWithFiltersRepo, _ = repo.(UsageTrendWithFiltersRepo)
	out.ModelStatsWithUsageFiltersRepo, _ = repo.(ModelStatsWithUsageFiltersRepo)
	out.ModelStatsBySourceRepo, _ = repo.(ModelStatsBySourceRepo)
	out.GroupStatsWithUsageFiltersRepo, _ = repo.(GroupStatsWithUsageFiltersRepo)
	out.UserStatsReader, _ = repo.(UserStatsReader)
	return out
}
