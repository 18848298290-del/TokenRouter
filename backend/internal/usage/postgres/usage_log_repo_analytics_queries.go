package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// usage_log_repo_analytics_queries.go builds filtered analytics sources and
// reads multidimensional usage reports from them.
// Multidimensional reports can use rollups only when every requested filter and
// grouping exists in their stored dimensions. Provider, request-ID, compaction,
// and unsupported model dimensions select raw-query handling, while compatible
// reports combine covered UTC buckets with normalized raw edges.
//
// The combined source supplies both actor and payer identities, enabling
// owner-visible user scopes and billing-user rankings to answer different
// questions. Query builders keep timestamp, date, dimension, timezone, and limit
// parameters in one ordered list. Report readers return an eligibility flag
// separately from errors so callers can distinguish unavailable aggregation from
// a failed attempt, and ranking totals are computed before row limits to retain
// the full eligible population.

// usageAnalyticsQuery carries combined-source SQL, its predicates, and ordered
// parameters.
type usageAnalyticsQuery struct {
	// cte, where, and args describe one parameterized query. Consumers append their
	// timezone, dimensions, or limit after args and derive positions from its
	// length.
	cte   string // combined source CTE text
	where string // optional post-combination restriction
	args  []any  // ordered bound source values
}

// buildUsageAnalyticsQuery constructs the combined source, restrictions, and
// parameters for compatible analytics. Unsupported
// provider/request-ID/compaction dimensions or a model filter outside the
// requested dimension return false before execution; unusable coverage also
// returns false.
//
// Complete UTC days can replace middle hourly buckets, while normalized raw
// records supply uncovered edges. Owned-team selection keeps raw actor and team
// scans disjoint and applies the same scope after combination. RequestType and
// Stream become separate predicates when both are present. Time bounds precede
// filters, and date-only arguments remain separate from timestamps so consumers
// can append further parameters by the current list length.
func (r *Store) buildUsageAnalyticsQuery(ctx context.Context, filters UsageLogFilters, start, end time.Time, useDaily bool) (usageAnalyticsQuery, bool, error) {
	// These dimensions have no rollup representation. Decline combined-source
	// construction so the caller can apply the complete restriction to raw records.
	if filters.ProviderID > 0 || strings.TrimSpace(filters.RequestID) != "" || filters.NativeCompactionV2 != nil {
		return usageAnalyticsQuery{}, false, nil
	}
	modelSource := strings.TrimSpace(filters.ModelFilterSource)
	if strings.TrimSpace(filters.Model) != "" && modelSource != usage.ModelSourceRequested {
		return usageAnalyticsQuery{}, false, nil
	}
	window, ok, err := r.resolveUsageAnalyticsWindow(ctx, start, end)
	if err != nil || !ok {
		return usageAnalyticsQuery{}, false, err
	}

	args := []any{
		window.start, window.end, window.aggregateStart, window.aggregateEnd,
		window.rawTailStart,
	}
	if useDaily {
		// Daily predicates cast their parameters to dates. Separate arguments retain
		// timestamp typing for hourly bounds, preventing an earlier-hour scan.
		args = append(args, window.dailySplit().args()...)
	}
	rawUsageSource := "usage_logs ul"
	conditions := make([]string, 0, 10)
	addCondition := func(format string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(format, len(args)))
	}

	if filters.UserID > 0 {
		if filters.IncludeOwnedTeam {
			teamID, teamErr := r.getOwnedTeamID(ctx, filters.UserID)
			if teamErr != nil {
				return usageAnalyticsQuery{}, false, teamErr
			}
			args = append(args, filters.UserID)
			userPosition := len(args)
			if teamID > 0 {
				args = append(args, teamID)
				teamPosition := len(args)
				rawUsageSource = fmt.Sprintf(`(
						SELECT * FROM usage_logs WHERE user_id = $%d
						UNION ALL
						SELECT * FROM usage_logs WHERE team_id = $%d AND user_id <> $%d
					) ul`, userPosition, teamPosition, userPosition)
				conditions = append(conditions, fmt.Sprintf(
					"(user_id = $%d OR (team_id = $%d AND user_id <> $%d))",
					userPosition, teamPosition, userPosition,
				))
			} else {
				conditions = append(conditions, fmt.Sprintf("user_id = $%d", userPosition))
			}
		} else {
			addCondition("user_id = $%d", filters.UserID)
		}
	}
	if filters.APIKeyID > 0 {
		addCondition("api_key_id = $%d", filters.APIKeyID)
	}
	if filters.GroupID > 0 {
		addCondition("group_id = $%d", filters.GroupID)
	}
	if filters.TeamID > 0 {
		addCondition("team_id = $%d", filters.TeamID)
	}
	if filters.PersonalOnly {
		conditions = append(conditions, "team_id = 0")
	}
	if model := strings.TrimSpace(filters.Model); model != "" {
		addCondition("requested_model = $%d", model)
	}
	if filters.RequestType != nil {
		addCondition("request_type = $%d", *filters.RequestType)
	}
	if filters.Stream != nil {
		addCondition("stream = $%d", *filters.Stream)
	}
	if filters.BillingType != nil {
		addCondition("billing_type = $%d", int16(*filters.BillingType))
	}
	if mode := strings.TrimSpace(filters.BillingMode); mode != "" {
		addCondition("billing_mode = $%d", mode)
	}

	metricColumns := `
		user_id, billing_user_id, team_id, api_key_id, group_id, requested_model,
		request_type, stream, billing_type, billing_mode, platform, inbound_endpoint,
		total_requests, input_tokens, output_tokens, cache_creation_tokens,
		cache_read_tokens, total_cost, actual_cost, provider_cost,
		total_duration_ms, duration_count`
	parts := make([]string, 0, 4)
	if useDaily {
		parts = append(parts, `
			SELECT bucket_date::timestamp AT TIME ZONE 'UTC' AS occurred_at, `+metricColumns+`
			FROM usage_analytics_daily
			WHERE bucket_date >= $8::date AND bucket_date < $9::date`)
	}
	hourlyRange := "bucket_start >= $3 AND bucket_start < $4"
	if useDaily {
		hourlyRange = "((bucket_start >= $3 AND bucket_start < $6) OR (bucket_start >= $7 AND bucket_start < $4))"
	}
	parts = append(parts, `
		SELECT bucket_start AS occurred_at, `+metricColumns+`
		FROM usage_analytics_hourly
		WHERE `+hourlyRange)
	parts = append(parts, `
		SELECT
			ul.created_at AS occurred_at,
`+usageAnalyticsRawDimensionsSQL+`
			1, ul.input_tokens, ul.output_tokens, ul.cache_creation_tokens,
			ul.cache_read_tokens, ul.total_cost, ul.actual_cost,
			COALESCE(ul.provider_stats_cost, ul.total_cost) * COALESCE(ul.provider_rate_multiplier, 1),
			COALESCE(ul.duration_ms, 0), CASE WHEN ul.duration_ms IS NULL THEN 0 ELSE 1 END
		FROM `+rawUsageSource+`
		LEFT JOIN groups g ON g.id = ul.group_id
		LEFT JOIN providers a ON a.id = ul.provider_id
		WHERE (ul.created_at >= $1 AND ul.created_at < $3)
		   OR (ul.created_at >= $5 AND ul.created_at < $2)`)

	return usageAnalyticsQuery{
		cte:   "WITH combined AS NOT MATERIALIZED (" + strings.Join(parts, " UNION ALL ") + ")",
		where: buildWhere(conditions),
		args:  args,
	}, true, nil
}

