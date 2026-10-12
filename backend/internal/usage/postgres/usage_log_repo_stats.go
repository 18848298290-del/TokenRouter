package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lib/pq"
	"golang.org/x/sync/errgroup"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// UsageStats summarizes usage and cost.
type UsageStats = usage.UsageStats

// BatchUserUsageStats contains one user's entry in a batch query.
type BatchUserUsageStats = usage.BatchUserUsageStats

// PlatformUsage contains usage attributed to one platform.
type PlatformUsage = usage.PlatformUsage

// BatchAPIKeyUsageStats contains one API key's entry in a batch query.
type BatchAPIKeyUsageStats = usage.BatchAPIKeyUsageStats

// ProviderUsageHistory contains a provider's daily usage history.
type ProviderUsageHistory = usage.ProviderUsageHistory

// ProviderUsageSummary summarizes a provider's usage.
type ProviderUsageSummary = usage.ProviderUsageSummary

// ProviderUsageStatsResponse contains a provider's summary and usage breakdowns.
type ProviderUsageStatsResponse = usage.ProviderUsageStatsResponse

// EndpointStat contains one endpoint usage row.
type EndpointStat = usage.EndpointStat

// GetProviderWindowStatsPair scans the union of both windows and aggregates each start separately.
func (r *Store) GetProviderWindowStatsPair(ctx context.Context, id int64, firstStart, secondStart time.Time) (*usage.ProviderStats, *usage.ProviderStats, error) {
	const query = `SELECT
	 COUNT(*) FILTER (WHERE created_at >= $2),
	 COALESCE(SUM(input_tokens+output_tokens+cache_creation_tokens+cache_read_tokens) FILTER (WHERE created_at >= $2),0),
	 COALESCE(SUM(COALESCE(provider_stats_cost,total_cost)*COALESCE(provider_rate_multiplier,1)) FILTER (WHERE created_at >= $2),0),
	 COALESCE(SUM(total_cost) FILTER (WHERE created_at >= $2),0),
	 COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $2),0),
	 COUNT(*) FILTER (WHERE created_at >= $3),
	 COALESCE(SUM(input_tokens+output_tokens+cache_creation_tokens+cache_read_tokens) FILTER (WHERE created_at >= $3),0),
	 COALESCE(SUM(COALESCE(provider_stats_cost,total_cost)*COALESCE(provider_rate_multiplier,1)) FILTER (WHERE created_at >= $3),0),
	 COALESCE(SUM(total_cost) FILTER (WHERE created_at >= $3),0),
	 COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $3),0)
	 FROM usage_logs WHERE provider_id=$1 AND created_at >= LEAST($2,$3)`
	a, b := &usage.ProviderStats{}, &usage.ProviderStats{}
	if err := scanSingleRow(ctx, r.sql, query, []any{id, firstStart, secondStart},
		&a.Requests, &a.Tokens, &a.Cost, &a.StandardCost, &a.UserCost,
		&b.Requests, &b.Tokens, &b.Cost, &b.StandardCost, &b.UserCost); err != nil {
		return nil, nil, err
	}
	return a, b, nil
}

// GetUserStatsAggregated computes a user's usage summary in SQL.
func (r *Store) GetUserStatsAggregated(ctx context.Context, userID int64, startTime, endTime time.Time) (*usage.UsageStats, error) {
	query := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(COALESCE(duration_ms, 0)), 0) as avg_duration_ms
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
	`

	var stats usage.UsageStats
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{userID, startTime, endTime},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.AverageDurationMs,
	); err != nil {
		return nil, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheTokens
	return &stats, nil
}

// GetAPIKeyStatsAggregated computes an API key's usage summary in SQL.
func (r *Store) GetAPIKeyStatsAggregated(ctx context.Context, apiKeyID int64, startTime, endTime time.Time) (*usage.UsageStats, error) {
	query := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(COALESCE(duration_ms, 0)), 0) as avg_duration_ms
		FROM usage_logs
		WHERE api_key_id = $1 AND created_at >= $2 AND created_at < $3
	`

	var stats usage.UsageStats
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{apiKeyID, startTime, endTime},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.AverageDurationMs,
	); err != nil {
		return nil, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheTokens
	return &stats, nil
}

// GetProviderStatsAggregated computes provider usage with SQL aggregates and returns one summary row.
func (r *Store) GetProviderStatsAggregated(ctx context.Context, providerID int64, startTime, endTime time.Time) (*usage.UsageStats, error) {
	query := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(COALESCE(duration_ms, 0)), 0) as avg_duration_ms
		FROM usage_logs
		WHERE provider_id = $1 AND created_at >= $2 AND created_at < $3
	`

	var stats usage.UsageStats
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{providerID, startTime, endTime},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.AverageDurationMs,
	); err != nil {
		return nil, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheTokens
	return &stats, nil
}

// GetModelStatsAggregated computes usage totals for the stored model with SQL aggregates.
func (r *Store) GetModelStatsAggregated(ctx context.Context, modelName string, startTime, endTime time.Time) (*usage.UsageStats, error) {
	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(COALESCE(duration_ms, 0)), 0) as avg_duration_ms
		FROM usage_logs
		WHERE %s = $1 AND created_at >= $2 AND created_at < $3
	`, rawUsageLogModelColumn)

	var stats usage.UsageStats
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{modelName, startTime, endTime},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.AverageDurationMs,
	); err != nil {
		return nil, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheTokens
	return &stats, nil
}

