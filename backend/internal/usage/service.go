package usage

import (
	"context"
	"fmt"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
)

var ErrUsageLogNotFound = infraerrors.NotFound("USAGE_LOG_NOT_FOUND", "usage log not found")

// UsageService reads usage logs, summaries, and rankings.
type UsageService struct {
	statsQueryCache *querycache.Cache
	usageRepo       UsageLogRepository
	readers         QueryReaders
}

// NewUsageService creates a usage reader with a 30-second statistics cache.
func NewUsageService(usageRepo UsageLogRepository, optional ...QueryReaders) *UsageService {
	readers := queryReaders(usageRepo)
	if len(optional) > 0 {
		readers = optional[0]
	}
	return &UsageService{
		statsQueryCache: querycache.NewCache(30 * time.Second),
		usageRepo:       usageRepo,
		readers:         readers,
	}
}

// GetByID returns the usage log with the given ID.
func (s *UsageService) GetByID(ctx context.Context, id int64) (*UsageLog, error) {
	log, err := s.usageRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get usage log: %w", err)
	}
	return log, nil
}

// ListByUser lists a user's usage logs.
func (s *UsageService) ListByUser(ctx context.Context, userID int64, params pagination.PaginationParams) ([]UsageLog, *pagination.PaginationResult, error) {
	logs, pagination, err := s.usageRepo.ListByUser(ctx, userID, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list usage logs: %w", err)
	}
	return logs, pagination, nil
}

// ListByAPIKey lists an API key's usage logs.
func (s *UsageService) ListByAPIKey(ctx context.Context, apiKeyID int64, params pagination.PaginationParams) ([]UsageLog, *pagination.PaginationResult, error) {
	logs, pagination, err := s.usageRepo.ListByAPIKey(ctx, apiKeyID, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list usage logs: %w", err)
	}
	return logs, pagination, nil
}

// ListByProvider lists a provider's usage logs.
func (s *UsageService) ListByProvider(ctx context.Context, providerID int64, params pagination.PaginationParams) ([]UsageLog, *pagination.PaginationResult, error) {
	logs, pagination, err := s.usageRepo.ListByProvider(ctx, providerID, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list usage logs: %w", err)
	}
	return logs, pagination, nil
}

// GetUsageRanking ranks users by the selected metric within the time range.
func (s *UsageService) GetUsageRanking(ctx context.Context, startTime, endTime time.Time, limit int, sortBy UsageRankingSortBy) (*UsageRankingResponse, error) {
	ranking, err := s.usageRepo.GetUsageRanking(ctx, startTime, endTime, limit, sortBy)
	if err != nil {
		return nil, fmt.Errorf("get usage ranking: %w", err)
	}
	return ranking, nil
}

// Delete removes a usage log. Callers must authorize this administrator operation.
func (s *UsageService) Delete(ctx context.Context, id int64) error {
	if err := s.usageRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete usage log: %w", err)
	}
	return nil
}

// GetUserDashboardStats returns dashboard totals for a user.
func (s *UsageService) GetUserDashboardStats(ctx context.Context, userID int64) (*UserDashboardStats, error) {
	stats, err := s.usageRepo.GetUserDashboardStats(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user dashboard stats: %w", err)
	}
	return stats, nil
}

// GetAPIKeyDashboardStats returns dashboard totals for an API key.
func (s *UsageService) GetAPIKeyDashboardStats(ctx context.Context, apiKeyID int64) (*UserDashboardStats, error) {
	stats, err := s.usageRepo.GetAPIKeyDashboardStats(ctx, apiKeyID)
	if err != nil {
		return nil, fmt.Errorf("get api key dashboard stats: %w", err)
	}
	return stats, nil
}

// GetUserUsageTrendByUserID returns a user's usage trend.
func (s *UsageService) GetUserUsageTrendByUserID(ctx context.Context, userID int64, startTime, endTime time.Time, granularity string) ([]TrendDataPoint, error) {
	trend, err := s.usageRepo.GetUserUsageTrendByUserID(ctx, userID, startTime, endTime, granularity)
	if err != nil {
		return nil, fmt.Errorf("get user usage trend: %w", err)
	}
	return trend, nil
}