// getUsageStatsFromAnalytics returns combined totals when filters provide a
// valid bounded range and compatible dimensions. Missing bounds or unusable
// coverage return false, while SQL errors discard the result for caller
// recovery.
//
// The source sums request, token, user-charge, base-cost, and provider-cost
// metrics separately. Duration uses the sum of observed durations divided by
// their nonnull count, retaining the raw AVG(duration_ms) rule across
// aggregates. TotalProviderCost is allocated even for zero sums, and TotalTokens
// includes creation and read-cache tokens as well as input/output tokens.
func (r *Store) getUsageStatsFromAnalytics(ctx context.Context, filters UsageLogFilters) (*UsageStats, bool, error) {
	if filters.StartTime == nil || filters.EndTime == nil || !filters.EndTime.After(*filters.StartTime) {
		return nil, false, nil
	}
	query, ok, err := r.buildUsageAnalyticsQuery(ctx, filters, *filters.StartTime, *filters.EndTime, true)
	if err != nil || !ok {
		return nil, false, err
	}
	stats := &UsageStats{}
	var totalDuration, durationCount int64
	var totalProviderCost float64
	err = scanSingleRow(ctx, r.sql, query.cte+`
		SELECT
			COALESCE(SUM(total_requests), 0), COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0), COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(total_cost), 0), COALESCE(SUM(actual_cost), 0),
			COALESCE(SUM(provider_cost), 0), COALESCE(SUM(total_duration_ms), 0),
			COALESCE(SUM(duration_count), 0)
		FROM combined `+query.where, query.args,
		&stats.TotalRequests, &stats.TotalInputTokens, &stats.TotalOutputTokens,
		&stats.TotalCacheTokens, &stats.TotalCacheCreationTokens, &stats.TotalCacheReadTokens,
		&stats.TotalCost, &stats.TotalActualCost, &totalProviderCost,
		&totalDuration, &durationCount,
	)
	if err != nil {
		return nil, false, err
	}
	stats.TotalTokens = stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheTokens
	stats.TotalProviderCost = &totalProviderCost
	if durationCount > 0 {
		stats.AverageDurationMs = float64(totalDuration) / float64(durationCount)
	}
	return stats, true, nil
}

