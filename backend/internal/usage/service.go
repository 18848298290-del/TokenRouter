package usage

import (
	"context"
	"fmt"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
)

// service.go serves usage-history and reporting queries through the configured
// repository and optional readers.
// Usage records populate user history, administrator diagnostics, and chart
// summaries after gateway completion writes them. Query methods use the
// injected repository, while optional readers carry richer filter shapes and
// model dimensions for implementations that support them; access control
// remains with each HTTP or module caller.

// ErrUsageLogNotFound identifies an absent usage row for repository and HTTP
// callers, allowing the standard application-error mapper to return not found.
var ErrUsageLogNotFound = infraerrors.NotFound("USAGE_LOG_NOT_FOUND", "usage log not found")

// UsageService exposes recorded usage queries through persistence and optional
// filter readers.
type UsageService struct {
	statsQueryCache *querycache.Cache  // administrator statistics snapshot cache
	usageRepo       UsageLogRepository // recorded usage persistence dependency
	readers         QueryReaders       // optional enhanced query capabilities
}

// NewUsageService binds recorded-usage queries and creates a thirty-second
// statistics cache. It discovers optional capabilities from usageRepo; when
// optional is supplied, its first value replaces the entire discovered
// capability set.
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

// GetByID returns a usage record selected by its database row ID. A missing
// row retains ErrUsageLogNotFound in the wrapped error chain, allowing callers
// to distinguish absence before deciding how to expose request metadata.
func (s *UsageService) GetByID(ctx context.Context, id int64) (*UsageLog, error) {
	log, err := s.usageRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get usage log: %w", err)
	}
	return log, nil
}

// ListByUser returns a page of records for the supplied acting user.
func (s *UsageService) ListByUser(ctx context.Context, userID int64, params pagination.PaginationParams) ([]UsageLog, *pagination.PaginationResult, error) {
	logs, pagination, err := s.usageRepo.ListByUser(ctx, userID, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list usage logs: %w", err)
	}
	return logs, pagination, nil
}

// ListByAPIKey returns a page of records for the supplied API key.
func (s *UsageService) ListByAPIKey(ctx context.Context, apiKeyID int64, params pagination.PaginationParams) ([]UsageLog, *pagination.PaginationResult, error) {
	logs, pagination, err := s.usageRepo.ListByAPIKey(ctx, apiKeyID, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list usage logs: %w", err)
	}
	return logs, pagination, nil
}

// ListByProvider returns a page of records for the supplied execution provider.
func (s *UsageService) ListByProvider(ctx context.Context, providerID int64, params pagination.PaginationParams) ([]UsageLog, *pagination.PaginationResult, error) {
	logs, pagination, err := s.usageRepo.ListByProvider(ctx, providerID, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list usage logs: %w", err)
	}
	return logs, pagination, nil
}

// GetUsageRanking returns public billing-user ranks and totals for a half-open
// range. PostgreSQL defaults nonpositive limits to twenty and unrecognized sort
// metrics to total tokens; the HTTP caller applies configured field visibility
// to the complete returned counters.
func (s *UsageService) GetUsageRanking(ctx context.Context, startTime, endTime time.Time, limit int, sortBy UsageRankingSortBy) (*UsageRankingResponse, error) {
	ranking, err := s.usageRepo.GetUsageRanking(ctx, startTime, endTime, limit, sortBy)
	if err != nil {
		return nil, fmt.Errorf("get usage ranking: %w", err)
	}
	return ranking, nil
}

// Delete removes the selected usage row through the repository. Callers
// authorize this administrator operation and coordinate any report repair
// needed after deletion, because this method performs no aggregation or cache
// invalidation. PostgreSQL also succeeds when the selected row is already absent.
func (s *UsageService) Delete(ctx context.Context, id int64) error {
	if err := s.usageRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete usage log: %w", err)
	}
	return nil
}

// GetUserDashboardStats returns totals for the user's records and their
// currently owned team's usage. PostgreSQL includes owner rows once and bases
// duration averages on records with observed durations, keeping missing
// observations out of that denominator.
func (s *UsageService) GetUserDashboardStats(ctx context.Context, userID int64) (*UserDashboardStats, error) {
	stats, err := s.usageRepo.GetUserDashboardStats(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user dashboard stats: %w", err)
	}
	return stats, nil
}

// GetAPIKeyDashboardStats returns a key's retained totals and today's values,
// including future-dated records. PostgreSQL computes recent RPM and TPM from
// raw records and averages observed durations; callers authenticate the key
// before using this query as a gateway summary.
func (s *UsageService) GetAPIKeyDashboardStats(ctx context.Context, apiKeyID int64) (*UserDashboardStats, error) {
	stats, err := s.usageRepo.GetAPIKeyDashboardStats(ctx, apiKeyID)
	if err != nil {
		return nil, fmt.Errorf("get api key dashboard stats: %w", err)
	}
	return stats, nil
}