// GetDailyStatsAggregated groups a user's usage by day in the database.
func (r *Store) GetDailyStatsAggregated(ctx context.Context, userID int64, startTime, endTime time.Time) (result []map[string]any, err error) {
	tzName := r.resolveUsageStatsTimezone()
	query := `
		SELECT
			-- 使用应用时区分组，避免数据库会话时区导致日边界偏移。
			TO_CHAR(created_at AT TIME ZONE $4, 'YYYY-MM-DD') as date,
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(COALESCE(duration_ms, 0)), 0) as avg_duration_ms
		FROM usage_logs
		WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
		GROUP BY 1
		ORDER BY 1
	`

	rows, err := r.sql.QueryContext(ctx, query, userID, startTime, endTime, tzName)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			result = nil
		}
	}()

	result = make([]map[string]any, 0)
	for rows.Next() {
		var (
			date              string
			totalRequests     int64
			totalInputTokens  int64
			totalOutputTokens int64
			totalCacheTokens  int64
			totalCost         float64
			totalActualCost   float64
			avgDurationMs     float64
		)
		if err = rows.Scan(
			&date,
			&totalRequests,
			&totalInputTokens,
			&totalOutputTokens,
			&totalCacheTokens,
			&totalCost,
			&totalActualCost,
			&avgDurationMs,
		); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{
			"date":                date,
			"total_requests":      totalRequests,
			"total_input_tokens":  totalInputTokens,
			"total_output_tokens": totalOutputTokens,
			"total_cache_tokens":  totalCacheTokens,
			"total_tokens":        totalInputTokens + totalOutputTokens + totalCacheTokens,
			"total_cost":          totalCost,
			"total_actual_cost":   totalActualCost,
			"average_duration_ms": avgDurationMs,
		})
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// resolveUsageStatsTimezone returns the timezone name used for SQL grouping.
// It prefers the injected calendar, then TZ, then UTC.
func (r *Store) resolveUsageStatsTimezone() string {
	tzName := r.calendar.Location().String()
	if tzName != "" && tzName != "Local" {
		return tzName
	}
	if envTZ := strings.TrimSpace(os.Getenv("TZ")); envTZ != "" {
		return envTZ
	}
	return "UTC"
}

// GetProviderTodayStats aggregates provider usage since local midnight.
func (r *Store) GetProviderTodayStats(ctx context.Context, providerID int64) (*usage.ProviderStats, error) {
	today := r.calendar.Today()

	query := `
		SELECT
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as tokens,
			COALESCE(SUM(COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1)), 0) as cost,
			COALESCE(SUM(total_cost), 0) as standard_cost,
			COALESCE(SUM(actual_cost), 0) as user_cost
		FROM usage_logs
		WHERE provider_id = $1 AND created_at >= $2
	`

	stats := &usage.ProviderStats{}
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{providerID, today},
		&stats.Requests,
		&stats.Tokens,
		&stats.Cost,
		&stats.StandardCost,
		&stats.UserCost,
	); err != nil {
		return nil, err
	}
	return stats, nil
}

// GetProviderWindowStats aggregates provider usage at or after startTime, including future-dated records.
func (r *Store) GetProviderWindowStats(ctx context.Context, providerID int64, startTime time.Time) (*usage.ProviderStats, error) {
	query := `
		SELECT
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as tokens,
			COALESCE(SUM(COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1)), 0) as cost,
			COALESCE(SUM(total_cost), 0) as standard_cost,
			COALESCE(SUM(actual_cost), 0) as user_cost
		FROM usage_logs
		WHERE provider_id = $1 AND created_at >= $2
	`

	stats := &usage.ProviderStats{}
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{providerID, startTime},
		&stats.Requests,
		&stats.Tokens,
		&stats.Cost,
		&stats.StandardCost,
		&stats.UserCost,
	); err != nil {
		return nil, err
	}
	return stats, nil
}