// GetUsageTrendWithFilters returns usage trends using the repository's supported filters.
func (s *UsageService) GetUsageTrendWithFilters(ctx context.Context, startTime, endTime time.Time, granularity string, filters UsageLogFilters) ([]TrendDataPoint, error) {
	if filterRepo := s.readers.UsageTrendWithFiltersRepo; filterRepo != nil {
		trend, err := filterRepo.GetUsageTrendWithUsageFilters(ctx, startTime, endTime, granularity, filters)
		if err != nil {
			return nil, fmt.Errorf("get usage trend with filters: %w", err)
		}
		return trend, nil
	}
	trend, err := s.usageRepo.GetUsageTrendWithFilters(ctx, startTime, endTime, granularity, filters.UserID, filters.APIKeyID, filters.ProviderID, filters.GroupID, filters.Model, filters.RequestType, filters.Stream, filters.BillingType)
	if err != nil {
		return nil, fmt.Errorf("get usage trend with filters: %w", err)
	}
	return trend, nil
}

// GetUserModelStats returns a user's model usage.
func (s *UsageService) GetUserModelStats(ctx context.Context, userID int64, startTime, endTime time.Time) ([]ModelStat, error) {
	stats, err := s.usageRepo.GetUserModelStats(ctx, userID, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("get user model stats: %w", err)
	}
	return stats, nil
}

// GetModelStatsWithFiltersBySource returns model statistics using the repository's supported model dimensions.
func (s *UsageService) GetModelStatsWithFiltersBySource(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters, modelSource string) ([]ModelStat, error) {
	normalizedSource := NormalizeModelSource(modelSource)

	if filterRepo := s.readers.ModelStatsWithUsageFiltersRepo; filterRepo != nil {
		stats, err := filterRepo.GetModelStatsWithUsageFiltersBySource(ctx, startTime, endTime, filters, normalizedSource)
		if err != nil {
			return nil, fmt.Errorf("get model stats with filters by source: %w", err)
		}
		return stats, nil
	}

	if sourceRepo := s.readers.ModelStatsBySourceRepo; sourceRepo != nil {
		stats, err := sourceRepo.GetModelStatsWithFiltersBySource(ctx, startTime, endTime, filters.UserID, filters.APIKeyID, filters.ProviderID, filters.GroupID, filters.RequestType, filters.Stream, filters.BillingType, normalizedSource)
		if err != nil {
			return nil, fmt.Errorf("get model stats with filters by source: %w", err)
		}
		return stats, nil
	}
	stats, err := s.usageRepo.GetModelStatsWithFilters(ctx, startTime, endTime, filters.UserID, filters.APIKeyID, filters.ProviderID, filters.GroupID, filters.RequestType, filters.Stream, filters.BillingType)
	if err != nil {
		return nil, fmt.Errorf("get model stats with filters: %w", err)
	}
	return stats, nil
}

// GetGroupStatsWithFilters returns group statistics using the repository's supported filters.
func (s *UsageService) GetGroupStatsWithFilters(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters) ([]GroupStat, error) {
	if filterRepo := s.readers.GroupStatsWithUsageFiltersRepo; filterRepo != nil {
		stats, err := filterRepo.GetGroupStatsWithUsageFilters(ctx, startTime, endTime, filters)
		if err != nil {
			return nil, fmt.Errorf("get group stats with filters: %w", err)
		}
		return stats, nil
	}
	stats, err := s.usageRepo.GetGroupStatsWithFilters(ctx, startTime, endTime, filters.UserID, filters.APIKeyID, filters.ProviderID, filters.GroupID, filters.RequestType, filters.Stream, filters.BillingType)
	if err != nil {
		return nil, fmt.Errorf("get group stats with filters: %w", err)
	}
	return stats, nil
}

