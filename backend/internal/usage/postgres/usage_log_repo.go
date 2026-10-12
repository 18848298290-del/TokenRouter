package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	gocache "github.com/patrickmn/go-cache"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

const (
	// rawUsageLogModelColumn selects the stored model value for direct filters.
	// Historical rows can contain upstream or billing models; current rows contain the internal request model.
	// Use resolveModelDimensionExpression for requested, upstream, or mapping analytics.
	rawUsageLogModelColumn            = "model"
	usageAnalyticsFallbackLogInterval = time.Minute

	// usageLogSuccessFilterUL selects charged rows for queries using the ul alias.
	// The cost predicate excludes zero-cost failure placeholders and free requests.
	//
	// Checking cost also includes per-request and image charges whose text token counts are zero.
	usageLogSuccessFilterUL = "ul.actual_cost > 0"

	// usageLogEffectivePlatformExpr uses the stored platform so configuration changes do not reassign historical usage.
	usageLogEffectivePlatformExpr = "ul.platform"
)

var (
	usageAnalyticsFallbackLogState = struct {
		sync.Mutex
		lastByOperation map[string]time.Time
	}{lastByOperation: make(map[string]time.Time)}

	// dateFormatWhitelist maps granularities to fixed PostgreSQL TO_CHAR formats for SQL interpolation.
	dateFormatWhitelist = map[string]string{
		"hour":  "YYYY-MM-DD HH24:00",
		"day":   "YYYY-MM-DD",
		"week":  "IYYY-IW",
		"month": "YYYY-MM",
	}
)

type Store struct {
	batchLifecycleMu sync.RWMutex
	batchStopped     bool
	batchWG          sync.WaitGroup
	client           *dbent.Client
	sql              sqlExecutor
	db               *sql.DB
	preAggregation   *preaggregation.PreAggregationSettingsService
	calendar         timezone.Calendar

	createBatchOnce     sync.Once
	createBatchCh       chan usageLogCreateRequest
	bestEffortBatchOnce sync.Once
	bestEffortBatchCh   chan usageLogBestEffortRequest
	bestEffortRecent    *gocache.Cache
}

type UsageRankingItem = usage.UsageRankingItem

type UsageRankingResponse = usage.UsageRankingResponse

func NewUsageLogRepository(client *dbent.Client, sqlDB *sql.DB, preAggregation *preaggregation.PreAggregationSettingsService, calendar timezone.Calendar) *Store {
	repo := NewUsageLogRepositoryWithSQL(client, sqlDB, calendar)
	repo.preAggregation = preAggregation
	return repo
}

func NewUsageLogRepositoryWithSQL(client *dbent.Client, sqlq sqlExecutor, calendar timezone.Calendar) *Store {
	// Queries use scanSingleRow so Ent transactions can implement sqlExecutor.
	repo := &Store{client: client, sql: sqlq, calendar: calendar}
	if db, ok := sqlq.(*sql.DB); ok {
		repo.db = db
	}
	repo.bestEffortRecent = gocache.New(usageLogBestEffortRecentTTL, 0)
	return repo
}

// logUsageAnalyticsFallback rate-limits warnings when aggregate queries fail and raw queries take over.
func (r *Store) logUsageAnalyticsFallback(operation string, err error) {
	if err == nil || !shouldLogUsageAnalyticsFallback(operation, time.Now()) {
		return
	}
	slog.Warn("预聚合查询失败，已透明回退使用记录原始表", "operation", operation, "error", err)
}

// shouldLogUsageAnalyticsFallback allows one warning per operation per minute.
func shouldLogUsageAnalyticsFallback(operation string, now time.Time) bool {
	usageAnalyticsFallbackLogState.Lock()
	defer usageAnalyticsFallbackLogState.Unlock()
	last := usageAnalyticsFallbackLogState.lastByOperation[operation]
	if now.Sub(last) < usageAnalyticsFallbackLogInterval {
		return false
	}
	usageAnalyticsFallbackLogState.lastByOperation[operation] = now
	return true
}

// safeDateFormat returns the allowed format, defaulting to daily buckets.
func safeDateFormat(granularity string) string {
	if f, ok := dateFormatWhitelist[granularity]; ok {
		return f
	}
	return "YYYY-MM-DD"
}