// GetProviderWindowStatsBatch aggregates multiple providers from a shared start time.
// Requested providers with no matching records receive zero-valued statistics.
func (r *Store) GetProviderWindowStatsBatch(ctx context.Context, providerIDs []int64, startTime time.Time) (map[int64]*usage.ProviderStats, error) {
	result := make(map[int64]*usage.ProviderStats, len(providerIDs))
	if len(providerIDs) == 0 {
		return result, nil
	}

	query := `
		SELECT
			provider_id,
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as tokens,
			COALESCE(SUM(COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1)), 0) as cost,
			COALESCE(SUM(total_cost), 0) as standard_cost,
			COALESCE(SUM(actual_cost), 0) as user_cost
		FROM usage_logs
		WHERE provider_id = ANY($1) AND created_at >= $2
		GROUP BY provider_id
	`
	rows, err := r.sql.QueryContext(ctx, query, pq.Array(providerIDs), startTime)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var providerID int64
		stats := &usage.ProviderStats{}
		if err := rows.Scan(
			&providerID,
			&stats.Requests,
			&stats.Tokens,
			&stats.Cost,
			&stats.StandardCost,
			&stats.UserCost,
		); err != nil {
			return nil, err
		}
		result[providerID] = stats
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, providerID := range providerIDs {
		if _, ok := result[providerID]; !ok {
			result[providerID] = &usage.ProviderStats{}
		}
	}
	return result, nil
}

// GetGeminiUsageTotalsBatch aggregates Pro and Flash usage for each provider in the window.
// Models containing flash or lite, ignoring case, count as Flash; other models count as Pro.
func (r *Store) GetGeminiUsageTotalsBatch(ctx context.Context, providerIDs []int64, startTime, endTime time.Time) (map[int64]provider.GeminiUsageTotals, error) {
	result := make(map[int64]provider.GeminiUsageTotals, len(providerIDs))
	if len(providerIDs) == 0 {
		return result, nil
	}

	query := `
		SELECT
			provider_id,
			COALESCE(SUM(CASE WHEN LOWER(COALESCE(model, '')) LIKE '%flash%' OR LOWER(COALESCE(model, '')) LIKE '%lite%' THEN 1 ELSE 0 END), 0) AS flash_requests,
			COALESCE(SUM(CASE WHEN LOWER(COALESCE(model, '')) LIKE '%flash%' OR LOWER(COALESCE(model, '')) LIKE '%lite%' THEN 0 ELSE 1 END), 0) AS pro_requests,
			COALESCE(SUM(CASE WHEN LOWER(COALESCE(model, '')) LIKE '%flash%' OR LOWER(COALESCE(model, '')) LIKE '%lite%' THEN (input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) ELSE 0 END), 0) AS flash_tokens,
			COALESCE(SUM(CASE WHEN LOWER(COALESCE(model, '')) LIKE '%flash%' OR LOWER(COALESCE(model, '')) LIKE '%lite%' THEN 0 ELSE (input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) END), 0) AS pro_tokens,
			COALESCE(SUM(CASE WHEN LOWER(COALESCE(model, '')) LIKE '%flash%' OR LOWER(COALESCE(model, '')) LIKE '%lite%' THEN COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1) ELSE 0 END), 0) AS flash_cost,
			COALESCE(SUM(CASE WHEN LOWER(COALESCE(model, '')) LIKE '%flash%' OR LOWER(COALESCE(model, '')) LIKE '%lite%' THEN 0 ELSE COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1) END), 0) AS pro_cost
		FROM usage_logs
		WHERE provider_id = ANY($1) AND created_at >= $2 AND created_at < $3
		GROUP BY provider_id
	`
	rows, err := r.sql.QueryContext(ctx, query, pq.Array(providerIDs), startTime, endTime)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var providerID int64
		var totals provider.GeminiUsageTotals
		if err := rows.Scan(
			&providerID,
			&totals.FlashRequests,
			&totals.ProRequests,
			&totals.FlashTokens,
			&totals.ProTokens,
			&totals.FlashCost,
			&totals.ProCost,
		); err != nil {
			return nil, err
		}
		result[providerID] = totals
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, providerID := range providerIDs {
		if _, ok := result[providerID]; !ok {
			result[providerID] = provider.GeminiUsageTotals{}
		}
	}
	return result, nil
}