// getUsageTrendFromAnalytics returns combined usage buckets in the resolved
// reporting timezone. Unsupported dimensions or unavailable coverage return
// false; granularity uses safeDateFormat, and the named timezone is appended
// after the source's current bound arguments.
func (r *Store) getUsageTrendFromAnalytics(ctx context.Context, start, end time.Time, granularity string, filters UsageLogFilters) ([]TrendDataPoint, bool, error) {
	query, ok, err := r.buildUsageAnalyticsQuery(ctx, filters, start, end, false)
	if err != nil || !ok {
		return nil, false, err
	}
	query.args = append(query.args, r.resolveUsageStatsTimezone())
	timezonePosition := len(query.args)
	dateFormat := safeDateFormat(granularity)
	rows, err := r.sql.QueryContext(ctx, query.cte+fmt.Sprintf(`
		SELECT
			TO_CHAR(occurred_at AT TIME ZONE $%d, '%s'),
			COALESCE(SUM(total_requests), 0), COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0),
			COALESCE(SUM(total_cost), 0), COALESCE(SUM(actual_cost), 0)
		FROM combined %s
		GROUP BY 1 ORDER BY 1`, timezonePosition, dateFormat, query.where), query.args...)
	if err != nil {
		return nil, false, err
	}
	// Release the acquired report rows after scanning or an early return. Close
	// errors are ignored here; query, scan, and iteration errors reject the result.
	defer func() { _ = rows.Close() }()
	results, err := scanTrendRows(rows)
	return results, true, err
}

// getModelStatsFromAnalytics groups compatible combined rows by internal request
// model. Upstream/mapping groupings return false, while eligible reports retain
// separate base cost, user charge, and provider cost values and order by total
// tokens.
func (r *Store) getModelStatsFromAnalytics(ctx context.Context, start, end time.Time, filters UsageLogFilters) ([]ModelStat, bool, error) {
	if usage.NormalizeModelSource(filters.ModelFilterSource) != usage.ModelSourceRequested {
		// Upstream and mapping identities were not retained in these rollups, so
		// grouping by them requires a complete raw query to keep models distinct.
		return nil, false, nil
	}
	query, ok, err := r.buildUsageAnalyticsQuery(ctx, filters, start, end, true)
	if err != nil || !ok {
		return nil, false, err
	}
	rows, err := r.sql.QueryContext(ctx, query.cte+`
		SELECT requested_model, COALESCE(SUM(total_requests), 0),
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cache_creation_tokens), 0), COALESCE(SUM(cache_read_tokens), 0),
		       COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0),
		       COALESCE(SUM(total_cost), 0), COALESCE(SUM(actual_cost), 0),
		       COALESCE(SUM(provider_cost), 0)
		FROM combined `+query.where+`
		GROUP BY requested_model ORDER BY 7 DESC`, query.args...)
	if err != nil {
		return nil, false, err
	}
	// Release the acquired report rows after scanning or an early return. Close
	// errors are ignored here; query, scan, and iteration errors reject the result.
	defer func() { _ = rows.Close() }()
	results, err := scanModelStatsRows(rows)
	return results, true, err
}