// appendRawUsageLogModelWhereCondition filters the stored model column, including historical values.
// Use resolveModelDimensionExpression for requested or upstream analytics.
func appendRawUsageLogModelWhereCondition(conditions []string, args []any, model string) ([]string, []any) {
	if strings.TrimSpace(model) == "" {
		return conditions, args
	}
	conditions = append(conditions, fmt.Sprintf("%s = $%d", rawUsageLogModelColumn, len(args)+1))
	args = append(args, model)
	return conditions, args
}

func appendUsageLogBillingModeWhereCondition(conditions []string, args []any, billingMode string) ([]string, []any) {
	return appendUsageLogBillingModeWhereConditionWithAlias(conditions, args, billingMode, "")
}

func appendUsageLogBillingModeWhereConditionWithAlias(conditions []string, args []any, billingMode string, alias string) ([]string, []any) {
	mode := strings.TrimSpace(billingMode)
	if mode == "" {
		return conditions, args
	}
	column := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	placeholder := fmt.Sprintf("$%d", len(args)+1)
	switch pricing.BillingMode(mode) {
	case pricing.BillingModeImage:
		conditions = append(conditions, fmt.Sprintf("(%s = %s OR ((%s IS NULL OR %s = '') AND COALESCE(%s, 0) <= 0 AND COALESCE(%s, 0) > 0))", column("billing_mode"), placeholder, column("billing_mode"), column("billing_mode"), column("video_duration_seconds"), column("image_count")))
	case pricing.BillingModeVideo:
		conditions = append(conditions, fmt.Sprintf("(%s = %s OR ((%s IS NULL OR %s = '') AND COALESCE(%s, 0) > 0))", column("billing_mode"), placeholder, column("billing_mode"), column("billing_mode"), column("video_duration_seconds")))
	case pricing.BillingModeToken:
		conditions = append(conditions, fmt.Sprintf("(%s = %s OR ((%s IS NULL OR %s = '') AND COALESCE(%s, 0) <= 0 AND COALESCE(%s, 0) <= 0))", column("billing_mode"), placeholder, column("billing_mode"), column("billing_mode"), column("video_duration_seconds"), column("image_count")))
	default:
		conditions = append(conditions, fmt.Sprintf("%s = %s", column("billing_mode"), placeholder))
	}
	args = append(args, mode)
	return conditions, args
}

func appendUsageLogBillingModeQueryFilter(query string, args []any, billingMode string, alias string) (string, []any) {
	conditions, args := appendUsageLogBillingModeWhereConditionWithAlias(nil, args, billingMode, alias)
	if len(conditions) == 0 {
		return query, args
	}
	return query + " AND " + conditions[0], args
}

func appendUsageLogModelWhereCondition(conditions []string, args []any, model string, source string) ([]string, []any) {
	if strings.TrimSpace(source) == "" {
		return appendRawUsageLogModelWhereCondition(conditions, args, model)
	}
	if strings.TrimSpace(model) == "" {
		return conditions, args
	}
	conditions = append(conditions, fmt.Sprintf("%s = $%d", resolveModelDimensionExpression(source), len(args)+1))
	args = append(args, model)
	return conditions, args
}

// appendRawUsageLogModelQueryFilter filters the stored model column, including historical values.
// Use resolveModelDimensionExpression for requested or upstream analytics.
func appendRawUsageLogModelQueryFilter(query string, args []any, model string) (string, []any) {
	if strings.TrimSpace(model) == "" {
		return query, args
	}
	query += fmt.Sprintf(" AND %s = $%d", rawUsageLogModelColumn, len(args)+1)
	args = append(args, model)
	return query, args
}

func appendUsageLogModelQueryFilter(query string, args []any, model string, source string) (string, []any) {
	if strings.TrimSpace(source) == "" {
		return appendRawUsageLogModelQueryFilter(query, args, model)
	}
	if strings.TrimSpace(model) == "" {
		return query, args
	}
	query += fmt.Sprintf(" AND %s = $%d", resolveModelDimensionExpression(source), len(args)+1)
	args = append(args, model)
	return query, args
}

func buildWhere(conditions []string) string {
	if len(conditions) == 0 {
		return ""
	}
	return "WHERE " + strings.Join(conditions, " AND ")
}