func normalizePositiveInt64IDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// GetBatchUserUsageStats gets today and total actual_cost for multiple users within a time range.
// If startTime is zero, defaults to 30 days ago.
func (r *Store) GetBatchUserUsageStats(ctx context.Context, userIDs []int64, startTime, endTime time.Time) (map[int64]*BatchUserUsageStats, error) {
	result := make(map[int64]*BatchUserUsageStats)
	normalizedUserIDs := normalizePositiveInt64IDs(userIDs)
	if len(normalizedUserIDs) == 0 {
		return result, nil
	}

	// A zero start time selects the last 30 days.
	if startTime.IsZero() {
		startTime = time.Now().AddDate(0, 0, -30)
	}
	if endTime.IsZero() {
		endTime = time.Now()
	}
	if stats, ok, err := r.getBatchUserUsageStatsFromAnalytics(ctx, normalizedUserIDs, startTime, endTime); err == nil && ok {
		return stats, nil
	} else if err != nil {
		r.logUsageAnalyticsFallback("batch_user_usage", err)
	}

	for _, id := range normalizedUserIDs {
		result[id] = &BatchUserUsageStats{UserID: id}
	}

	// Grouping by user and stored platform produces totals and platform breakdowns in one query.
	// Sum each user's rows and collect nonempty platforms in ByPlatform.
	query := `
		SELECT
			ul.user_id,
			` + usageLogEffectivePlatformExpr + ` as platform,
			COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $2 AND ul.created_at < $3), 0) as total_cost,
			COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $4), 0) as today_cost
		FROM usage_logs ul
		LEFT JOIN groups g ON g.id = ul.group_id
		LEFT JOIN providers a ON a.id = ul.provider_id
		WHERE ul.user_id = ANY($1)
		  AND ul.created_at >= LEAST($2, $4)
		  AND ` + usageLogSuccessFilterUL + `
		GROUP BY ul.user_id, ` + usageLogEffectivePlatformExpr + `
	`
	today := r.calendar.Today()
	rows, err := r.sql.QueryContext(ctx, query, pq.Array(normalizedUserIDs), startTime, endTime, today)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var userID int64
		var platform sql.NullString
		var total float64
		var todayTotal float64
		if err := rows.Scan(&userID, &platform, &total, &todayTotal); err != nil {
			_ = rows.Close()
			return nil, err
		}
		stats, ok := result[userID]
		if !ok {
			continue
		}
		stats.TotalActualCost += total
		stats.TodayActualCost += todayTotal
		if platform.Valid && platform.String != "" {
			stats.ByPlatform = append(stats.ByPlatform, PlatformUsage{
				Platform:        platform.String,
				TotalActualCost: total,
				TodayActualCost: todayTotal,
			})
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// GetBatchAPIKeyUsageStats gets today and total actual_cost for multiple API keys within a time range.
// If startTime is zero, defaults to 30 days ago.
func (r *Store) GetBatchAPIKeyUsageStats(ctx context.Context, apiKeyIDs []int64, startTime, endTime time.Time) (map[int64]*BatchAPIKeyUsageStats, error) {
	result := make(map[int64]*BatchAPIKeyUsageStats)
	normalizedAPIKeyIDs := normalizePositiveInt64IDs(apiKeyIDs)
	if len(normalizedAPIKeyIDs) == 0 {
		return result, nil
	}

	// A zero start time selects the last 30 days.
	if startTime.IsZero() {
		startTime = time.Now().AddDate(0, 0, -30)
	}
	if endTime.IsZero() {
		endTime = time.Now()
	}
	if stats, ok, err := r.getBatchAPIKeyUsageStatsFromAnalytics(ctx, normalizedAPIKeyIDs, startTime, endTime); err == nil && ok {
		return stats, nil
	} else if err != nil {
		r.logUsageAnalyticsFallback("batch_api_key_usage", err)
	}

	for _, id := range normalizedAPIKeyIDs {
		result[id] = &BatchAPIKeyUsageStats{APIKeyID: id}
	}

	query := `
		SELECT
			api_key_id,
			COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $2 AND created_at < $3), 0) as total_cost,
			COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $4), 0) as today_cost
		FROM usage_logs
		WHERE api_key_id = ANY($1)
		  AND created_at >= LEAST($2, $4)
		GROUP BY api_key_id
	`
	today := r.calendar.Today()
	rows, err := r.sql.QueryContext(ctx, query, pq.Array(normalizedAPIKeyIDs), startTime, endTime, today)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var apiKeyID int64
		var total float64
		var todayTotal float64
		if err := rows.Scan(&apiKeyID, &total, &todayTotal); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if stats, ok := result[apiKeyID]; ok {
			stats.TotalActualCost = total
			stats.TodayActualCost = todayTotal
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// resolveEndpointColumn maps endpoint type to the corresponding DB column name.
func resolveEndpointColumn(endpointType string) string {
	switch endpointType {
	case "upstream":
		return "ul.upstream_endpoint"
	case "path":
		return "ul.inbound_endpoint || ' -> ' || ul.upstream_endpoint"
	default:
		return "ul.inbound_endpoint"
	}
}

// GetGlobalStats gets usage statistics for all users within a time range.
func (r *Store) GetGlobalStats(ctx context.Context, startTime, endTime time.Time) (*UsageStats, error) {
	query := `
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(AVG(duration_ms), 0) as avg_duration_ms
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
	`

	stats := &UsageStats{}
	if err := scanSingleRow(
		ctx,
		r.sql,
		query,
		[]any{startTime, endTime},
		&stats.TotalRequests,
		&stats.TotalInputTokens,
		&stats.TotalOutputTokens,
		&stats.TotalCacheTokens,
		&stats.TotalCacheCreationTokens,
		&stats.TotalCacheReadTokens,
		&stats.TotalCost,
		&stats.TotalActualCost,
		&stats.AverageDurationMs,
	); err != nil {
		return nil, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheTokens
	return stats, nil
}

// GetStatsWithFilters summarizes usage matching the supplied filters.
func (r *Store) GetStatsWithFilters(ctx context.Context, filters UsageLogFilters) (*UsageStats, error) {
	return r.getStatsWithFilters(ctx, filters, true)
}

// GetUserStatsWithFilters omits upstream endpoint and path queries from the user summary.
func (r *Store) GetUserStatsWithFilters(ctx context.Context, filters UsageLogFilters) (*UsageStats, error) {
	return r.getStatsWithFilters(ctx, filters, false)
}

func (r *Store) getStatsWithFilters(ctx context.Context, filters UsageLogFilters, includeRawEndpointDetails bool) (*UsageStats, error) {
	conditions := make([]string, 0, 9)
	args := make([]any, 0, 9)

	source, scopeCondition, args, scopeErr := r.buildUsageLogScopeSource(ctx, args, filters.UserID, filters.IncludeOwnedTeam, "")
	if scopeErr != nil {
		return nil, scopeErr
	}
	if scopeCondition != "" {
		conditions = append(conditions, scopeCondition)
	}
	if filters.APIKeyID > 0 {
		conditions = append(conditions, fmt.Sprintf("api_key_id = $%d", len(args)+1))
		args = append(args, filters.APIKeyID)
	}
	if filters.ProviderID > 0 {
		conditions = append(conditions, fmt.Sprintf("provider_id = $%d", len(args)+1))
		args = append(args, filters.ProviderID)
	}
	if filters.GroupID > 0 {
		conditions = append(conditions, fmt.Sprintf("group_id = $%d", len(args)+1))
		args = append(args, filters.GroupID)
	}
	if filters.TeamID > 0 {
		conditions = append(conditions, fmt.Sprintf("team_id = $%d", len(args)+1))
		args = append(args, filters.TeamID)
	}
	if filters.PersonalOnly {
		conditions = append(conditions, "team_id IS NULL")
	}
	if id := strings.TrimSpace(filters.RequestID); id != "" {
		conditions = append(conditions, usageRequestIDCondition("", len(args)+1))
		args = append(args, id)
	}
	conditions, args = appendUsageLogModelWhereCondition(conditions, args, filters.Model, filters.ModelFilterSource)
	conditions, args = appendRequestTypeOrStreamWhereCondition(conditions, args, filters.RequestType, filters.Stream)
	conditions, args = appendNativeCompactionV2WhereCondition(conditions, args, filters.NativeCompactionV2, "")
	if filters.BillingType != nil {
		conditions = append(conditions, fmt.Sprintf("billing_type = $%d", len(args)+1))
		args = append(args, int16(*filters.BillingType))
	}
	conditions, args = appendUsageLogBillingModeWhereCondition(conditions, args, filters.BillingMode)
	if filters.StartTime != nil {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", len(args)+1))
		args = append(args, *filters.StartTime)
	}
	if filters.EndTime != nil {
		conditions = append(conditions, fmt.Sprintf("created_at < $%d", len(args)+1))
		args = append(args, *filters.EndTime)
	}

	query := fmt.Sprintf(`
		SELECT
			COUNT(*) as total_requests,
			COALESCE(SUM(input_tokens), 0) as total_input_tokens,
			COALESCE(SUM(output_tokens), 0) as total_output_tokens,
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0) as total_cache_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as total_cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as total_cache_read_tokens,
			COALESCE(SUM(total_cost), 0) as total_cost,
			COALESCE(SUM(actual_cost), 0) as total_actual_cost,
			COALESCE(SUM(COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1)), 0) as total_provider_cost,
			COALESCE(AVG(duration_ms), 0) as avg_duration_ms
		FROM %s
		%s
	`, source, buildWhere(conditions))

	stats := &UsageStats{}
	var totalProviderCost float64
	if aggregated, ok, aggregateErr := r.getUsageStatsFromAnalytics(ctx, filters); aggregateErr == nil && ok {
		stats = aggregated
		if aggregated.TotalProviderCost != nil {
			totalProviderCost = *aggregated.TotalProviderCost
		}
	} else if aggregateErr != nil {
		r.logUsageAnalyticsFallback("usage_stats", aggregateErr)
	}

	start := time.Unix(0, 0).UTC()
	if filters.StartTime != nil {
		start = *filters.StartTime
	}
	end := time.Now().UTC()
	if filters.EndTime != nil {
		end = *filters.EndTime
	}

	var endpoints, upstreamEndpoints, endpointPaths []EndpointStat

	// A failed summary query fails the request.
	runSummary := func(c context.Context) error {
		if stats.TotalProviderCost != nil {
			return nil
		}
		return scanSingleRow(
			c, r.sql, query, args,
			&stats.TotalRequests,
			&stats.TotalInputTokens,
			&stats.TotalOutputTokens,
			&stats.TotalCacheTokens,
			&stats.TotalCacheCreationTokens,
			&stats.TotalCacheReadTokens,
			&stats.TotalCost,
			&stats.TotalActualCost,
			&totalProviderCost,
			&stats.AverageDurationMs,
		)
	}
	// Endpoint failures return empty details; non-cancellation errors are logged.
	runEndpoints := func(c context.Context) {
		if filters.EndpointSource != "" && filters.EndpointSource != "inbound" {
			return
		}
		res, err := r.getEndpointStatsByColumnWithFilters(c, "inbound_endpoint", start, end, filters.UserID, filters.APIKeyID, filters.ProviderID, filters.GroupID, filters.TeamID, filters.Model, filters.ModelFilterSource, filters.RequestType, filters.Stream, filters.BillingType, filters.BillingMode, filters.PersonalOnly, filters.IncludeOwnedTeam, filters.NativeCompactionV2, filters.RequestID)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				logger.LegacyPrintf("repository.usage_log", "GetEndpointStatsWithFilters failed in GetStatsWithFilters: %v", err)
			}
			res = []EndpointStat{}
		}
		endpoints = res
	}
	runUpstream := func(c context.Context) {
		if filters.EndpointSource != "" && filters.EndpointSource != "upstream" {
			return
		}
		res, err := r.getEndpointStatsByColumnWithFilters(c, "upstream_endpoint", start, end, filters.UserID, filters.APIKeyID, filters.ProviderID, filters.GroupID, filters.TeamID, filters.Model, filters.ModelFilterSource, filters.RequestType, filters.Stream, filters.BillingType, filters.BillingMode, filters.PersonalOnly, filters.IncludeOwnedTeam, filters.NativeCompactionV2, filters.RequestID)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				logger.LegacyPrintf("repository.usage_log", "GetUpstreamEndpointStatsWithFilters failed in GetStatsWithFilters: %v", err)
			}
			res = []EndpointStat{}
		}
		upstreamEndpoints = res
	}
	runPaths := func(c context.Context) {
		if filters.EndpointSource != "" && filters.EndpointSource != "path" {
			return
		}
		res, err := r.getEndpointPathStatsWithFilters(c, start, end, filters.UserID, filters.APIKeyID, filters.ProviderID, filters.GroupID, filters.TeamID, filters.Model, filters.ModelFilterSource, filters.RequestType, filters.Stream, filters.BillingType, filters.BillingMode, filters.PersonalOnly, filters.IncludeOwnedTeam, filters.NativeCompactionV2, filters.RequestID)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				logger.LegacyPrintf("repository.usage_log", "getEndpointPathStatsWithFilters failed in GetStatsWithFilters: %v", err)
			}
			res = []EndpointStat{}
		}
		endpointPaths = res
	}

	if r.db != nil {
		// Pool-backed queries can run concurrently because each can acquire its own connection.
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() error { return runSummary(gctx) })
		g.Go(func() error { runEndpoints(gctx); return nil })
		if includeRawEndpointDetails {
			g.Go(func() error { runUpstream(gctx); return nil })
			g.Go(func() error { runPaths(gctx); return nil })
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
	} else {
		// Run Ent transaction queries sequentially on their shared connection.
		if err := runSummary(ctx); err != nil {
			return nil, err
		}
		runEndpoints(ctx)
		if includeRawEndpointDetails {
			runUpstream(ctx)
			runPaths(ctx)
		}
	}

	stats.TotalProviderCost = &totalProviderCost
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheTokens
	stats.Endpoints = endpoints
	stats.UpstreamEndpoints = upstreamEndpoints
	stats.EndpointPaths = endpointPaths

	return stats, nil
}