// GetAPIKeyModelStats returns an API key's model usage.
func (s *UsageService) GetAPIKeyModelStats(ctx context.Context, apiKeyID int64, startTime, endTime time.Time) ([]ModelStat, error) {
	stats, err := s.usageRepo.GetModelStatsWithFilters(ctx, startTime, endTime, 0, apiKeyID, 0, 0, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get api key model stats: %w", err)
	}
	return stats, nil
}

// GetAPIKeyDailyUsage returns daily usage filtered by user and API key.
func (s *UsageService) GetAPIKeyDailyUsage(ctx context.Context, userID, apiKeyID int64, startTime, endTime time.Time) ([]APIKeyDailyUsagePoint, error) {
	return s.getAPIKeyDailyUsage(ctx, userID, apiKeyID, startTime, endTime)
}

// GetAPIKeyDailyUsageByKey reads daily usage for gateway introspection after the caller authenticates the key.
func (s *UsageService) GetAPIKeyDailyUsageByKey(ctx context.Context, apiKeyID int64, startTime, endTime time.Time) ([]APIKeyDailyUsagePoint, error) {
	return s.getAPIKeyDailyUsage(ctx, 0, apiKeyID, startTime, endTime)
}

func (s *UsageService) getAPIKeyDailyUsage(ctx context.Context, userID, apiKeyID int64, startTime, endTime time.Time) ([]APIKeyDailyUsagePoint, error) {
	trend, err := s.usageRepo.GetUsageTrendWithFilters(ctx, startTime, endTime, "day", userID, apiKeyID, 0, 0, "", nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get api key daily usage: %w", err)
	}

	points := make([]APIKeyDailyUsagePoint, 0, len(trend))
	for _, row := range trend {
		points = append(points, APIKeyDailyUsagePoint{
			Date:             row.Date,
			Requests:         row.Requests,
			InputTokens:      row.InputTokens,
			OutputTokens:     row.OutputTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheCreationTokens,
			TotalTokens:      row.TotalTokens,
			Cost:             row.Cost,
			ActualCost:       row.ActualCost,
		})
	}
	return points, nil
}

// GetBatchAPIKeyUsageStats returns today's and range-total user charges for multiple API keys.
func (s *UsageService) GetBatchAPIKeyUsageStats(ctx context.Context, apiKeyIDs []int64, startTime, endTime time.Time) (map[int64]*BatchAPIKeyUsageStats, error) {
	stats, err := s.usageRepo.GetBatchAPIKeyUsageStats(ctx, apiKeyIDs, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("get batch api key usage stats: %w", err)
	}
	return stats, nil
}

// ListWithFilters lists usage logs matching the supplied filters.
func (s *UsageService) ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters UsageLogFilters) ([]UsageLog, *pagination.PaginationResult, error) {
	logs, result, err := s.usageRepo.ListWithFilters(ctx, params, filters)
	if err != nil {
		return nil, nil, fmt.Errorf("list usage logs with filters: %w", err)
	}
	return logs, result, nil
}

// GetGlobalStats returns global usage totals for the time range.
func (s *UsageService) GetGlobalStats(ctx context.Context, startTime, endTime time.Time) (*UsageStats, error) {
	stats, err := s.usageRepo.GetGlobalStats(ctx, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("get global usage stats: %w", err)
	}
	return stats, nil
}

// GetStatsWithFilters summarizes usage matching the supplied filters.
func (s *UsageService) GetStatsWithFilters(ctx context.Context, filters UsageLogFilters) (*UsageStats, error) {
	stats, err := s.usageRepo.GetStatsWithFilters(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("get usage stats with filters: %w", err)
	}
	return stats, nil
}

// GetUserStatsWithFilters uses a summary reader that omits upstream and path details when supported.
func (s *UsageService) GetUserStatsWithFilters(ctx context.Context, filters UsageLogFilters) (*UsageStats, error) {
	reader := s.readers.UserStatsReader
	ok := reader != nil
	if !ok {
		return s.GetStatsWithFilters(ctx, filters)
	}
	stats, err := reader.GetUserStatsWithFilters(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("get user usage stats with filters: %w", err)
	}
	return stats, nil
}