func appendRequestTypeOrStreamWhereCondition(conditions []string, args []any, requestType *int16, stream *bool) ([]string, []any) {
	if requestType != nil {
		condition, conditionArgs := buildRequestTypeFilterCondition(len(args)+1, *requestType)
		conditions = append(conditions, condition)
		args = append(args, conditionArgs...)
		return conditions, args
	}
	if stream != nil {
		conditions = append(conditions, fmt.Sprintf("stream = $%d", len(args)+1))
		args = append(args, *stream)
	}
	return conditions, args
}

func appendRequestTypeOrStreamQueryFilter(query string, args []any, requestType *int16, stream *bool) (string, []any) {
	if requestType != nil {
		condition, conditionArgs := buildRequestTypeFilterCondition(len(args)+1, *requestType)
		query += " AND " + condition
		args = append(args, conditionArgs...)
		return query, args
	}
	if stream != nil {
		query += fmt.Sprintf(" AND stream = $%d", len(args)+1)
		args = append(args, *stream)
	}
	return query, args
}

// appendNativeCompactionV2WhereCondition adds a compaction predicate when value is set.
func appendNativeCompactionV2WhereCondition(conditions []string, args []any, value *bool, alias string) ([]string, []any) {
	if value == nil {
		return conditions, args
	}
	column := "native_compaction_v2"
	if alias != "" {
		column = alias + "." + column
	}
	conditions = append(conditions, fmt.Sprintf("%s = $%d", column, len(args)+1))
	args = append(args, *value)
	return conditions, args
}

// appendNativeCompactionV2QueryFilter appends a compaction predicate to an existing WHERE clause.
func appendNativeCompactionV2QueryFilter(query string, args []any, value *bool, alias string) (string, []any) {
	conditions, args := appendNativeCompactionV2WhereCondition(nil, args, value, alias)
	if len(conditions) == 0 {
		return query, args
	}
	return query + " AND " + conditions[0], args
}

// buildRequestTypeFilterCondition includes legacy stream fields when filtering request types.
func buildRequestTypeFilterCondition(startArgIndex int, requestType int16) (string, []any) {
	return buildRequestTypeFilterConditionWithAlias(startArgIndex, requestType, "")
}

// buildRequestTypeFilterConditionWithAlias qualifies the legacy-compatible request type predicate.
func buildRequestTypeFilterConditionWithAlias(startArgIndex int, requestType int16, alias string) (string, []any) {
	normalized := usage.RequestTypeFromInt16(requestType)
	requestTypeArg := int16(normalized)
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	switch normalized {
	case usage.RequestTypeSync:
		return fmt.Sprintf("(%srequest_type = $%d OR (%srequest_type = %d AND %sstream = FALSE AND %sopenai_ws_mode = FALSE))", prefix, startArgIndex, prefix, int16(usage.RequestTypeUnknown), prefix, prefix), []any{requestTypeArg}
	case usage.RequestTypeStream:
		return fmt.Sprintf("(%srequest_type = $%d OR (%srequest_type = %d AND %sstream = TRUE AND %sopenai_ws_mode = FALSE))", prefix, startArgIndex, prefix, int16(usage.RequestTypeUnknown), prefix, prefix), []any{requestTypeArg}
	case usage.RequestTypeWSV2:
		return fmt.Sprintf("(%srequest_type = $%d OR (%srequest_type = %d AND %sopenai_ws_mode = TRUE))", prefix, startArgIndex, prefix, int16(usage.RequestTypeUnknown), prefix), []any{requestTypeArg}
	default:
		return fmt.Sprintf("%srequest_type = $%d", prefix, startArgIndex), []any{requestTypeArg}
	}
}

// GetDashboardPublicStats reads the user and token counts exposed on the public homepage.
func (r *Store) GetDashboardPublicStats(ctx context.Context, start, end time.Time, useAggregates bool) (*usage.DashboardPublicStats, error) {
	stats := &usage.DashboardPublicStats{}
	if err := r.fillDashboardPublicUserStats(ctx, stats); err != nil {
		return nil, err
	}
	if useAggregates {
		if err := r.fillDashboardPublicTokenStatsAggregated(ctx, stats, r.calendar.Today()); err != nil {
			return nil, err
		}
		return stats, nil
	}
	if err := r.fillDashboardPublicTokenStatsFromUsageLogs(ctx, stats, start, end, r.calendar.Today()); err != nil {
		return nil, err
	}
	return stats, nil
}