func (r *Store) getEndpointStatsByColumnWithFilters(ctx context.Context, endpointColumn string, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID, teamID int64, model string, modelSource string, requestType *int16, stream *bool, billingType *int8, billingMode string, personalOnly bool, includeOwnedTeam bool, nativeCompactionV2 *bool, requestIDs ...string) (results []EndpointStat, err error) {
	requestID := ""
	if len(requestIDs) > 0 {
		requestID = strings.TrimSpace(requestIDs[0])
	}

	if endpointColumn == "inbound_endpoint" {
		analyticsFilters := UsageLogFilters{
			RequestID: requestID,
			UserID:    userID, APIKeyID: apiKeyID, ProviderID: providerID, GroupID: groupID,
			TeamID: teamID, Model: model, ModelFilterSource: modelSource,
			RequestType: requestType, Stream: stream, BillingType: billingType,
			BillingMode: billingMode, PersonalOnly: personalOnly, IncludeOwnedTeam: includeOwnedTeam,
			NativeCompactionV2: nativeCompactionV2,
		}
		if aggregated, ok, aggregateErr := r.getInboundEndpointStatsFromAnalytics(ctx, startTime, endTime, analyticsFilters); aggregateErr == nil && ok {
			return aggregated, nil
		} else if aggregateErr != nil {
			r.logUsageAnalyticsFallback("inbound_endpoint_stats", aggregateErr)
		}
	}
	// Actual cost means user charges even when filtering by provider.
	args := []any{startTime, endTime}
	source, scopeCondition, args, scopeErr := r.buildUsageLogScopeSource(ctx, args, userID, includeOwnedTeam, "")
	if scopeErr != nil {
		return nil, scopeErr
	}
	query := fmt.Sprintf(`
		SELECT
			COALESCE(NULLIF(TRIM(%s), ''), 'unknown') AS endpoint,
			COUNT(*) AS requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS total_tokens,
			COALESCE(SUM(total_cost), 0) as cost,
			COALESCE(SUM(actual_cost), 0) as actual_cost
		FROM %s
		WHERE created_at >= $1 AND created_at < $2
	`, endpointColumn, source)

	if requestID != "" {
		query += " AND " + usageRequestIDCondition("", len(args)+1)
		args = append(args, requestID)
	}

	if scopeCondition != "" {
		query += " AND " + scopeCondition
	}
	if apiKeyID > 0 {
		query += fmt.Sprintf(" AND api_key_id = $%d", len(args)+1)
		args = append(args, apiKeyID)
	}
	if providerID > 0 {
		query += fmt.Sprintf(" AND provider_id = $%d", len(args)+1)
		args = append(args, providerID)
	}
	if groupID > 0 {
		query += fmt.Sprintf(" AND group_id = $%d", len(args)+1)
		args = append(args, groupID)
	}
	if teamID > 0 {
		query += fmt.Sprintf(" AND team_id = $%d", len(args)+1)
		args = append(args, teamID)
	}
	if personalOnly {
		query += " AND team_id IS NULL"
	}
	query, args = appendUsageLogModelQueryFilter(query, args, model, modelSource)
	query, args = appendRequestTypeOrStreamQueryFilter(query, args, requestType, stream)
	query, args = appendNativeCompactionV2QueryFilter(query, args, nativeCompactionV2, "")
	if billingType != nil {
		query += fmt.Sprintf(" AND billing_type = $%d", len(args)+1)
		args = append(args, int16(*billingType))
	}
	query, args = appendUsageLogBillingModeQueryFilter(query, args, billingMode, "")
	query += " GROUP BY endpoint ORDER BY requests DESC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]EndpointStat, 0)
	for rows.Next() {
		var row EndpointStat
		if err := rows.Scan(&row.Endpoint, &row.Requests, &row.TotalTokens, &row.Cost, &row.ActualCost); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (r *Store) getEndpointPathStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID, teamID int64, model string, modelSource string, requestType *int16, stream *bool, billingType *int8, billingMode string, personalOnly bool, includeOwnedTeam bool, nativeCompactionV2 *bool, requestIDs ...string) (results []EndpointStat, err error) {
	requestID := ""
	if len(requestIDs) > 0 {
		requestID = strings.TrimSpace(requestIDs[0])
	}

	// Path statistics use the same user-charge measure as individual endpoints.
	args := []any{startTime, endTime}
	source, scopeCondition, args, scopeErr := r.buildUsageLogScopeSource(ctx, args, userID, includeOwnedTeam, "")
	if scopeErr != nil {
		return nil, scopeErr
	}
	query := fmt.Sprintf(`
		SELECT
			CONCAT(
				COALESCE(NULLIF(TRIM(inbound_endpoint), ''), 'unknown'),
				' -> ',
				COALESCE(NULLIF(TRIM(upstream_endpoint), ''), 'unknown')
			) AS endpoint,
			COUNT(*) AS requests,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS total_tokens,
			COALESCE(SUM(total_cost), 0) as cost,
			COALESCE(SUM(actual_cost), 0) as actual_cost
		FROM %s
		WHERE created_at >= $1 AND created_at < $2
	`, source)

	if requestID != "" {
		query += " AND " + usageRequestIDCondition("", len(args)+1)
		args = append(args, requestID)
	}

	if scopeCondition != "" {
		query += " AND " + scopeCondition
	}
	if apiKeyID > 0 {
		query += fmt.Sprintf(" AND api_key_id = $%d", len(args)+1)
		args = append(args, apiKeyID)
	}
	if providerID > 0 {
		query += fmt.Sprintf(" AND provider_id = $%d", len(args)+1)
		args = append(args, providerID)
	}
	if groupID > 0 {
		query += fmt.Sprintf(" AND group_id = $%d", len(args)+1)
		args = append(args, groupID)
	}
	if teamID > 0 {
		query += fmt.Sprintf(" AND team_id = $%d", len(args)+1)
		args = append(args, teamID)
	}
	if personalOnly {
		query += " AND team_id IS NULL"
	}
	query, args = appendUsageLogModelQueryFilter(query, args, model, modelSource)
	query, args = appendRequestTypeOrStreamQueryFilter(query, args, requestType, stream)
	query, args = appendNativeCompactionV2QueryFilter(query, args, nativeCompactionV2, "")
	if billingType != nil {
		query += fmt.Sprintf(" AND billing_type = $%d", len(args)+1)
		args = append(args, int16(*billingType))
	}
	query, args = appendUsageLogBillingModeQueryFilter(query, args, billingMode, "")
	query += " GROUP BY endpoint ORDER BY requests DESC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]EndpointStat, 0)
	for rows.Next() {
		var row EndpointStat
		if err := rows.Scan(&row.Endpoint, &row.Requests, &row.TotalTokens, &row.Cost, &row.ActualCost); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// GetEndpointStatsWithFilters returns inbound endpoint statistics with optional filters.
func (r *Store) GetEndpointStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) ([]EndpointStat, error) {
	return r.getEndpointStatsByColumnWithFilters(ctx, "inbound_endpoint", startTime, endTime, userID, apiKeyID, providerID, groupID, 0, model, "", requestType, stream, billingType, "", false, false, nil)
}

// GetUpstreamEndpointStatsWithFilters returns upstream endpoint statistics with optional filters.
func (r *Store) GetUpstreamEndpointStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) ([]EndpointStat, error) {
	return r.getEndpointStatsByColumnWithFilters(ctx, "upstream_endpoint", startTime, endTime, userID, apiKeyID, providerID, groupID, 0, model, "", requestType, stream, billingType, "", false, false, nil)
}

// GetProviderUsageStats returns the provider's complete report for the requested range.
func (r *Store) GetProviderUsageStats(ctx context.Context, providerID int64, startTime, endTime time.Time) (resp *ProviderUsageStatsResponse, err error) {
	daysCount := int(endTime.Sub(startTime).Hours()/24) + 1
	if daysCount <= 0 {
		daysCount = 30
	}

	data, err := r.readProviderReport(ctx, providerID, startTime, endTime)
	if err != nil {
		return nil, err
	}
	history := data.history

	var totalProviderCost, totalUserCost, totalStandardCost float64
	var totalRequests, totalTokens int64
	var highestCostDay, highestRequestDay *ProviderUsageHistory

	for i := range history {
		h := &history[i]
		totalProviderCost += h.ActualCost
		totalUserCost += h.UserCost
		totalStandardCost += h.Cost
		totalRequests += h.Requests
		totalTokens += h.Tokens

		if highestCostDay == nil || h.ActualCost > highestCostDay.ActualCost {
			highestCostDay = h
		}
		if highestRequestDay == nil || h.Requests > highestRequestDay.Requests {
			highestRequestDay = h
		}
	}

	actualDaysUsed := len(history)
	if actualDaysUsed == 0 {
		actualDaysUsed = 1
	}

	var avgDuration float64
	if data.durationCount > 0 {
		avgDuration = float64(data.durationSum) / float64(data.durationCount)
	}

	summary := ProviderUsageSummary{
		Days:              daysCount,
		ActualDaysUsed:    actualDaysUsed,
		TotalCost:         totalProviderCost,
		TotalUserCost:     totalUserCost,
		TotalStandardCost: totalStandardCost,
		TotalRequests:     totalRequests,
		TotalTokens:       totalTokens,
		AvgDailyCost:      totalProviderCost / float64(actualDaysUsed),
		AvgDailyUserCost:  totalUserCost / float64(actualDaysUsed),
		AvgDailyRequests:  float64(totalRequests) / float64(actualDaysUsed),
		AvgDailyTokens:    float64(totalTokens) / float64(actualDaysUsed),
		AvgDurationMs:     avgDuration,
	}

	todayStr := r.calendar.Now().Format("2006-01-02")
	for i := range history {
		if history[i].Date == todayStr {
			summary.Today = &struct {
				Date     string  `json:"date"`
				Cost     float64 `json:"cost"`
				UserCost float64 `json:"user_cost"`
				Requests int64   `json:"requests"`
				Tokens   int64   `json:"tokens"`
			}{
				Date:     history[i].Date,
				Cost:     history[i].ActualCost,
				UserCost: history[i].UserCost,
				Requests: history[i].Requests,
				Tokens:   history[i].Tokens,
			}
			break
		}
	}

	if highestCostDay != nil {
		summary.HighestCostDay = &struct {
			Date     string  `json:"date"`
			Label    string  `json:"label"`
			Cost     float64 `json:"cost"`
			UserCost float64 `json:"user_cost"`
			Requests int64   `json:"requests"`
		}{
			Date:     highestCostDay.Date,
			Label:    highestCostDay.Label,
			Cost:     highestCostDay.ActualCost,
			UserCost: highestCostDay.UserCost,
			Requests: highestCostDay.Requests,
		}
	}

	if highestRequestDay != nil {
		summary.HighestRequestDay = &struct {
			Date     string  `json:"date"`
			Label    string  `json:"label"`
			Requests int64   `json:"requests"`
			Cost     float64 `json:"cost"`
			UserCost float64 `json:"user_cost"`
		}{
			Date:     highestRequestDay.Date,
			Label:    highestRequestDay.Label,
			Requests: highestRequestDay.Requests,
			Cost:     highestRequestDay.ActualCost,
			UserCost: highestRequestDay.UserCost,
		}
	}

	resp = &ProviderUsageStatsResponse{
		History:           history,
		Summary:           summary,
		Models:            data.models,
		Endpoints:         data.endpoints,
		UpstreamEndpoints: data.upstreamEndpoints,
	}
	return resp, nil
}