// getGroupStatsFromAnalytics returns combined routed-group totals with current
// display names. Unusable sources return false; zero group IDs remain the
// missing-group bucket, and locale-aware user presentation chooses localized
// names while costs retain separate meanings.
func (r *Store) getGroupStatsFromAnalytics(ctx context.Context, start, end time.Time, filters UsageLogFilters) ([]usage.GroupStat, bool, error) {
	query, ok, err := r.buildUsageAnalyticsQuery(ctx, filters, start, end, true)
	if err != nil || !ok {
		return nil, false, err
	}
	extraGroup := ""
	if locale.UserPresentation(ctx) {
		extraGroup = ", g.localization"
	}
	rows, err := r.sql.QueryContext(ctx, query.cte+`
		SELECT c.group_id, `+postgresinfra.LocalizedTextExpression(ctx, "g.localization", "display_name", "COALESCE(g.name, '')")+`, COALESCE(SUM(c.total_requests), 0),
		       COALESCE(SUM(c.input_tokens + c.output_tokens + c.cache_creation_tokens + c.cache_read_tokens), 0),
		       COALESCE(SUM(c.total_cost), 0), COALESCE(SUM(c.actual_cost), 0),
		       COALESCE(SUM(c.provider_cost), 0)
		FROM combined c
		LEFT JOIN groups g ON g.id = NULLIF(c.group_id, 0) `+query.where+`
		GROUP BY c.group_id, g.name`+extraGroup+` ORDER BY 4 DESC`, query.args...)
	if err != nil {
		return nil, false, err
	}
	// Release the acquired report rows after scanning or an early return. Close
	// errors are ignored here; query, scan, and iteration errors reject the result.
	defer func() { _ = rows.Close() }()
	results := make([]usage.GroupStat, 0)
	for rows.Next() {
		var row usage.GroupStat
		if err := rows.Scan(&row.GroupID, &row.GroupName, &row.Requests, &row.TotalTokens, &row.Cost, &row.ActualCost, &row.ProviderCost); err != nil {
			return nil, false, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return results, true, nil
}

// getInboundEndpointStatsFromAnalytics groups compatible source rows by trimmed
// inbound endpoint. Null or blank endpoints use unknown; unavailable analytics
// returns false, and totals include requests, all token buckets, base costs, and
// user charges.
func (r *Store) getInboundEndpointStatsFromAnalytics(ctx context.Context, start, end time.Time, filters UsageLogFilters) ([]EndpointStat, bool, error) {
	query, ok, err := r.buildUsageAnalyticsQuery(ctx, filters, start, end, true)
	if err != nil || !ok {
		return nil, false, err
	}
	rows, err := r.sql.QueryContext(ctx, query.cte+`
		SELECT COALESCE(NULLIF(TRIM(inbound_endpoint), ''), 'unknown'),
		       COALESCE(SUM(total_requests), 0),
		       COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0),
		       COALESCE(SUM(total_cost), 0), COALESCE(SUM(actual_cost), 0)
		FROM combined `+query.where+`
		GROUP BY 1 ORDER BY 2 DESC`, query.args...)
	if err != nil {
		return nil, false, err
	}
	// Release the acquired report rows after scanning or an early return. Close
	// errors are ignored here; query, scan, and iteration errors reject the result.
	defer func() { _ = rows.Close() }()
	results := make([]EndpointStat, 0)
	for rows.Next() {
		var row EndpointStat
		if err := rows.Scan(&row.Endpoint, &row.Requests, &row.TotalTokens, &row.Cost, &row.ActualCost); err != nil {
			return nil, false, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return results, true, nil
}

// getAPIKeyUsageTrendFromAnalytics returns date buckets for keys with the
// largest token totals in the range. Nonpositive limit uses twelve, and
// unavailable source coverage returns false for raw-query selection.
//
// The top-key CTE selects identities before the date grouping, so one set of
// keys is followed through the whole interval. Current key names are joined
// after that selection; request counts come from total_requests and tokens
// include cache creation/read. Timezone and limit parameters are appended after
// the source arguments, keeping their positions correct for hourly-only queries.
func (r *Store) getAPIKeyUsageTrendFromAnalytics(ctx context.Context, start, end time.Time, granularity string, limit int) ([]APIKeyUsageTrendPoint, bool, error) {
	query, ok, err := r.buildUsageAnalyticsQuery(ctx, UsageLogFilters{}, start, end, false)
	if err != nil || !ok {
		return nil, false, err
	}
	if limit <= 0 {
		limit = 12
	}
	query.args = append(query.args, r.resolveUsageStatsTimezone(), limit)
	timezonePosition := len(query.args) - 1
	limitPosition := len(query.args)
	rows, err := r.sql.QueryContext(ctx, query.cte+fmt.Sprintf(`,
		top_keys AS (
			SELECT api_key_id
			FROM combined
			GROUP BY api_key_id
			ORDER BY SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) DESC
			LIMIT $%d
		)
		SELECT
			TO_CHAR(c.occurred_at AT TIME ZONE $%d, '%s'),
			c.api_key_id,
			COALESCE(k.name, ''),
			COALESCE(SUM(c.total_requests), 0),
			COALESCE(SUM(c.input_tokens + c.output_tokens + c.cache_creation_tokens + c.cache_read_tokens), 0)
		FROM combined c
		LEFT JOIN api_keys k ON k.id = c.api_key_id
		WHERE c.api_key_id IN (SELECT api_key_id FROM top_keys)
		GROUP BY 1, c.api_key_id, k.name
		ORDER BY 1 ASC, 5 DESC`, limitPosition, timezonePosition, safeDateFormat(granularity)), query.args...)
	if err != nil {
		return nil, false, err
	}
	// Release the acquired report rows after scanning or an early return. Close
	// errors are ignored here; query, scan, and iteration errors reject the result.
	defer func() { _ = rows.Close() }()
	results := make([]APIKeyUsageTrendPoint, 0)
	for rows.Next() {
		var row APIKeyUsageTrendPoint
		if err := rows.Scan(&row.Date, &row.APIKeyID, &row.KeyName, &row.Requests, &row.Tokens); err != nil {
			return nil, false, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return results, true, nil
}

// getUserUsageTrendFromAnalytics returns date buckets for the most active
// billing users in the range. Nonpositive limit uses twelve; unavailable
// coverage or incompatible source construction returns false.
//
// Token totals select payer identities before grouping dates, combining team
// members whose billing_user_id is the owner. Current email and username come
// from users at query time, while actor identity stays in the source for other
// scopes. The appended named timezone determines display buckets, and
// cost/actual_cost remain separate base and charged amounts.
func (r *Store) getUserUsageTrendFromAnalytics(ctx context.Context, start, end time.Time, granularity string, limit int) ([]UserUsageTrendPoint, bool, error) {
	query, ok, err := r.buildUsageAnalyticsQuery(ctx, UsageLogFilters{}, start, end, false)
	if err != nil || !ok {
		return nil, false, err
	}
	if limit <= 0 {
		limit = 12
	}
	query.args = append(query.args, r.resolveUsageStatsTimezone(), limit)
	timezonePosition := len(query.args) - 1
	limitPosition := len(query.args)
	rows, err := r.sql.QueryContext(ctx, query.cte+fmt.Sprintf(`,
		top_users AS (
			SELECT billing_user_id AS user_id
			FROM combined
			-- 聚合表同时保留行为用户和付款主体，Top 用户按付款主体合并团队用量。
			GROUP BY billing_user_id
			ORDER BY SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) DESC
			LIMIT $%d
		)
		SELECT
			TO_CHAR(c.occurred_at AT TIME ZONE $%d, '%s'),
			c.billing_user_id AS user_id,
			COALESCE(u.email, ''),
			COALESCE(u.username, ''),
			COALESCE(SUM(c.total_requests), 0),
			COALESCE(SUM(c.input_tokens + c.output_tokens + c.cache_creation_tokens + c.cache_read_tokens), 0),
			COALESCE(SUM(c.total_cost), 0),
			COALESCE(SUM(c.actual_cost), 0)
		FROM combined c
		LEFT JOIN users u ON u.id = c.billing_user_id
		WHERE c.billing_user_id IN (SELECT user_id FROM top_users)
		GROUP BY 1, c.billing_user_id, u.email, u.username
		ORDER BY 1 ASC, 6 DESC`, limitPosition, timezonePosition, safeDateFormat(granularity)), query.args...)
	if err != nil {
		return nil, false, err
	}
	// Release the acquired report rows after scanning or an early return. Close
	// errors are ignored here; query, scan, and iteration errors reject the result.
	defer func() { _ = rows.Close() }()
	results := make([]UserUsageTrendPoint, 0)
	for rows.Next() {
		var row UserUsageTrendPoint
		if err := rows.Scan(&row.Date, &row.UserID, &row.Email, &row.Username, &row.Requests, &row.Tokens, &row.Cost, &row.ActualCost); err != nil {
			return nil, false, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return results, true, nil
}

// getUserSpendingRankingFromAnalytics returns payer spending ranks and
// full-range totals from a compatible combined source. It binds the supplied
// limit directly, leaving positive-limit policy to its public caller.
//
// Source rows merge by billing_user_id before ordering by user charges, tokens,
// and user ID. Window sums run before LIMIT, so limited ranking entries still
// carry totals for every payer. Current email and username are joined for
// display after selection, keeping identity changes independent of historical
// analytics buckets.
func (r *Store) getUserSpendingRankingFromAnalytics(ctx context.Context, start, end time.Time, limit int) (*UserSpendingRankingResponse, bool, error) {
	query, ok, err := r.buildUsageAnalyticsQuery(ctx, UsageLogFilters{}, start, end, true)
	if err != nil || !ok {
		return nil, false, err
	}
	query.args = append(query.args, limit)
	limitPosition := len(query.args)
	rows, err := r.sql.QueryContext(ctx, query.cte+fmt.Sprintf(`,
		user_spend AS (
			SELECT billing_user_id AS user_id,
			       COALESCE(SUM(actual_cost), 0) AS actual_cost,
			       COALESCE(SUM(total_requests), 0) AS requests,
			       COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS tokens
			FROM combined
			-- 聚合表同时保留行为用户和付款主体，排行榜按付款主体合并团队用量。
			GROUP BY billing_user_id
		),
		ranked AS (
			SELECT user_id, actual_cost, requests, tokens,
			       COALESCE(SUM(actual_cost) OVER (), 0) AS total_actual_cost,
			       COALESCE(SUM(requests) OVER (), 0) AS total_requests,
			       COALESCE(SUM(tokens) OVER (), 0) AS total_tokens
			FROM user_spend
			ORDER BY actual_cost DESC, tokens DESC, user_id ASC
			LIMIT $%d
		)
		SELECT r.user_id, COALESCE(u.email, ''), COALESCE(u.username, ''),
		       r.actual_cost, r.requests, r.tokens,
		       r.total_actual_cost, r.total_requests, r.total_tokens
		FROM ranked r
		LEFT JOIN users u ON u.id = r.user_id
		ORDER BY r.actual_cost DESC, r.tokens DESC, r.user_id ASC`, limitPosition), query.args...)
	if err != nil {
		return nil, false, err
	}
	// Release the acquired report rows after scanning or an early return. Close
	// errors are ignored here; query, scan, and iteration errors reject the result.
	defer func() { _ = rows.Close() }()
	result := &UserSpendingRankingResponse{Ranking: make([]UserSpendingRankingItem, 0)}
	for rows.Next() {
		var row UserSpendingRankingItem
		if err := rows.Scan(&row.UserID, &row.Email, &row.Username, &row.ActualCost, &row.Requests, &row.Tokens,
			&result.TotalActualCost, &result.TotalRequests, &result.TotalTokens); err != nil {
			return nil, false, err
		}
		result.Ranking = append(result.Ranking, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return result, true, nil
}

// usageRankingAnalyticsEligibility returns fixed positive eligibility SQL for
// the public metric. Invalid labels use the normalized total-token metric, while
// request and actual-cost metrics require their own summed value to be positive.
func usageRankingAnalyticsEligibility(sortBy usage.UsageRankingSortBy) string {
	switch usage.NormalizeUsageRankingSortBy(string(sortBy)) {
	case usage.UsageRankingSortByRequests:
		return "COALESCE(SUM(total_requests), 0) > 0"
	case usage.UsageRankingSortByActualCost:
		return "COALESCE(SUM(actual_cost), 0) > 0"
	default:
		return "COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) > 0"
	}
}

// getUsageRankingFromAnalytics returns eligible public payer rankings with
// complete population totals. Source availability is reported separately from
// execution errors, and the caller supplies the public limit policy.
//
// Payer grouping precedes positive eligibility and the configured ordering.
// Window totals cover all eligible rows before LIMIT, and ranked entries use the
// same fixed tie-breaking SQL as the raw implementation. Current avatar and
// identity rows supply display data; rankingDisplayName trims usernames and
// masks emails when a username is absent.
func (r *Store) getUsageRankingFromAnalytics(ctx context.Context, start, end time.Time, limit int, sortBy usage.UsageRankingSortBy) (*UsageRankingResponse, bool, error) {
	query, ok, err := r.buildUsageAnalyticsQuery(ctx, UsageLogFilters{}, start, end, true)
	if err != nil || !ok {
		return nil, false, err
	}
	query.args = append(query.args, limit)
	limitPosition := len(query.args)
	_, orderBy := usageRankingQueryParts(sortBy)
	eligibility := usageRankingAnalyticsEligibility(sortBy)
	rows, err := r.sql.QueryContext(ctx, query.cte+fmt.Sprintf(`,
		user_usage AS (
			SELECT billing_user_id AS user_id,
			       COALESCE(SUM(total_requests), 0) AS requests,
			       COALESCE(SUM(input_tokens), 0) AS input_tokens,
			       COALESCE(SUM(output_tokens), 0) AS output_tokens,
			       COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
			       COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
			       COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS total_tokens,
			       COALESCE(SUM(actual_cost), 0) AS actual_cost
			FROM combined
			-- 聚合表同时保留行为用户和付款主体，排行榜按付款主体合并团队用量。
			GROUP BY billing_user_id
			HAVING %s
		),
		ranked AS (
			SELECT ROW_NUMBER() OVER (ORDER BY %s) AS rank,
			       user_id, requests, input_tokens, output_tokens, cache_creation_tokens,
			       cache_read_tokens, total_tokens, actual_cost,
			       COALESCE(SUM(requests) OVER (), 0) AS total_requests,
			       COALESCE(SUM(total_tokens) OVER (), 0) AS ranking_total_tokens,
			       COALESCE(SUM(actual_cost) OVER (), 0) AS total_actual_cost
			FROM user_usage
			ORDER BY %s
			LIMIT $%d
		)
		SELECT r.rank, r.user_id, COALESCE(u.email, ''), COALESCE(u.username, ''),
		       COALESCE(a.url, ''), r.requests, r.input_tokens, r.output_tokens,
		       r.cache_creation_tokens, r.cache_read_tokens, r.total_tokens, r.actual_cost,
		       r.total_requests, r.ranking_total_tokens, r.total_actual_cost
		FROM ranked r
		LEFT JOIN users u ON u.id = r.user_id
		LEFT JOIN user_avatars a ON a.user_id = r.user_id
		ORDER BY r.rank ASC`, eligibility, orderBy, orderBy, limitPosition), query.args...)
	if err != nil {
		return nil, false, err
	}
	// Release the acquired report rows after scanning or an early return. Close
	// errors are ignored here; query, scan, and iteration errors reject the result.
	defer func() { _ = rows.Close() }()
	result := &UsageRankingResponse{Ranking: make([]UsageRankingItem, 0)}
	for rows.Next() {
		var row UsageRankingItem
		var email, username string
		if err := rows.Scan(
			&row.Rank, &row.UserID, &email, &username, &row.AvatarURL,
			&row.Requests, &row.InputTokens, &row.OutputTokens,
			&row.CacheCreationTokens, &row.CacheReadTokens, &row.TotalTokens,
			&row.ActualCost, &result.TotalRequests, &result.TotalTokens,
			&result.TotalActualCost,
		); err != nil {
			return nil, false, err
		}
		row.DisplayName = rankingDisplayName(username, email, row.UserID)
		result.Ranking = append(result.Ranking, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return result, true, nil
}

// getUserBreakdownStatsFromAnalytics ranks payer totals for dimensions
// represented by analytics. Provider/compaction filters, non-requested model
// filters, and non-inbound endpoint dimensions return false for raw-query
// handling.
//
// An acting-user filter still restricts source rows before billing_user_id
// grouping. The fixed sort allowlist selects a metric, with actual_cost for
// unknown labels; positive limits append a bound row cap and nonpositive limits
// leave results unlimited. Current email is joined for the grouped payer, while
// provider cost stays separate from the user-charge ordering.
func (r *Store) getUserBreakdownStatsFromAnalytics(ctx context.Context, start, end time.Time, dim usage.UserBreakdownDimension, limit int) ([]usage.UserBreakdownItem, bool, error) {
	modelSource := usage.NormalizeModelSource(dim.ModelType)
	if dim.ProviderID > 0 || dim.NativeCompactionV2 != nil || (dim.Model != "" && modelSource != usage.ModelSourceRequested) {
		return nil, false, nil
	}
	if dim.Endpoint != "" && dim.EndpointType != "" && dim.EndpointType != "inbound" {
		return nil, false, nil
	}
	filters := UsageLogFilters{
		UserID: dim.UserID, APIKeyID: dim.APIKeyID, GroupID: dim.GroupID,
		Model: dim.Model, ModelFilterSource: modelSource,
		RequestType: dim.RequestType, Stream: dim.Stream, BillingType: dim.BillingType,
	}
	query, ok, err := r.buildUsageAnalyticsQuery(ctx, filters, start, end, true)
	if err != nil || !ok {
		return nil, false, err
	}
	if dim.Endpoint != "" {
		query.args = append(query.args, dim.Endpoint)
		query.where = appendUsageAnalyticsCondition(query.where, fmt.Sprintf("inbound_endpoint = $%d", len(query.args)))
	}

	// Sort labels choose fixed metric identifiers. Unrecognized input keeps the
	// actual-charge ordering rather than becoming interpolated SQL column text.
	orderBy := "actual_cost"
	switch dim.SortBy {
	case "total_tokens", "input_tokens", "output_tokens", "cache_tokens", "requests", "cost", "actual_cost":
		orderBy = dim.SortBy
	}
	limitClause := ""
	if limit > 0 {
		query.args = append(query.args, limit)
		limitClause = fmt.Sprintf(" LIMIT $%d", len(query.args))
	}
	rows, err := r.sql.QueryContext(ctx, query.cte+`
		SELECT c.billing_user_id AS user_id, COALESCE(u.email, ''),
		       COALESCE(SUM(c.total_requests), 0) AS requests,
		       COALESCE(SUM(c.input_tokens), 0) AS input_tokens,
		       COALESCE(SUM(c.output_tokens), 0) AS output_tokens,
		       COALESCE(SUM(c.cache_creation_tokens + c.cache_read_tokens), 0) AS cache_tokens,
		       COALESCE(SUM(c.input_tokens + c.output_tokens + c.cache_creation_tokens + c.cache_read_tokens), 0) AS total_tokens,
		       COALESCE(SUM(c.total_cost), 0) AS cost,
		       COALESCE(SUM(c.actual_cost), 0) AS actual_cost,
		       COALESCE(SUM(c.provider_cost), 0) AS provider_cost
		FROM combined c
		LEFT JOIN users u ON u.id = c.billing_user_id `+query.where+`
		-- 管理员排行按付款主体归属，团队成员用团队 Key 的用量归到 Owner。
		GROUP BY c.billing_user_id, u.email
		ORDER BY `+orderBy+` DESC`+limitClause, query.args...)
	if err != nil {
		return nil, false, err
	}
	// Release the acquired report rows after scanning or an early return. Close
	// errors are ignored here; query, scan, and iteration errors reject the result.
	defer func() { _ = rows.Close() }()
	results := make([]usage.UserBreakdownItem, 0)
	for rows.Next() {
		var row usage.UserBreakdownItem
		if err := rows.Scan(
			&row.UserID, &row.Email, &row.Requests, &row.InputTokens,
			&row.OutputTokens, &row.CacheTokens, &row.TotalTokens,
			&row.Cost, &row.ActualCost, &row.ProviderCost,
		); err != nil {
			return nil, false, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return results, true, nil
}

// getAllGroupUsageSummaryFromAnalytics returns retained, today, and yesterday
// user charges for existing groups. Missing retained-source bounds or any
// unusable source returns false, and the caller supplies local midnight as
// todayStart.
//
// Three combined queries preserve their own argument positions. The total query
// first creates entries for existing groups, including soft-deleted rows and
// zero-cost groups; bounded
// today and calendar-subtracted yesterday sums then update matching entries.
// Unmatched historical group IDs are omitted from this current-group list. A
// failure at any stage discards partial results so the public caller can read
// one complete raw summary.
func (r *Store) getAllGroupUsageSummaryFromAnalytics(ctx context.Context, todayStart time.Time) ([]usage.GroupUsageSummary, bool, error) {
	var sourceOldest sql.NullTime
	if err := scanSingleRow(ctx, r.sql, `
		SELECT source_oldest_at
		FROM usage_analytics_aggregation_state
		WHERE id = 1
	`, nil, &sourceOldest); err != nil {
		return nil, false, err
	}
	if !sourceOldest.Valid {
		return nil, false, nil
	}
	now := time.Now().UTC()
	totalQuery, ok, err := r.buildUsageAnalyticsQuery(ctx, UsageLogFilters{}, sourceOldest.Time, now, true)
	if err != nil || !ok {
		return nil, false, err
	}
	todayQuery, ok, err := r.buildUsageAnalyticsQuery(ctx, UsageLogFilters{}, todayStart, now, false)
	if err != nil || !ok {
		return nil, false, err
	}
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	yesterdayQuery, ok, err := r.buildUsageAnalyticsQuery(ctx, UsageLogFilters{}, yesterdayStart, todayStart, false)
	if err != nil || !ok {
		return nil, false, err
	}

	rows, err := r.sql.QueryContext(ctx, totalQuery.cte+`
		SELECT g.id, COALESCE(SUM(c.actual_cost), 0)
		FROM groups g
		LEFT JOIN combined c ON c.group_id = g.id
		GROUP BY g.id
	`, totalQuery.args...)
	if err != nil {
		return nil, false, err
	}
	results := make([]usage.GroupUsageSummary, 0)
	byID := make(map[int64]int)
	for rows.Next() {
		var row usage.GroupUsageSummary
		if err := rows.Scan(&row.GroupID, &row.TotalCost); err != nil {
			// This partial total result is unusable. Release its rows before returning
			// the failure so the public caller can perform a complete raw recovery.
			_ = rows.Close()
			return nil, false, err
		}
		byID[row.GroupID] = len(results)
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		// Release the failed iteration before returning; partially populated group
		// summaries remain local and cannot enter the caller's recovery response.
		_ = rows.Close()
		return nil, false, err
	}
	// Complete and close the total scan before period queries, allowing transaction
	// executors to reuse their connection and rejecting a failed cleanup result.
	if err := rows.Close(); err != nil {
		return nil, false, err
	}

	applyPeriod := func(query usageAnalyticsQuery, apply func(*usage.GroupUsageSummary, float64)) error {
		rows, queryErr := r.sql.QueryContext(ctx, query.cte+`
			SELECT group_id, COALESCE(SUM(actual_cost), 0)
			FROM combined
			GROUP BY group_id
		`, query.args...)
		if queryErr != nil {
			return queryErr
		}
		// Release each period result after merging or an early error. A period's
		// failure discards the whole summary; Close errors are ignored by this helper.
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var groupID int64
			var cost float64
			if scanErr := rows.Scan(&groupID, &cost); scanErr != nil {
				return scanErr
			}
			if position, exists := byID[groupID]; exists {
				apply(&results[position], cost)
			}
		}
		return rows.Err()
	}
	if err := applyPeriod(todayQuery, func(row *usage.GroupUsageSummary, cost float64) {
		row.TodayCost = cost
	}); err != nil {
		return nil, false, err
	}
	if err := applyPeriod(yesterdayQuery, func(row *usage.GroupUsageSummary, cost float64) {
		row.YesterdayCost = cost
	}); err != nil {
		return nil, false, err
	}
	return results, true, nil
}

// appendUsageAnalyticsCondition appends one trusted SQL predicate to an optional
// WHERE clause. Empty where begins a WHERE clause; populated where receives AND,
// with condition text supplied unchanged by the query builder.
func appendUsageAnalyticsCondition(where, condition string) string {
	if where == "" {
		return "WHERE " + condition
	}
	return where + " AND " + condition
}