// GetUserUsageTrendByUserID returns bucketed usage for the user and their
// currently owned team over a half-open range. PostgreSQL uses daily formatting
// for unsupported granularity values, while ownership is resolved at query time.
func (s *UsageService) GetUserUsageTrendByUserID(ctx context.Context, userID int64, startTime, endTime time.Time, granularity string) ([]TrendDataPoint, error) {
	trend, err := s.usageRepo.GetUserUsageTrendByUserID(ctx, userID, startTime, endTime, granularity)
	if err != nil {
		return nil, fmt.Errorf("get user usage trend: %w", err)
	}
	return trend, nil
}

// GetUsageTrendWithFilters returns trends through an optional reader accepting
// the unified filter object. Its base path carries UserID, APIKeyID,
// ProviderID, GroupID, Model, RequestType, Stream, and BillingType; callers
// requiring additional selectors choose a reader with those filtering rules.
// UsageTrendWithFiltersRepo documents PostgreSQL's legacy request-ID limitation.
func (s *UsageService) GetUsageTrendWithFilters(ctx context.Context, startTime, endTime time.Time, granularity string, filters UsageLogFilters) ([]TrendDataPoint, error) {
	if filterRepo := s.readers.UsageTrendWithFiltersRepo; filterRepo != nil {
		trend, err := filterRepo.GetUsageTrendWithUsageFilters(ctx, startTime, endTime, granularity, filters)
		if err != nil {
			return nil, fmt.Errorf("get usage trend with filters: %w", err)
		}
		return trend, nil
	}
	// The base signature cannot carry team or request-ID selectors. Use the
	// selected enhanced capability above when those restrictions are required by callers.
	trend, err := s.usageRepo.GetUsageTrendWithFilters(ctx, startTime, endTime, granularity, filters.UserID, filters.APIKeyID, filters.ProviderID, filters.GroupID, filters.Model, filters.RequestType, filters.Stream, filters.BillingType)
	if err != nil {
		return nil, fmt.Errorf("get usage trend with filters: %w", err)
	}
	return trend, nil
}

// GetUserModelStats returns request-model totals for the user and their
// currently owned team over a half-open range. PostgreSQL reads the current
// ownership relation while keeping owner rows in one scope branch.
func (s *UsageService) GetUserModelStats(ctx context.Context, userID int64, startTime, endTime time.Time) ([]ModelStat, error) {
	stats, err := s.usageRepo.GetUserModelStats(ctx, userID, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("get user model stats: %w", err)
	}
	return stats, nil
}

// GetModelStatsWithFiltersBySource returns totals grouped by the normalized
// model dimension. It prefers the unified-filter reader, then the scalar source
// reader; the base path groups the request-model dimension. Scalar paths carry
// UserID, APIKeyID, ProviderID, GroupID, RequestType, Stream, and BillingType;
// model-text, team, request-ID, and other unified restrictions require the first
// reader capability.
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
	// Repositories without either source capability supply their request-model
	// totals here. Additional scope and alternate dimensions require an enhanced reader.
	stats, err := s.usageRepo.GetModelStatsWithFilters(ctx, startTime, endTime, filters.UserID, filters.APIKeyID, filters.ProviderID, filters.GroupID, filters.RequestType, filters.Stream, filters.BillingType)
	if err != nil {
		return nil, fmt.Errorf("get model stats with filters: %w", err)
	}
	return stats, nil
}

// GetGroupStatsWithFilters returns group totals through a unified-filter reader
// when one is supplied. The base path forwards UserID, APIKeyID, ProviderID,
// GroupID, RequestType, Stream, and BillingType, so callers needing additional
// restrictions supply the enhanced reader.
func (s *UsageService) GetGroupStatsWithFilters(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters) ([]GroupStat, error) {
	if filterRepo := s.readers.GroupStatsWithUsageFiltersRepo; filterRepo != nil {
		stats, err := filterRepo.GetGroupStatsWithUsageFilters(ctx, startTime, endTime, filters)
		if err != nil {
			return nil, fmt.Errorf("get group stats with filters: %w", err)
		}
		return stats, nil
	}
	// The base group query carries its scalar selectors. Team, personal, owned-team,
	// and request-ID restrictions need the unified-filter capability selected above.
	stats, err := s.usageRepo.GetGroupStatsWithFilters(ctx, startTime, endTime, filters.UserID, filters.APIKeyID, filters.ProviderID, filters.GroupID, filters.RequestType, filters.Stream, filters.BillingType)
	if err != nil {
		return nil, fmt.Errorf("get group stats with filters: %w", err)
	}
	return stats, nil
}

// GetAPIKeyModelStats returns request-model totals for an API key over a
// half-open range. Callers supply an authorized positive key ID, since zero
// leaves the key predicate unrestricted in the base repository query.
func (s *UsageService) GetAPIKeyModelStats(ctx context.Context, apiKeyID int64, startTime, endTime time.Time) ([]ModelStat, error) {
	stats, err := s.usageRepo.GetModelStatsWithFilters(ctx, startTime, endTime, 0, apiKeyID, 0, 0, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get api key model stats: %w", err)
	}
	return stats, nil
}