// usageRankingQueryParts selects fixed SQL fragments for the configured ranking metric.
func usageRankingQueryParts(sortBy usage.UsageRankingSortBy) (eligibility, orderBy string) {
	switch usage.NormalizeUsageRankingSortBy(string(sortBy)) {
	case usage.UsageRankingSortByRequests:
		return "COUNT(*) > 0", "requests DESC, total_tokens DESC, actual_cost DESC, user_id ASC"
	case usage.UsageRankingSortByActualCost:
		return "COALESCE(SUM(u.actual_cost), 0) > 0", "actual_cost DESC, total_tokens DESC, requests DESC, user_id ASC"
	default:
		return "COALESCE(SUM(u.input_tokens + u.output_tokens + u.cache_creation_tokens + u.cache_read_tokens), 0) > 0", "total_tokens DESC, requests DESC, actual_cost DESC, user_id ASC"
	}
}

// GetUsageRanking returns public usage rankings ordered by the configured metric.
func (r *Store) GetUsageRanking(ctx context.Context, startTime, endTime time.Time, limit int, sortBy usage.UsageRankingSortBy) (result *UsageRankingResponse, err error) {
	if limit <= 0 {
		limit = usage.DefaultUsageRankingLimit
	}
	if aggregated, ok, aggregateErr := r.getUsageRankingFromAnalytics(ctx, startTime, endTime, limit, sortBy); aggregateErr == nil && ok {
		return aggregated, nil
	} else if aggregateErr != nil {
		r.logUsageAnalyticsFallback("usage_ranking", aggregateErr)
	}

	eligibility, orderBy := usageRankingQueryParts(sortBy)
	query := fmt.Sprintf(`
		WITH user_usage AS (
			SELECT
				COALESCE(u.billing_user_id, u.user_id) as user_id,
				COUNT(*) as requests,
				COALESCE(SUM(u.input_tokens), 0) as input_tokens,
				COALESCE(SUM(u.output_tokens), 0) as output_tokens,
				COALESCE(SUM(u.cache_creation_tokens), 0) as cache_creation_tokens,
				COALESCE(SUM(u.cache_read_tokens), 0) as cache_read_tokens,
				COALESCE(SUM(u.input_tokens + u.output_tokens + u.cache_creation_tokens + u.cache_read_tokens), 0) as total_tokens,
				COALESCE(SUM(u.actual_cost), 0) as actual_cost
			FROM usage_logs u
			WHERE u.created_at >= $1 AND u.created_at < $2
			-- 排行按付款主体归属，团队成员使用团队 Key 时计入团队 Owner。
			GROUP BY COALESCE(u.billing_user_id, u.user_id)
			HAVING %s
		),
		ranked AS (
			SELECT
				ROW_NUMBER() OVER (ORDER BY %s) as rank,
				user_id,
				requests,
				input_tokens,
				output_tokens,
				cache_creation_tokens,
				cache_read_tokens,
				total_tokens,
				actual_cost,
				COALESCE(SUM(requests) OVER (), 0) as total_requests,
				COALESCE(SUM(total_tokens) OVER (), 0) as ranking_total_tokens,
				COALESCE(SUM(actual_cost) OVER (), 0) as total_actual_cost
			FROM user_usage
			ORDER BY %s
			LIMIT $3
		)
		SELECT
			r.rank,
			r.user_id,
			COALESCE(us.email, '') as email,
			COALESCE(us.username, '') as username,
			COALESCE(ua.url, '') as avatar_url,
			r.requests,
			r.input_tokens,
			r.output_tokens,
			r.cache_creation_tokens,
			r.cache_read_tokens,
			r.total_tokens,
			r.actual_cost,
			r.total_requests,
			r.ranking_total_tokens,
			r.total_actual_cost
		FROM ranked r
		LEFT JOIN users us ON r.user_id = us.id
		LEFT JOIN user_avatars ua ON ua.user_id = r.user_id
		ORDER BY r.rank ASC
	`, eligibility, orderBy, orderBy)

	rows, err := r.sql.QueryContext(ctx, query, startTime, endTime, limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			result = nil
		}
	}()

	ranking := make([]UsageRankingItem, 0)
	totalRequests := int64(0)
	totalTokens := int64(0)
	totalActualCost := 0.0
	for rows.Next() {
		var row UsageRankingItem
		var email string
		var username string
		if err = rows.Scan(
			&row.Rank,
			&row.UserID,
			&email,
			&username,
			&row.AvatarURL,
			&row.Requests,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CacheCreationTokens,
			&row.CacheReadTokens,
			&row.TotalTokens,
			&row.ActualCost,
			&totalRequests,
			&totalTokens,
			&totalActualCost,
		); err != nil {
			return nil, err
		}
		row.DisplayName = rankingDisplayName(username, email, row.UserID)
		ranking = append(ranking, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	return &UsageRankingResponse{
		Ranking:         ranking,
		TotalRequests:   totalRequests,
		TotalTokens:     totalTokens,
		TotalActualCost: totalActualCost,
	}, nil
}

func (r *Store) fillDashboardPublicTokenStatsAggregated(ctx context.Context, stats *usage.DashboardPublicStats, todayStart time.Time) error {
	query := `
		SELECT
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) FILTER (WHERE bucket_date = $1::date), 0) AS today_tokens,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS total_tokens
		FROM usage_dashboard_daily
	`
	return scanSingleRow(ctx, r.sql, query, []any{todayStart}, &stats.TodayTokens, &stats.TotalTokens)
}

func (r *Store) fillDashboardPublicTokenStatsFromUsageLogs(ctx context.Context, stats *usage.DashboardPublicStats, start, end, todayStart time.Time) error {
	startUTC := start.UTC()
	endUTC := end.UTC()
	todayStartUTC := todayStart.UTC()
	todayEndUTC := todayStartUTC.Add(24 * time.Hour)
	query := `
		WITH scoped AS (
			SELECT
				created_at,
				input_tokens,
				output_tokens,
				cache_creation_tokens,
				cache_read_tokens
			FROM usage_logs
			WHERE created_at >= LEAST($1::timestamptz, $3::timestamptz)
				AND created_at < GREATEST($2::timestamptz, $4::timestamptz)
		)
		SELECT
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) FILTER (WHERE created_at >= $3::timestamptz AND created_at < $4::timestamptz), 0) AS today_tokens,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) FILTER (WHERE created_at >= $1::timestamptz AND created_at < $2::timestamptz), 0) AS total_tokens
		FROM scoped
	`
	return scanSingleRow(ctx, r.sql, query, []any{startUTC, endUTC, todayStartUTC, todayEndUTC}, &stats.TodayTokens, &stats.TotalTokens)
}

func (r *Store) fillDashboardPublicUserStats(ctx context.Context, stats *usage.DashboardPublicStats) error {
	query := `SELECT COUNT(*) FROM users WHERE deleted_at IS NULL`
	return scanSingleRow(ctx, r.sql, query, nil, &stats.TotalUsers)
}

// rankingDisplayName returns a username, masked email, or user ID label.
func rankingDisplayName(username, email string, userID int64) string {
	if name := strings.TrimSpace(username); name != "" {
		return name
	}
	if email = strings.TrimSpace(email); email != "" {
		return identity.MaskEmail(email)
	}
	return fmt.Sprintf("User #%d", userID)
}

// ExpireRuntimeCaches removes expired deduplication entries; the caller schedules cleanup.
func (r *Store) ExpireRuntimeCaches() {
	if r.bestEffortRecent != nil {
		r.bestEffortRecent.DeleteExpired()
	}
}

// StopUsageBatchers closes both queues and waits for their workers to drain them.
func (r *Store) StopUsageBatchers() {
	r.batchLifecycleMu.Lock()
	if !r.batchStopped {
		r.batchStopped = true
		if r.createBatchCh != nil {
			close(r.createBatchCh)
		}
		if r.bestEffortBatchCh != nil {
			close(r.bestEffortBatchCh)
		}
	}
	r.batchLifecycleMu.Unlock()
	r.batchWG.Wait()
}
