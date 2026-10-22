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

// usage_log_repo_trend.go queries usage trends, distributions, and rankings
// through analytics, legacy buckets, or raw records.
// Usage distribution can group the internal request model, actual upstream
// model, or their mapping path without splitting one internal model by a client
// display prefix. Trends also select actor-visible scopes, while administrator
// rankings merge payer identities so team-member activity belongs to its billing
// owner.
//
// Multidimensional analytics combines covered UTC buckets and raw edges when
// supported. Raw recovery keeps each method's filters and grouping SQL, while a
// legacy global hourly/daily path uses its narrower selector set and dashboard
// buckets. Date formatting comes from a fixed whitelist, but raw trends use the
// SQL session timezone and analytics trends bind the resolved Calendar zone.
// Ranking totals are calculated before limiting entries, and every monetary
// result keeps base, charged, and provider amounts distinct.

// TrendDataPoint records one date bucket of requests, tokens, and monetary
// totals.
type TrendDataPoint = usage.TrendDataPoint

// ModelStat records requests, token buckets, and distinct costs for one model.
type ModelStat = usage.ModelStat

// UserUsageTrendPoint records date-bucket totals for a billing user with current
// identity.
type UserUsageTrendPoint = usage.UserUsageTrendPoint

type (
	// UserSpendingRankingItem records a payer spending rank with current identity
	// fields.
	UserSpendingRankingItem = usage.UserSpendingRankingItem
	// UserSpendingRankingResponse holds limited payer entries and complete
	// selected-range totals.
	UserSpendingRankingResponse = usage.UserSpendingRankingResponse
)

// APIKeyUsageTrendPoint records date-bucket requests and tokens with current key
// display names.
type APIKeyUsageTrendPoint = usage.APIKeyUsageTrendPoint