// GetAPIKeyDailyUsage returns daily points scoped to a user and API key.
// Callers authorize both identifiers and supply positive values; zero selectors
// leave those predicates unrestricted in the repository query.
func (s *UsageService) GetAPIKeyDailyUsage(ctx context.Context, userID, apiKeyID int64, startTime, endTime time.Time) ([]APIKeyDailyUsagePoint, error) {
	return s.getAPIKeyDailyUsage(ctx, userID, apiKeyID, startTime, endTime)
}

// GetAPIKeyDailyUsageByKey reads daily points using key scope alone for gateway
// introspection. Callers authenticate and pass a positive key ID before invoking
// it; the zero user selector leaves user filtering to that authenticated key.
func (s *UsageService) GetAPIKeyDailyUsageByKey(ctx context.Context, apiKeyID int64, startTime, endTime time.Time) ([]APIKeyDailyUsagePoint, error) {
	return s.getAPIKeyDailyUsage(ctx, 0, apiKeyID, startTime, endTime)
}

// getAPIKeyDailyUsage queries daily trend rows and converts them to key-report
// points. Callers supply a positive key ID; a zero user selector leaves scope
// with that key. The response's CacheWriteTokens receives the trend's cache
// creation total.
func (s *UsageService) getAPIKeyDailyUsage(ctx context.Context, userID, apiKeyID int64, startTime, endTime time.Time) ([]APIKeyDailyUsagePoint, error) {
	trend, err := s.usageRepo.GetUsageTrendWithFilters(ctx, startTime, endTime, "day", userID, apiKeyID, 0, 0, "", nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("get api key daily usage: %w", err)
	}

	// The key response names cache creation cache_write_tokens. Copy that total
	// alongside the trend's TotalTokens so both fields describe the same daily usage.
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

// GetBatchAPIKeyUsageStats returns today's and range-total charges for positive
// IDs in the supplied key list. PostgreSQL supplies zero results for unused
// keys, defaults zero bounds to the preceding thirty days through now, and
// computes today from server midnight independently.
func (s *UsageService) GetBatchAPIKeyUsageStats(ctx context.Context, apiKeyIDs []int64, startTime, endTime time.Time) (map[int64]*BatchAPIKeyUsageStats, error) {
	stats, err := s.usageRepo.GetBatchAPIKeyUsageStats(ctx, apiKeyIDs, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("get batch api key usage stats: %w", err)
	}
	return stats, nil
}

// ListWithFilters returns matching rows with repository pagination metadata.
// Set ExactTotal when an exact row count is needed; eligible broad PostgreSQL
// queries otherwise report a total sufficient to indicate a following page.
func (s *UsageService) ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters UsageLogFilters) ([]UsageLog, *pagination.PaginationResult, error) {
	logs, result, err := s.usageRepo.ListWithFilters(ctx, params, filters)
	if err != nil {
		return nil, nil, fmt.Errorf("list usage logs with filters: %w", err)
	}
	return logs, result, nil
}

// GetGlobalStats returns recorded totals over the supplied global half-open
// range. PostgreSQL excludes missing durations from the average and returns
// zero-valued statistics when no record matches, while user charges remain
// separate from standard recorded costs.
func (s *UsageService) GetGlobalStats(ctx context.Context, startTime, endTime time.Time) (*UsageStats, error) {
	stats, err := s.usageRepo.GetGlobalStats(ctx, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("get global usage stats: %w", err)
	}
	return stats, nil
}

// GetStatsWithFilters returns matching recorded totals and selected endpoint
// charts. PostgreSQL summary failures fail the request; individual endpoint
// failures can yield empty charts with successful totals, so chart absence does
// not establish zero traffic.
func (s *UsageService) GetStatsWithFilters(ctx context.Context, filters UsageLogFilters) (*UsageStats, error) {
	stats, err := s.usageRepo.GetStatsWithFilters(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("get usage stats with filters: %w", err)
	}
	return stats, nil
}

// GetUserStatsWithFilters uses the reduced user-summary reader when one is
// available. Otherwise it delegates to GetStatsWithFilters, whose result can
// include upstream details that the user HTTP caller removes before responding.
func (s *UsageService) GetUserStatsWithFilters(ctx context.Context, filters UsageLogFilters) (*UsageStats, error) {
	reader := s.readers.UserStatsReader
	ok := reader != nil
	if !ok {
		// A missing reduced reader still permits a summary through the general
		// repository path; user HTTP responses remove its provider and upstream fields.
		return s.GetStatsWithFilters(ctx, filters)
	}
	stats, err := reader.GetUserStatsWithFilters(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("get user usage stats with filters: %w", err)
	}
	return stats, nil
}