// GetAPIKeyUsageTrend returns date buckets for keys selected by range-wide token
// totals. The caller supplies a positive limit for consistent behavior:
// analytics defaults nonpositive values to twelve, while the raw branch binds
// the supplied limit.
//
// Top-key selection happens before date grouping, following the same selected
// keys across [startTime, endTime). Current key names provide display labels,
// and cache tokens participate in selection and bucket sums. Analytics uses the
// resolved named timezone; raw TO_CHAR uses the SQL session timezone. Failed
// analytics logs a warning before the complete raw query.
func (r *Store) GetAPIKeyUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) (results []APIKeyUsageTrendPoint, err error) {
	// Failed or unusable multidimensional analytics is discarded before the later
	// source executes, keeping partial aggregate data out of raw report totals.
	if aggregated, ok, aggregateErr := r.getAPIKeyUsageTrendFromAnalytics(ctx, startTime, endTime, granularity, limit); aggregateErr == nil && ok {
		return aggregated, nil
	} else if aggregateErr != nil {
		r.logUsageAnalyticsFallback("api_key_usage_trend", aggregateErr)
	}
	dateFormat := safeDateFormat(granularity)

	query := fmt.Sprintf(`
		WITH top_keys AS (
			SELECT api_key_id
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY api_key_id
			ORDER BY SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) DESC
			LIMIT $3
		)
		SELECT
			TO_CHAR(u.created_at, '%s') as date,
			u.api_key_id,
			COALESCE(k.name, '') as key_name,
			COUNT(*) as requests,
			COALESCE(SUM(u.input_tokens + u.output_tokens + u.cache_creation_tokens + u.cache_read_tokens), 0) as tokens
		FROM usage_logs u
		LEFT JOIN api_keys k ON u.api_key_id = k.id
		WHERE u.api_key_id IN (SELECT api_key_id FROM top_keys)
		  AND u.created_at >= $4 AND u.created_at < $5
		GROUP BY date, u.api_key_id, k.name
		ORDER BY date ASC, tokens DESC
	`, dateFormat)

	rows, err := r.sql.QueryContext(ctx, query, startTime, endTime, limit, startTime, endTime)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Keep an earlier query/scan error as the primary failure, while a successful
		// scan can still be rejected when closing its result rows fails.
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]APIKeyUsageTrendPoint, 0)
	for rows.Next() {
		var row APIKeyUsageTrendPoint
		if err = rows.Scan(&row.Date, &row.APIKeyID, &row.KeyName, &row.Requests, &row.Tokens); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

// GetUserUsageTrend returns date buckets for the most active billing users in
// [startTime, endTime). Positive limits are caller-selected; analytics defaults
// nonpositive values to twelve while the raw branch passes them to SQL directly.
//
// Both sources select payer identities by total tokens before date grouping,
// combining team members under their billing_user_id with historical actor
// recovery on raw rows. Current email and username provide display fields. Raw
// date grouping uses the SQL session zone, analytics uses the resolved named
// zone, and cost versus actual_cost retain base versus user-charge meanings.
func (r *Store) GetUserUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) (results []UserUsageTrendPoint, err error) {
	// Failed or unusable multidimensional analytics is discarded before the later
	// source executes, keeping partial aggregate data out of raw report totals.
	if aggregated, ok, aggregateErr := r.getUserUsageTrendFromAnalytics(ctx, startTime, endTime, granularity, limit); aggregateErr == nil && ok {
		return aggregated, nil
	} else if aggregateErr != nil {
		r.logUsageAnalyticsFallback("user_usage_trend", aggregateErr)
	}
	dateFormat := safeDateFormat(granularity)

	query := fmt.Sprintf(`
		WITH top_users AS (
			SELECT COALESCE(billing_user_id, user_id) AS user_id
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			-- 团队成员使用团队 Key 时，Top 用户应按付款主体合并用量。
			GROUP BY COALESCE(billing_user_id, user_id)
			ORDER BY SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) DESC
			LIMIT $3
		)
		SELECT
			TO_CHAR(u.created_at, '%s') as date,
			COALESCE(u.billing_user_id, u.user_id) AS user_id,
			COALESCE(us.email, '') as email,
			COALESCE(us.username, '') as username,
			COUNT(*) as requests,
			COALESCE(SUM(u.input_tokens + u.output_tokens + u.cache_creation_tokens + u.cache_read_tokens), 0) as tokens,
			COALESCE(SUM(u.total_cost), 0) as cost,
			COALESCE(SUM(u.actual_cost), 0) as actual_cost
		FROM usage_logs u
		LEFT JOIN users us ON COALESCE(u.billing_user_id, u.user_id) = us.id
		WHERE COALESCE(u.billing_user_id, u.user_id) IN (SELECT user_id FROM top_users)
		  AND u.created_at >= $4 AND u.created_at < $5
		GROUP BY date, COALESCE(u.billing_user_id, u.user_id), us.email, us.username
		ORDER BY date ASC, tokens DESC
	`, dateFormat)

	rows, err := r.sql.QueryContext(ctx, query, startTime, endTime, limit, startTime, endTime)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Keep an earlier query/scan error as the primary failure, while a successful
		// scan can still be rejected when closing its result rows fails.
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]UserUsageTrendPoint, 0)
	for rows.Next() {
		var row UserUsageTrendPoint
		if err = rows.Scan(&row.Date, &row.UserID, &row.Email, &row.Username, &row.Requests, &row.Tokens, &row.Cost, &row.ActualCost); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

// GetUserSpendingRanking returns payer spending ranks and full selected-range
// totals. Nonpositive limits choose twelve, and unusable or failed analytics
// causes a complete raw query.
//
// Raw rows group by billing_user_id with historical user_id recovery, so
// team-member records contribute to their payer. Window totals precede LIMIT and
// therefore cover every grouped payer even when only a few entries are returned.
// Current email and username are presentation data, and ordering breaks charge
// ties by tokens then user ID.
func (r *Store) GetUserSpendingRanking(ctx context.Context, startTime, endTime time.Time, limit int) (result *UserSpendingRankingResponse, err error) {
	if limit <= 0 {
		limit = 12
	}
	// Failed or unusable multidimensional analytics is discarded before the later
	// source executes, keeping partial aggregate data out of raw report totals.
	if aggregated, ok, aggregateErr := r.getUserSpendingRankingFromAnalytics(ctx, startTime, endTime, limit); aggregateErr == nil && ok {
		return aggregated, nil
	} else if aggregateErr != nil {
		r.logUsageAnalyticsFallback("user_spending_ranking", aggregateErr)
	}

	query := `
		WITH user_spend AS (
			SELECT
				COALESCE(u.billing_user_id, u.user_id) as user_id,
				COALESCE(us.email, '') as email,
				COALESCE(us.username, '') as username,
				COALESCE(SUM(u.actual_cost), 0) as actual_cost,
				COUNT(*) as requests,
				COALESCE(SUM(u.input_tokens + u.output_tokens + u.cache_creation_tokens + u.cache_read_tokens), 0) as tokens
			FROM usage_logs u
			LEFT JOIN users us ON COALESCE(u.billing_user_id, u.user_id) = us.id
			WHERE u.created_at >= $1 AND u.created_at < $2
			-- 排行按付款主体归属，团队成员使用团队 Key 时计入团队 Owner。
			GROUP BY COALESCE(u.billing_user_id, u.user_id), us.email, us.username
		),
		ranked AS (
			SELECT
				user_id,
				email,
				username,
				actual_cost,
				requests,
				tokens,
				COALESCE(SUM(actual_cost) OVER (), 0) as total_actual_cost,
				COALESCE(SUM(requests) OVER (), 0) as total_requests,
				COALESCE(SUM(tokens) OVER (), 0) as total_tokens
			FROM user_spend
			ORDER BY actual_cost DESC, tokens DESC, user_id ASC
			LIMIT $3
		)
		SELECT
			user_id,
			email,
			username,
			actual_cost,
			requests,
			tokens,
			total_actual_cost,
			total_requests,
			total_tokens
		FROM ranked
		ORDER BY actual_cost DESC, tokens DESC, user_id ASC
	`

	rows, err := r.sql.QueryContext(ctx, query, startTime, endTime, limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Keep an earlier query/scan error as the primary failure, while a successful
		// scan can still be rejected when closing its result rows fails.
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			result = nil
		}
	}()

	ranking := make([]UserSpendingRankingItem, 0)
	totalActualCost := 0.0
	totalRequests := int64(0)
	totalTokens := int64(0)
	for rows.Next() {
		var row UserSpendingRankingItem
		if err = rows.Scan(&row.UserID, &row.Email, &row.Username, &row.ActualCost, &row.Requests, &row.Tokens, &totalActualCost, &totalRequests, &totalTokens); err != nil {
			return nil, err
		}
		ranking = append(ranking, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	return &UserSpendingRankingResponse{
		Ranking:         ranking,
		TotalActualCost: totalActualCost,
		TotalRequests:   totalRequests,
		TotalTokens:     totalTokens,
	}, nil
}

// GetUserUsageTrendByUserID returns trend buckets for the actor and current
// owned-team scope. The half-open range and granularity pass through unchanged;
// the richer helper handles source availability and prevents duplicate owner
// rows.
func (r *Store) GetUserUsageTrendByUserID(ctx context.Context, userID int64, startTime, endTime time.Time, granularity string) (results []TrendDataPoint, err error) {
	return r.getUsageTrendWithFilters(ctx, startTime, endTime, granularity, UsageLogFilters{UserID: userID, IncludeOwnedTeam: true})
}

// GetUserModelStats groups actor and current owned-team records by internal
// request model. The caller authorizes that scope and supplies a half-open
// range; current client display prefixes remain in detail records rather than
// model grouping.
func (r *Store) GetUserModelStats(ctx context.Context, userID int64, startTime, endTime time.Time) (results []ModelStat, err error) {
	return r.getModelStatsWithFiltersBySource(ctx, startTime, endTime, UsageLogFilters{UserID: userID, IncludeOwnedTeam: true}, usage.ModelSourceRequested)
}

// GetUsageTrendWithFilters returns buckets over a half-open range with the base
// scalar selectors. Zero IDs and nil optional selectors leave dimensions
// unrestricted; richer team/personal/request-ID scopes use the unified-filter
// entry point.
func (r *Store) GetUsageTrendWithFilters(ctx context.Context, startTime, endTime time.Time, granularity string, userID, apiKeyID, providerID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) (results []TrendDataPoint, err error) {
	return r.getUsageTrendWithFilters(ctx, startTime, endTime, granularity, UsageLogFilters{
		UserID:      userID,
		APIKeyID:    apiKeyID,
		ProviderID:  providerID,
		GroupID:     groupID,
		Model:       model,
		RequestType: requestType,
		Stream:      stream,
		BillingType: billingType,
	})
}

// GetUsageTrendWithUsageFilters forwards the unified selectors with its supplied
// range and granularity. StartTime/EndTime fields inside filters do not replace
// those explicit arguments; the helper chooses analytics, legacy buckets, or raw
// SQL.
func (r *Store) GetUsageTrendWithUsageFilters(ctx context.Context, startTime, endTime time.Time, granularity string, filters UsageLogFilters) (results []TrendDataPoint, err error) {
	return r.getUsageTrendWithFilters(ctx, startTime, endTime, granularity, filters)
}

// getUsageTrendWithFilters returns usage buckets after source selection for the
// supplied range and selectors. RequestID is trimmed before querying;
// compatible analytics is preferred, with failed attempts warning before later
// query paths.
//
// A global legacy hour/day check considers the selectors in
// shouldUsePreaggregatedTrend and a separate zero-team condition. Request ID is
// applied when raw SQL executes, but is absent from that legacy eligibility
// check. Raw actor/team branches stay disjoint, RequestType precedes Stream,
// and dates use the SQL session zone. A request-ID-only query can consequently
// return global legacy buckets when they are nonempty.
func (r *Store) getUsageTrendWithFilters(ctx context.Context, startTime, endTime time.Time, granularity string, filters UsageLogFilters) (results []TrendDataPoint, err error) {
	requestID := strings.TrimSpace(filters.RequestID)

	analyticsFilters := filters
	analyticsFilters.RequestID = requestID
	// Failed or unusable multidimensional analytics is discarded before the later
	// source executes, keeping partial aggregate data out of raw report totals.
	if aggregated, ok, aggregateErr := r.getUsageTrendFromAnalytics(ctx, startTime, endTime, granularity, analyticsFilters); aggregateErr == nil && ok {
		return aggregated, nil
	} else if aggregateErr != nil {
		r.logUsageAnalyticsFallback("usage_trend", aggregateErr)
	}
	// Legacy buckets use this represented-selector check plus filters.TeamID. Request
	// identity is absent from that check and reaches the raw predicate only if raw
	// SQL executes.
	if filters.TeamID == 0 && shouldUsePreaggregatedTrend(granularity, filters) {
		// Legacy bucket errors or empty results move to raw SQL; only a nonempty
		// successful bucket result ends this path before raw filtering.
		aggregated, aggregatedErr := r.getUsageTrendFromAggregates(ctx, startTime, endTime, granularity)
		if aggregatedErr == nil && len(aggregated) > 0 {
			return aggregated, nil
		}
	}

	dateFormat := safeDateFormat(granularity)
	args := []any{startTime, endTime}
	source, scopeCondition, args, scopeErr := r.buildUsageLogScopeSource(ctx, args, filters.UserID, filters.IncludeOwnedTeam, "")
	if scopeErr != nil {
		return nil, scopeErr
	}

	query := fmt.Sprintf(`
		SELECT
			TO_CHAR(created_at, '%s') as date,
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(total_cost), 0) as cost,
			COALESCE(SUM(actual_cost), 0) as actual_cost
		FROM %s
		WHERE created_at >= $1 AND created_at < $2
	`, dateFormat, source)

	if requestID != "" {
		query += " AND " + usageRequestIDCondition("", len(args)+1)
		args = append(args, requestID)
	}

	if scopeCondition != "" {
		query += " AND " + scopeCondition
	}
	if filters.APIKeyID > 0 {
		query += fmt.Sprintf(" AND api_key_id = $%d", len(args)+1)
		args = append(args, filters.APIKeyID)
	}
	if filters.ProviderID > 0 {
		query += fmt.Sprintf(" AND provider_id = $%d", len(args)+1)
		args = append(args, filters.ProviderID)
	}
	if filters.GroupID > 0 {
		query += fmt.Sprintf(" AND group_id = $%d", len(args)+1)
		args = append(args, filters.GroupID)
	}
	if filters.TeamID > 0 {
		query += fmt.Sprintf(" AND team_id = $%d", len(args)+1)
		args = append(args, filters.TeamID)
	}
	if filters.PersonalOnly {
		query += " AND team_id IS NULL"
	}
	query, args = appendUsageLogModelQueryFilter(query, args, filters.Model, filters.ModelFilterSource)
	query, args = appendRequestTypeOrStreamQueryFilter(query, args, filters.RequestType, filters.Stream)
	query, args = appendNativeCompactionV2QueryFilter(query, args, filters.NativeCompactionV2, "")
	if filters.BillingType != nil {
		query += fmt.Sprintf(" AND billing_type = $%d", len(args)+1)
		args = append(args, int16(*filters.BillingType))
	}
	query, args = appendUsageLogBillingModeQueryFilter(query, args, filters.BillingMode, "")
	query += " GROUP BY date ORDER BY date ASC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Keep an earlier query/scan error as the primary failure, while a successful
		// scan can still be rejected when closing its result rows fails.
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results, err = scanTrendRows(rows)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// shouldUsePreaggregatedTrend checks the represented selectors for legacy global
// hour/day eligibility. It returns true only when those selectors are absent;
// team and request identity are excluded from this check. The caller checks
// team separately and applies request identity only on its raw path.
func shouldUsePreaggregatedTrend(granularity string, filters UsageLogFilters) bool {
	if granularity != "day" && granularity != "hour" {
		return false
	}
	return !filters.PersonalOnly &&
		!filters.IncludeOwnedTeam &&
		filters.UserID == 0 &&
		filters.APIKeyID == 0 &&
		filters.ProviderID == 0 &&
		filters.GroupID == 0 &&
		filters.Model == "" &&
		filters.RequestType == nil &&
		filters.Stream == nil &&
		filters.BillingType == nil &&
		filters.BillingMode == "" &&
		filters.NativeCompactionV2 == nil
}

// getUsageTrendFromAggregates returns legacy global hour/day dashboard buckets
// in the supplied range. Bound settings disabling usage return nil; unsupported
// granularities also return nil, and this reader performs no multidimensional
// coverage validation.
func (r *Store) getUsageTrendFromAggregates(ctx context.Context, startTime, endTime time.Time, granularity string) (results []TrendDataPoint, err error) {
	if r.preAggregation != nil && !r.preAggregation.UsageEnabled(ctx) {
		return nil, nil
	}
	dateFormat := safeDateFormat(granularity)
	query := ""
	args := []any{startTime, endTime}

	switch granularity {
	case "hour":
		query = fmt.Sprintf(`
			SELECT
				TO_CHAR(bucket_start, '%s') as date,
				total_requests as requests,
				input_tokens,
				output_tokens,
				cache_creation_tokens,
				cache_read_tokens,
				(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) as total_tokens,
				total_cost as cost,
				actual_cost
			FROM usage_dashboard_hourly
			WHERE bucket_start >= $1 AND bucket_start < $2
			ORDER BY bucket_start ASC
		`, dateFormat)
	case "day":
		query = fmt.Sprintf(`
			SELECT
				TO_CHAR(bucket_date::timestamp, '%s') as date,
				total_requests as requests,
				input_tokens,
				output_tokens,
				cache_creation_tokens,
				cache_read_tokens,
				(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) as total_tokens,
				total_cost as cost,
				actual_cost
			FROM usage_dashboard_daily
			WHERE bucket_date >= $1::date AND bucket_date < $2::date
			ORDER BY bucket_date ASC
		`, dateFormat)
	default:
		return nil, nil
	}

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Keep an earlier query/scan error as the primary failure, while a successful
		// scan can still be rejected when closing its result rows fails.
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results, err = scanTrendRows(rows)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// GetModelStatsWithFilters returns internal-request-model groups for base
// selectors and a half-open range. Nil transport selectors leave them
// unrestricted, and model dimensions or richer scope selectors use the
// corresponding source/unified entry points.
func (r *Store) GetModelStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, requestType *int16, stream *bool, billingType *int8) (results []ModelStat, err error) {
	return r.getModelStatsWithFiltersBySource(ctx, startTime, endTime, UsageLogFilters{
		UserID:      userID,
		APIKeyID:    apiKeyID,
		ProviderID:  providerID,
		GroupID:     groupID,
		RequestType: requestType,
		Stream:      stream,
		BillingType: billingType,
	}, usage.ModelSourceRequested)
}

// GetModelStatsWithFiltersBySource returns base-filter model groups using
// requested, upstream, or mapping dimension. Invalid source labels normalize to
// requested, and the caller authorizes the selected IDs before reading the
// supplied half-open range.
func (r *Store) GetModelStatsWithFiltersBySource(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, requestType *int16, stream *bool, billingType *int8, source string) (results []ModelStat, err error) {
	return r.getModelStatsWithFiltersBySource(ctx, startTime, endTime, UsageLogFilters{
		UserID:      userID,
		APIKeyID:    apiKeyID,
		ProviderID:  providerID,
		GroupID:     groupID,
		RequestType: requestType,
		Stream:      stream,
		BillingType: billingType,
	}, source)
}

// GetModelStatsWithUsageFiltersBySource returns unified-filter model groups
// using the explicit source argument. That source governs both grouping and
// filters.Model matching; explicit start/end arguments govern the half-open
// range.
func (r *Store) GetModelStatsWithUsageFiltersBySource(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters, source string) (results []ModelStat, err error) {
	return r.getModelStatsWithFiltersBySource(ctx, startTime, endTime, filters, source)
}

// getModelStatsWithFiltersBySource groups usage by the selected internal,
// upstream, or mapping expression. The source argument governs both grouping
// and model matching, and compatible requested-model analytics can supply the
// complete result.
//
// Raw scope construction keeps actor and owned-team branches disjoint. Model
// equality uses the same expression as grouping, while RequestType takes
// precedence over Stream and other selectors add bound restrictions. TotalCost
// and ActualCost remain base and user charges; provider expense is computed
// separately from its recorded base/multiplier. Scan, iteration, or
// successful-path cleanup failures discard the result.
func (r *Store) getModelStatsWithFiltersBySource(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters, source string) (results []ModelStat, err error) {
	requestID := strings.TrimSpace(filters.RequestID)

	analyticsFilters := filters
	analyticsFilters.RequestID = requestID
	analyticsFilters.ModelFilterSource = source
	// Failed or unusable multidimensional analytics is discarded before the later
	// source executes, keeping partial aggregate data out of raw report totals.
	if aggregated, ok, aggregateErr := r.getModelStatsFromAnalytics(ctx, startTime, endTime, analyticsFilters); aggregateErr == nil && ok {
		return aggregated, nil
	} else if aggregateErr != nil {
		r.logUsageAnalyticsFallback("model_stats", aggregateErr)
	}
	// Filtering by execution provider keeps user charges in actual_cost. Compute
	// provider expense in its separate column from the recorded base and
	// multiplier.
	providerCostExpr := "COALESCE(SUM(COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1)), 0) as provider_cost"
	modelExpr := resolveModelDimensionExpression(source)
	args := []any{startTime, endTime}
	usageSource, scopeCondition, args, scopeErr := r.buildUsageLogScopeSource(ctx, args, filters.UserID, filters.IncludeOwnedTeam, "")
	if scopeErr != nil {
		return nil, scopeErr
	}

	query := fmt.Sprintf(`
		SELECT
			%s as model,
			COUNT(*) as requests,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) as cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(total_cost), 0) as cost,
			COALESCE(SUM(actual_cost), 0) as actual_cost,
			%s
		FROM %s
		WHERE created_at >= $1 AND created_at < $2
	`, modelExpr, providerCostExpr, usageSource)

	if requestID != "" {
		query += " AND " + usageRequestIDCondition("", len(args)+1)
		args = append(args, requestID)
	}

	if scopeCondition != "" {
		query += " AND " + scopeCondition
	}
	if filters.APIKeyID > 0 {
		query += fmt.Sprintf(" AND api_key_id = $%d", len(args)+1)
		args = append(args, filters.APIKeyID)
	}
	if filters.ProviderID > 0 {
		query += fmt.Sprintf(" AND provider_id = $%d", len(args)+1)
		args = append(args, filters.ProviderID)
	}
	if filters.GroupID > 0 {
		query += fmt.Sprintf(" AND group_id = $%d", len(args)+1)
		args = append(args, filters.GroupID)
	}
	if filters.TeamID > 0 {
		query += fmt.Sprintf(" AND team_id = $%d", len(args)+1)
		args = append(args, filters.TeamID)
	}
	if filters.PersonalOnly {
		query += " AND team_id IS NULL"
	}
	if strings.TrimSpace(filters.Model) != "" {
		query += fmt.Sprintf(" AND %s = $%d", modelExpr, len(args)+1)
		args = append(args, filters.Model)
	}
	query, args = appendRequestTypeOrStreamQueryFilter(query, args, filters.RequestType, filters.Stream)
	query, args = appendNativeCompactionV2QueryFilter(query, args, filters.NativeCompactionV2, "")
	if filters.BillingType != nil {
		query += fmt.Sprintf(" AND billing_type = $%d", len(args)+1)
		args = append(args, int16(*filters.BillingType))
	}
	query, args = appendUsageLogBillingModeQueryFilter(query, args, filters.BillingMode, "")
	query += fmt.Sprintf(" GROUP BY %s ORDER BY total_tokens DESC", modelExpr)

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Keep an earlier query/scan error as the primary failure, while a successful
		// scan can still be rejected when closing its result rows fails.
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results, err = scanModelStatsRows(rows)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// GetGroupStatsWithFilters returns group totals for the base selectors over a
// half-open range. Zero IDs and nil selectors leave dimensions unrestricted;
// current group names supply presentation labels and absent groups use zero
// identity.
func (r *Store) GetGroupStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, providerID, groupID int64, requestType *int16, stream *bool, billingType *int8) (results []usage.GroupStat, err error) {
	return r.getGroupStatsWithFilters(ctx, startTime, endTime, UsageLogFilters{
		UserID:      userID,
		APIKeyID:    apiKeyID,
		ProviderID:  providerID,
		GroupID:     groupID,
		RequestType: requestType,
		Stream:      stream,
		BillingType: billingType,
	})
}

// GetGroupStatsWithUsageFilters returns group totals with unified ownership and
// diagnostic selectors. The Model value selects the internal request-model
// dimension, while explicit start/end arguments supply the half-open range for
// this query.
func (r *Store) GetGroupStatsWithUsageFilters(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters) (results []usage.GroupStat, err error) {
	return r.getGroupStatsWithFilters(ctx, startTime, endTime, filters)
}

// getGroupStatsWithFilters returns routed-group totals for the supplied
// ownership and diagnostic selectors. It tries compatible requested-model
// analytics first, then executes a complete raw query when unavailable or
// failed.
//
// The raw actor/team source remains disjoint and uses its explicit ul alias for
// group joins. Model equality selects the internal requested dimension, and
// current localized names participate in grouping when user presentation
// requests them. Requests/tokens, base cost, user charges, and provider expense
// remain distinct. An absent group ID becomes zero, and a trimmed RequestID
// reaches raw lookup. The model filter source is the requested dimension.
func (r *Store) getGroupStatsWithFilters(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters) (results []usage.GroupStat, err error) {
	requestID := strings.TrimSpace(filters.RequestID)

	analyticsFilters := filters
	analyticsFilters.RequestID = requestID
	analyticsFilters.ModelFilterSource = usage.ModelSourceRequested
	// Failed or unusable multidimensional analytics is discarded before the later
	// source executes, keeping partial aggregate data out of raw report totals.
	if aggregated, ok, aggregateErr := r.getGroupStatsFromAnalytics(ctx, startTime, endTime, analyticsFilters); aggregateErr == nil && ok {
		return aggregated, nil
	} else if aggregateErr != nil {
		r.logUsageAnalyticsFallback("group_stats", aggregateErr)
	}
	args := []any{startTime, endTime}
	usageSource, scopeCondition, args, scopeErr := r.buildUsageLogScopeSource(ctx, args, filters.UserID, filters.IncludeOwnedTeam, "ul")
	if scopeErr != nil {
		return nil, scopeErr
	}
	query := fmt.Sprintf(`
		SELECT
			COALESCE(ul.group_id, 0) as group_id,
			`+postgresinfra.LocalizedTextExpression(ctx, "g.localization", "display_name", "COALESCE(g.name, '')")+` as group_name,
			COUNT(*) as requests,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(ul.total_cost), 0) as cost,
			COALESCE(SUM(ul.actual_cost), 0) as actual_cost,
			COALESCE(SUM(COALESCE(ul.provider_stats_cost, ul.total_cost) * COALESCE(ul.provider_rate_multiplier, 1)), 0) as provider_cost
		FROM %s
		LEFT JOIN groups g ON g.id = ul.group_id
		WHERE ul.created_at >= $1 AND ul.created_at < $2
	`, usageSource)

	if requestID != "" {
		query += " AND " + usageRequestIDCondition("ul.", len(args)+1)
		args = append(args, requestID)
	}

	if scopeCondition != "" {
		query += " AND " + scopeCondition
	}
	if filters.APIKeyID > 0 {
		query += fmt.Sprintf(" AND ul.api_key_id = $%d", len(args)+1)
		args = append(args, filters.APIKeyID)
	}
	if filters.ProviderID > 0 {
		query += fmt.Sprintf(" AND ul.provider_id = $%d", len(args)+1)
		args = append(args, filters.ProviderID)
	}
	if filters.GroupID > 0 {
		query += fmt.Sprintf(" AND ul.group_id = $%d", len(args)+1)
		args = append(args, filters.GroupID)
	}
	if filters.TeamID > 0 {
		query += fmt.Sprintf(" AND ul.team_id = $%d", len(args)+1)
		args = append(args, filters.TeamID)
	}
	if filters.PersonalOnly {
		query += " AND ul.team_id IS NULL"
	}
	if strings.TrimSpace(filters.Model) != "" {
		modelExpr := resolveModelDimensionExpressionWithAlias(usage.ModelSourceRequested, "ul")
		query += fmt.Sprintf(" AND %s = $%d", modelExpr, len(args)+1)
		args = append(args, filters.Model)
	}
	query, args = appendRequestTypeOrStreamQueryFilter(query, args, filters.RequestType, filters.Stream)
	query, args = appendNativeCompactionV2QueryFilter(query, args, filters.NativeCompactionV2, "ul")
	if filters.BillingType != nil {
		query += fmt.Sprintf(" AND ul.billing_type = $%d", len(args)+1)
		args = append(args, int16(*filters.BillingType))
	}
	query, args = appendUsageLogBillingModeQueryFilter(query, args, filters.BillingMode, "ul")
	query += " GROUP BY ul.group_id, g.name"
	if locale.UserPresentation(ctx) {
		query += ", g.localization"
	}
	query += " ORDER BY total_tokens DESC"

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Keep an earlier query/scan error as the primary failure, while a successful
		// scan can still be rejected when closing its result rows fails.
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]usage.GroupStat, 0)
	for rows.Next() {
		var row usage.GroupStat
		if err := rows.Scan(
			&row.GroupID,
			&row.GroupName,
			&row.Requests,
			&row.TotalTokens,
			&row.Cost,
			&row.ActualCost,
			&row.ProviderCost,
		); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// GetUserBreakdownStats ranks billing-user totals inside the supplied half-open
// range and dimensional restrictions. A positive limit caps rows; unknown sort
// labels choose actual_cost, and nonpositive limits leave the result
// unrestricted.
//
// Compatible analytics supplies represented dimensions, while raw SQL also
// supports provider and upstream/path selectors. dim.UserID filters actors
// before payer grouping, so a member's selected activity can appear under its
// owner. RequestType and Stream both apply when supplied here. Current email
// belongs to the payer, and base cost, user charges, and provider expense retain
// separate result columns.
func (r *Store) GetUserBreakdownStats(ctx context.Context, startTime, endTime time.Time, dim usage.UserBreakdownDimension, limit int) (results []usage.UserBreakdownItem, err error) {
	// Failed or unusable multidimensional analytics is discarded before the later
	// source executes, keeping partial aggregate data out of raw report totals.
	if aggregated, ok, aggregateErr := r.getUserBreakdownStatsFromAnalytics(ctx, startTime, endTime, dim, limit); aggregateErr == nil && ok {
		return aggregated, nil
	} else if aggregateErr != nil {
		r.logUsageAnalyticsFallback("user_breakdown_stats", aggregateErr)
	}
	query := `
		SELECT
			COALESCE(ul.billing_user_id, ul.user_id, 0) as user_id,
			COALESCE(u.email, '') as email,
			COUNT(*) as requests,
			COALESCE(SUM(ul.input_tokens), 0) as input_tokens,
			COALESCE(SUM(ul.output_tokens), 0) as output_tokens,
			COALESCE(SUM(ul.cache_creation_tokens + ul.cache_read_tokens), 0) as cache_tokens,
			COALESCE(SUM(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens), 0) as total_tokens,
			COALESCE(SUM(ul.total_cost), 0) as cost,
			COALESCE(SUM(ul.actual_cost), 0) as actual_cost,
			COALESCE(SUM(COALESCE(ul.provider_stats_cost, ul.total_cost) * COALESCE(ul.provider_rate_multiplier, 1)), 0) as provider_cost
		FROM usage_logs ul
		LEFT JOIN users u ON u.id = COALESCE(ul.billing_user_id, ul.user_id)
		WHERE ul.created_at >= $1 AND ul.created_at < $2
	`
	args := []any{startTime, endTime}

	if dim.GroupID > 0 {
		query += fmt.Sprintf(" AND ul.group_id = $%d", len(args)+1)
		args = append(args, dim.GroupID)
	}
	if dim.Model != "" {
		query += fmt.Sprintf(" AND %s = $%d", resolveModelDimensionExpression(dim.ModelType), len(args)+1)
		args = append(args, dim.Model)
	}
	if dim.Endpoint != "" {
		col := resolveEndpointColumn(dim.EndpointType)
		query += fmt.Sprintf(" AND %s = $%d", col, len(args)+1)
		args = append(args, dim.Endpoint)
	}
	if dim.UserID > 0 {
		query += fmt.Sprintf(" AND ul.user_id = $%d", len(args)+1)
		args = append(args, dim.UserID)
	}
	if dim.APIKeyID > 0 {
		query += fmt.Sprintf(" AND ul.api_key_id = $%d", len(args)+1)
		args = append(args, dim.APIKeyID)
	}
	if dim.ProviderID > 0 {
		query += fmt.Sprintf(" AND ul.provider_id = $%d", len(args)+1)
		args = append(args, dim.ProviderID)
	}
	if dim.RequestType != nil {
		condition, conditionArgs := buildRequestTypeFilterConditionWithAlias(len(args)+1, *dim.RequestType, "ul")
		query += " AND " + condition
		args = append(args, conditionArgs...)
	}
	if dim.Stream != nil {
		query += fmt.Sprintf(" AND ul.stream = $%d", len(args)+1)
		args = append(args, *dim.Stream)
	}
	query, args = appendNativeCompactionV2QueryFilter(query, args, dim.NativeCompactionV2, "ul")
	if dim.BillingType != nil {
		query += fmt.Sprintf(" AND ul.billing_type = $%d", len(args)+1)
		args = append(args, *dim.BillingType)
	}

	// Requested sort labels select fixed output metrics; any other label retains
	// actual-charge ordering and cannot introduce SQL identifier text.
	orderBy := "actual_cost"
	switch dim.SortBy {
	case "total_tokens", "input_tokens", "output_tokens", "cache_tokens", "requests", "cost", "actual_cost":
		orderBy = dim.SortBy
	}
	// Actor restrictions are already applied. Group the surviving rows by payer so
	// selected team-member activity remains attributed to its billing owner.
	query += " GROUP BY COALESCE(ul.billing_user_id, ul.user_id, 0), u.email ORDER BY " + orderBy + " DESC"
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Keep an earlier query/scan error as the primary failure, while a successful
		// scan can still be rejected when closing its result rows fails.
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]usage.UserBreakdownItem, 0)
	for rows.Next() {
		var row usage.UserBreakdownItem
		if err := rows.Scan(
			&row.UserID,
			&row.Email,
			&row.Requests,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CacheTokens,
			&row.TotalTokens,
			&row.Cost,
			&row.ActualCost,
			&row.ProviderCost,
		); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// GetAllGroupUsageSummary returns existing groups with retained, today, and
// yesterday user charges. Callers supply local midnight as todayStart; usable
// analytics uses bounded current-time queries before raw recovery.
//
// Raw SQL keeps every existing group through a left join, including zero-usage
// groups and soft-deleted rows still present in the groups table. Yesterday
// starts one calendar date before todayStart and ends at todayStart, while raw
// today and retained totals have no upper bound. Summed actual_cost remains
// user charges even though the response calls these fields Cost, and historical
// records for absent groups are omitted.
func (r *Store) GetAllGroupUsageSummary(ctx context.Context, todayStart time.Time) ([]usage.GroupUsageSummary, error) {
	// Failed or unusable multidimensional analytics is discarded before the later
	// source executes, keeping partial aggregate data out of raw report totals.
	if aggregated, ok, aggregateErr := r.getAllGroupUsageSummaryFromAnalytics(ctx, todayStart); aggregateErr == nil && ok {
		return aggregated, nil
	} else if aggregateErr != nil {
		r.logUsageAnalyticsFallback("all_group_usage_summary", aggregateErr)
	}
	yesterdayStart := todayStart.AddDate(0, 0, -1)
	query := `
		SELECT
			g.id AS group_id,
			COALESCE(SUM(ul.actual_cost), 0) AS total_cost,
			COALESCE(SUM(CASE WHEN ul.created_at >= $1 THEN ul.actual_cost ELSE 0 END), 0) AS today_cost,
			COALESCE(SUM(CASE WHEN ul.created_at >= $2 AND ul.created_at < $1 THEN ul.actual_cost ELSE 0 END), 0) AS yesterday_cost
		FROM groups g
		LEFT JOIN usage_logs ul ON ul.group_id = g.id
		GROUP BY g.id
	`

	rows, err := r.sql.QueryContext(ctx, query, todayStart, yesterdayStart)
	if err != nil {
		return nil, err
	}
	// Release group-summary rows on completion or an early error. This helper
	// ignores Close errors, while scan and iteration failures reject the result.
	defer func() { _ = rows.Close() }()
	var results []usage.GroupUsageSummary
	for rows.Next() {
		var row usage.GroupUsageSummary
		if err := rows.Scan(&row.GroupID, &row.TotalCost, &row.TodayCost, &row.YesterdayCost); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// resolveModelDimensionExpression returns the fixed unqualified SQL expression
// for a model dimension. Invalid labels normalize to requested, and callers use
// the aliased helper when joins require qualified stored columns.
func resolveModelDimensionExpression(modelType string) string {
	return resolveModelDimensionExpressionWithAlias(modelType, "")
}

// resolveModelDimensionExpressionWithAlias returns a fixed model expression
// using the trusted alias supplied by its caller. Unknown dimension labels
// normalize to requested; alias text itself is not validated here.
//
// Requested grouping prefers trimmed internal model, then trimmed historical
// requested_model, then empty text, so client prefixes do not split a recorded
// internal model. Upstream grouping prefers trimmed upstream_model and otherwise
// retains stored model. Mapping joins those two representations into one label,
// keeping the request and execution evidence paired for model-distribution
// readers.
func resolveModelDimensionExpressionWithAlias(modelType, alias string) string {
	column := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	// Prefer internal request model before historical display text, keeping a
	// client composite-key prefix from separating rows that share one internal
	// model identity.
	requestedExpr := fmt.Sprintf(
		"COALESCE(NULLIF(TRIM(%s), ''), NULLIF(TRIM(%s), ''), '')",
		column("model"),
		column("requested_model"),
	)
	// Historical rows can lack upstream_model. Keep their available stored model
	// text so upstream and mapping distributions still have a label for those rows.
	upstreamExpr := fmt.Sprintf("COALESCE(NULLIF(TRIM(%s), ''), %s)", column("upstream_model"), column("model"))
	switch usage.NormalizeModelSource(modelType) {
	case usage.ModelSourceUpstream:
		return upstreamExpr
	case usage.ModelSourceMapping:
		return fmt.Sprintf("(%s || ' -> ' || %s)", requestedExpr, upstreamExpr)
	default:
		return requestedExpr
	}
}

// scanTrendRows decodes the nine-column trend shape into an allocated slice. The
// caller owns and closes rows; scan or iteration errors discard the partial
// result before it can enter a report.
func scanTrendRows(rows *sql.Rows) ([]TrendDataPoint, error) {
	results := make([]TrendDataPoint, 0)
	for rows.Next() {
		var row TrendDataPoint
		if err := rows.Scan(
			&row.Date,
			&row.Requests,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CacheCreationTokens,
			&row.CacheReadTokens,
			&row.TotalTokens,
			&row.Cost,
			&row.ActualCost,
		); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// scanModelStatsRows decodes model totals with separate base, charged, and
// provider amounts. The caller owns row cleanup, while scan or iteration errors
// discard the partially collected slice.
func scanModelStatsRows(rows *sql.Rows) ([]ModelStat, error) {
	results := make([]ModelStat, 0)
	for rows.Next() {
		var row ModelStat
		if err := rows.Scan(
			&row.Model,
			&row.Requests,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CacheCreationTokens,
			&row.CacheReadTokens,
			&row.TotalTokens,
			&row.Cost,
			&row.ActualCost,
			&row.ProviderCost,
		); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
