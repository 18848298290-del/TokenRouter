package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// TestResolveUsageAnalyticsWindowUsesCoveredMiddle checks that the unbackfilled head uses raw records.
func TestResolveUsageAnalyticsWindowUsesCoveredMiddle(t *testing.T) {
	db, mock := newSQLMock(t)
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{sql: db, preAggregation: settings}
	start := time.Date(2026, 8, 1, 10, 15, 0, 0, time.UTC)
	end := time.Date(2026, 8, 4, 4, 30, 0, 0, time.UTC)
	coverage := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	watermark := time.Date(2026, 8, 4, 4, 25, 0, 0, time.UTC)
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(watermark, coverage))

	window, ok, err := repo.resolveUsageAnalyticsWindow(context.Background(), start, end)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, start, window.start)
	require.Equal(t, coverage, window.aggregateStart)
	require.Equal(t, time.Date(2026, 8, 4, 5, 0, 0, 0, time.UTC), window.aggregateEnd)
	require.Equal(t, watermark, window.rawTailStart)
	require.Equal(t, time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), window.dailyStart)
	require.Equal(t, time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC), window.dailyEnd)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestBuildUsageAnalyticsQueryUsesHalfOpenRanges checks that raw head and tail ranges do not overlap.
func TestBuildUsageAnalyticsQueryUsesHalfOpenRanges(t *testing.T) {
	db, mock := newSQLMock(t)
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{sql: db, preAggregation: settings}
	start := time.Date(2026, 8, 1, 10, 15, 0, 0, time.UTC)
	end := time.Date(2026, 8, 3, 11, 45, 0, 0, time.UTC)
	coverage := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	watermark := time.Date(2026, 8, 3, 11, 30, 0, 0, time.UTC)
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(watermark, coverage))

	query, ok, err := repo.buildUsageAnalyticsQuery(context.Background(), UsageLogFilters{}, start, end, true)
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, query.cte, "ul.created_at >= $1 AND ul.created_at < $3")
	require.Contains(t, query.cte, "ul.created_at >= $5 AND ul.created_at < $2")
	require.Contains(t, query.cte, "bucket_date >= $8::date AND bucket_date < $9::date")
	require.Contains(t, query.cte, "bucket_start >= $3 AND bucket_start < $6")
	require.Contains(t, query.cte, "bucket_start >= $7 AND bucket_start < $4")
	require.Contains(t, query.cte, "COALESCE(NULLIF(TRIM(ul.model), ''), NULLIF(TRIM(ul.requested_model), ''), '')")
	require.Len(t, query.args, 9)
	require.Equal(t, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), query.args[5])
	require.Equal(t, time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), query.args[6])
	require.Equal(t, "2026-08-02", query.args[7])
	require.Equal(t, "2026-08-03", query.args[8])
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestBuildUsageAnalyticsQueryBeforeUTCMidnightKeepsTodayBoundary excludes yesterday's hourly buckets before UTC midnight.
func TestBuildUsageAnalyticsQueryBeforeUTCMidnightKeepsTodayBoundary(t *testing.T) {
	db, mock := newSQLMock(t)
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{sql: db, preAggregation: settings}
	// Beijing time 2026-08-06 00:00 to 03:30 is a partial window within one UTC day.
	start := time.Date(2026, 8, 5, 16, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 5, 19, 30, 0, 0, time.UTC)
	watermark := time.Date(2026, 8, 5, 19, 25, 0, 0, time.UTC)
	coverage := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(watermark, coverage))

	query, ok, err := repo.buildUsageAnalyticsQuery(context.Background(), UsageLogFilters{}, start, end, true)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, query.args, 9)
	// An empty daily range keeps the hourly split at today's start, not the preceding UTC midnight.
	require.Equal(t, start, query.args[5])
	require.Equal(t, start, query.args[6])
	require.Equal(t, "2026-08-05", query.args[7])
	require.Equal(t, "2026-08-05", query.args[8])
	require.Contains(t, query.cte, "bucket_date >= $8::date AND bucket_date < $9::date")
	require.Contains(t, query.cte, "bucket_start >= $3 AND bucket_start < $6")
	require.Contains(t, query.cte, "bucket_start >= $7 AND bucket_start < $4")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetAllGroupUsageSummaryFromAnalyticsIncludesYesterday(t *testing.T) {
	db, mock := newSQLMock(t)
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{sql: db, preAggregation: settings}
	now := time.Now().UTC()
	todayStart := now.AddDate(0, 0, -2).Truncate(24 * time.Hour)
	sourceOldest := todayStart.AddDate(0, 0, -7)
	watermark := now.Truncate(time.Hour)

	mock.ExpectQuery("(?s)SELECT source_oldest_at.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"source_oldest_at"}).AddRow(sourceOldest))
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(watermark, sourceOldest))
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(watermark, sourceOldest))
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(watermark, sourceOldest))
	mock.ExpectQuery("(?s)SELECT g.id, COALESCE\\(SUM\\(c.actual_cost\\), 0\\).*FROM groups g").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "total_cost"}).
			AddRow(int64(1), 9.0).
			AddRow(int64(2), 0.0))
	mock.ExpectQuery("(?s)SELECT group_id, COALESCE\\(SUM\\(actual_cost\\), 0\\).*FROM combined").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "cost"}).
			AddRow(int64(1), 2.0).
			AddRow(int64(2), 1.0))
	mock.ExpectQuery("(?s)SELECT group_id, COALESCE\\(SUM\\(actual_cost\\), 0\\).*FROM combined").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "cost"}).
			AddRow(int64(1), 3.0))

	results, ok, err := repo.getAllGroupUsageSummaryFromAnalytics(context.Background(), todayStart)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []usage.GroupUsageSummary{
		{GroupID: 1, TodayCost: 2, YesterdayCost: 3, TotalCost: 9},
		{GroupID: 2, TodayCost: 1, YesterdayCost: 0, TotalCost: 0},
	}, results)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetAllGroupUsageSummaryFallbackIncludesYesterday(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}
	location, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	todayStart := time.Date(2026, time.March, 9, 0, 0, 0, 0, location)
	yesterdayStart := time.Date(2026, time.March, 8, 0, 0, 0, 0, location)

	mock.ExpectQuery("(?s)SELECT source_oldest_at.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"source_oldest_at"}).AddRow(nil))
	mock.ExpectQuery("(?s)SELECT.*AS total_cost.*AS today_cost.*AS yesterday_cost.*FROM groups g").
		WithArgs(todayStart, yesterdayStart).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "total_cost", "today_cost", "yesterday_cost"}).
			AddRow(int64(1), 9.0, 2.0, 3.0))

	results, err := repo.GetAllGroupUsageSummary(context.Background(), todayStart)
	require.NoError(t, err)
	require.Equal(t, []usage.GroupUsageSummary{
		{GroupID: 1, TodayCost: 2, YesterdayCost: 3, TotalCost: 9},
	}, results)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageAnalyticsFallbackWarningIsRateLimited checks one warning per operation per minute.
func TestUsageAnalyticsFallbackWarningIsRateLimited(t *testing.T) {
	operation := "test_rate_limit_analytics_fallback"
	started := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	usageAnalyticsFallbackLogState.Lock()
	delete(usageAnalyticsFallbackLogState.lastByOperation, operation)
	usageAnalyticsFallbackLogState.Unlock()
	t.Cleanup(func() {
		usageAnalyticsFallbackLogState.Lock()
		delete(usageAnalyticsFallbackLogState.lastByOperation, operation)
		usageAnalyticsFallbackLogState.Unlock()
	})

	require.True(t, shouldLogUsageAnalyticsFallback(operation, started))
	require.False(t, shouldLogUsageAnalyticsFallback(operation, started.Add(59*time.Second)))
	require.True(t, shouldLogUsageAnalyticsFallback(operation, started.Add(time.Minute)))
}

// TestBuildUsageAnalyticsQueryWithoutDailyUsesContiguousArgs checks that hourly queries omit unused daily bounds.
func TestBuildUsageAnalyticsQueryWithoutDailyUsesContiguousArgs(t *testing.T) {
	db, mock := newSQLMock(t)
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{sql: db, preAggregation: settings}
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(4 * time.Hour)
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(end, start))

	query, ok, err := repo.buildUsageAnalyticsQuery(context.Background(), UsageLogFilters{}, start, end, false)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, query.args, 5)
	require.NotContains(t, query.cte, "$6")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestBuildUsageAnalyticsQueryNumbersOwnedTeamFiltersContinuously checks contiguous parameters after the time bounds.
func TestBuildUsageAnalyticsQueryNumbersOwnedTeamFiltersContinuously(t *testing.T) {
	db, mock := newSQLMock(t)
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{sql: db, preAggregation: settings}
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(4 * time.Hour)
	requestType := int16(usage.RequestTypeStream)
	stream := true
	billingType := int8(1)
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(end, start))
	mock.ExpectQuery("(?s)SELECT \\(.*SELECT tm.team_id.*team_memberships").
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(9)))

	query, ok, err := repo.buildUsageAnalyticsQuery(context.Background(), UsageLogFilters{
		UserID: 7, IncludeOwnedTeam: true, APIKeyID: 11, GroupID: 12, TeamID: 13,
		Model: "requested-model", ModelFilterSource: usage.ModelSourceRequested,
		RequestType: &requestType, Stream: &stream, BillingType: &billingType, BillingMode: "token",
	}, start, end, false)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, query.args, 15)
	require.Contains(t, query.cte, "user_id = $6")
	require.Contains(t, query.cte, "team_id = $7 AND user_id <> $6")
	require.Contains(t, query.where, "api_key_id = $8")
	require.Contains(t, query.where, "group_id = $9")
	require.Contains(t, query.where, "team_id = $10")
	require.Contains(t, query.where, "requested_model = $11")
	require.Contains(t, query.where, "request_type = $12")
	require.Contains(t, query.where, "stream = $13")
	require.Contains(t, query.where, "billing_type = $14")
	require.Contains(t, query.where, "billing_mode = $15")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestBuildUsageAnalyticsQueryUsesIndexableOwnedTeamRawSource checks separate raw user and team scans.
func TestBuildUsageAnalyticsQueryUsesIndexableOwnedTeamRawSource(t *testing.T) {
	db, mock := newSQLMock(t)
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{sql: db, preAggregation: settings}
	start := time.Date(2026, 8, 1, 10, 15, 0, 0, time.UTC)
	end := time.Date(2026, 8, 2, 11, 45, 0, 0, time.UTC)
	coverage := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	watermark := time.Date(2026, 8, 2, 11, 30, 0, 0, time.UTC)
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(watermark, coverage))
	mock.ExpectQuery("(?s)SELECT \\(.*SELECT tm.team_id.*team_memberships").
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(9)))

	query, ok, err := repo.buildUsageAnalyticsQuery(context.Background(), UsageLogFilters{
		UserID: 7, IncludeOwnedTeam: true,
	}, start, end, true)
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, query.cte, "SELECT * FROM usage_logs WHERE user_id = $10")
	require.Contains(t, query.cte, "SELECT * FROM usage_logs WHERE team_id = $11 AND user_id <> $10")
	require.Contains(t, query.where, "user_id = $10 OR (team_id = $11 AND user_id <> $10)")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestBuildUsageAnalyticsQueryRejectsUnsupportedFilters checks that missing aggregate dimensions require raw queries.
func TestBuildUsageAnalyticsQueryRejectsUnsupportedFilters(t *testing.T) {
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{preAggregation: settings}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	tests := []struct {
		name    string
		filters UsageLogFilters
	}{
		{name: "提供商维度", filters: UsageLogFilters{ProviderID: 1}},
		{name: "请求编号", filters: UsageLogFilters{RequestID: "request-1"}},
		{name: "默认模型语义", filters: UsageLogFilters{Model: "mapped-model"}},
		{name: "上游模型", filters: UsageLogFilters{Model: "upstream-model", ModelFilterSource: usage.ModelSourceUpstream}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, ok, err := repo.buildUsageAnalyticsQuery(context.Background(), test.filters, start, end, true)
			require.NoError(t, err)
			require.False(t, ok)
		})
	}
}

// TestGetModelStatsFromAnalyticsRejectsUnsupportedGrouping rejects dimensions absent from request-model aggregates.
func TestGetModelStatsFromAnalyticsRejectsUnsupportedGrouping(t *testing.T) {
	repo := &Store{}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	for _, source := range []string{usage.ModelSourceUpstream, usage.ModelSourceMapping} {
		_, ok, err := repo.getModelStatsFromAnalytics(context.Background(), start, end, UsageLogFilters{
			ModelFilterSource: source,
		})
		require.NoError(t, err)
		require.False(t, ok)
	}
}

// TestGetUserSpendingRankingFromAnalyticsReturnsCurrentUsername checks that rankings join current user identity fields.
func TestGetUserSpendingRankingFromAnalyticsReturnsCurrentUsername(t *testing.T) {
	db, mock := newSQLMock(t)
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{sql: db, preAggregation: settings}
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(4 * time.Hour)

	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(end, start))
	mock.ExpectQuery("(?s)user_spend AS \\(.*SELECT billing_user_id AS user_id.*GROUP BY billing_user_id.*COALESCE\\(u.username, ''\\)").
		WillReturnRows(sqlmock.NewRows([]string{
			"user_id", "email", "username", "actual_cost", "requests", "tokens",
			"total_actual_cost", "total_requests", "total_tokens",
		}).AddRow(int64(7), "rank@example.com", "rank-user", 12.5, int64(9), int64(900), 12.5, int64(9), int64(900)))

	got, ok, err := repo.getUserSpendingRankingFromAnalytics(context.Background(), start, end, 12)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []usage.UserSpendingRankingItem{
		{UserID: 7, Email: "rank@example.com", Username: "rank-user", ActualCost: 12.5, Requests: 9, Tokens: 900},
	}, got.Ranking)
	require.Equal(t, 12.5, got.TotalActualCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestGetUsageRankingFromAnalyticsGroupsByBillingUser combines team members under their billing user.
func TestGetUsageRankingFromAnalyticsGroupsByBillingUser(t *testing.T) {
	db, mock := newSQLMock(t)
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{sql: db, preAggregation: settings}
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(4 * time.Hour)

	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(end, start))
	mock.ExpectQuery("(?s)user_usage AS \\(.*SELECT billing_user_id AS user_id.*GROUP BY billing_user_id.*LEFT JOIN users u ON u.id = r.user_id").
		WillReturnRows(sqlmock.NewRows([]string{
			"rank", "user_id", "email", "username", "avatar_url", "requests",
			"input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens",
			"total_tokens", "actual_cost", "total_requests", "ranking_total_tokens", "total_actual_cost",
		}).AddRow(1, int64(9), "owner@example.com", "owner", "", int64(3), int64(100), int64(20), int64(0), int64(0), int64(120), 2.5, int64(3), int64(120), 2.5))

	got, ok, err := repo.getUsageRankingFromAnalytics(context.Background(), start, end, 20, usage.UsageRankingSortByTotalTokens)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, got.Ranking, 1)
	require.Equal(t, int64(9), got.Ranking[0].UserID)
	require.Equal(t, int64(3), got.Ranking[0].Requests)
	require.Equal(t, int64(120), got.Ranking[0].TotalTokens)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestGetUserUsageTrendFromAnalyticsGroupsByBillingUser combines team usage under the paying user in top-user trends.
func TestGetUserUsageTrendFromAnalyticsGroupsByBillingUser(t *testing.T) {
	db, mock := newSQLMock(t)
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{sql: db, preAggregation: settings}
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(4 * time.Hour)

	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(end, start))
	mock.ExpectQuery("(?s)top_users AS \\(.*SELECT billing_user_id AS user_id.*GROUP BY billing_user_id.*c\\.billing_user_id AS user_id.*LEFT JOIN users u ON u\\.id = c\\.billing_user_id").
		WillReturnRows(sqlmock.NewRows([]string{
			"date", "user_id", "email", "username", "requests", "tokens", "cost", "actual_cost",
		}).AddRow("2026-08-01", int64(9), "owner@example.com", "owner", int64(3), int64(120), 2.5, 2.5))

	got, ok, err := repo.getUserUsageTrendFromAnalytics(context.Background(), start, end, "day", 12)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []usage.UserUsageTrendPoint{
		{Date: "2026-08-01", UserID: 9, Email: "owner@example.com", Username: "owner", Requests: 3, Tokens: 120, Cost: 2.5, ActualCost: 2.5},
	}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestGetUsageTrendFromAnalyticsUsesNamedTimezoneForDST passes a named timezone to PostgreSQL for DST-aware buckets.
func TestGetUsageTrendFromAnalyticsUsesNamedTimezoneForDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	db, mock := newSQLMock(t)
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{sql: db, preAggregation: settings, calendar: timezone.NewCalendar(loc)}
	// This UTC range crosses New York's autumn 2026 clock change.
	start := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(end, start))
	mock.ExpectQuery("(?s)TO_CHAR\\(occurred_at AT TIME ZONE \\$6").
		WithArgs(start, end, start, end, end, "America/New_York").
		WillReturnRows(sqlmock.NewRows([]string{
			"date", "requests", "input_tokens", "output_tokens", "cache_creation_tokens",
			"cache_read_tokens", "total_tokens", "cost", "actual_cost",
		}))

	_, ok, err := repo.getUsageTrendFromAnalytics(context.Background(), start, end, "hour", UsageLogFilters{})
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestBatchAPIKeyUsageAnalyticsUsesDynamicTodayIDPosition places key IDs after the five time bounds.
func TestBatchAPIKeyUsageAnalyticsUsesDynamicTodayIDPosition(t *testing.T) {
	db, mock := newSQLMock(t)
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{sql: db, preAggregation: settings}
	now := time.Now().UTC()
	start := now.Add(-4 * time.Hour).Truncate(time.Hour)
	end := now.Add(time.Hour).Truncate(time.Hour)
	todayStart := timezone.NewCalendar(time.Local).
		Today().UTC()
	coverage := todayStart.Add(-time.Hour).Truncate(time.Hour)
	// A watermark just before now leaves usable aggregates across server timezones and near UTC midnight.
	watermark := now.Add(-time.Nanosecond)
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(watermark, coverage))

	// The daily query takes ten arguments: the ID array and nine time bounds.
	mock.ExpectQuery("(?s)SELECT api_key_id, COALESCE\\(SUM\\(actual_cost\\), 0\\).*FROM combined").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"api_key_id", "actual_cost"}))
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(watermark, coverage))
	mock.ExpectQuery("(?s)WHERE api_key_id = ANY\\(\\$6\\)").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"api_key_id", "actual_cost"}))

	_, ok, err := repo.getBatchAPIKeyUsageStatsFromAnalytics(context.Background(), []int64{3, 5}, start, end)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestBatchUserUsageAnalyticsUsesDynamicTodayIDPosition places user IDs after the five time bounds.
func TestBatchUserUsageAnalyticsUsesDynamicTodayIDPosition(t *testing.T) {
	db, mock := newSQLMock(t)
	settings := preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true, IntervalSeconds: 60},
	})
	repo := &Store{sql: db, preAggregation: settings}
	now := time.Now().UTC()
	start := now.Add(-4 * time.Hour).Truncate(time.Hour)
	end := now.Add(time.Hour).Truncate(time.Hour)
	todayStart := timezone.NewCalendar(time.Local).
		Today().UTC()
	coverage := todayStart.Add(-time.Hour).Truncate(time.Hour)
	// A watermark just before now leaves usable aggregates across server timezones and near UTC midnight.
	watermark := now.Add(-time.Nanosecond)
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(watermark, coverage))
	mock.ExpectQuery("(?s)SELECT user_id, platform, COALESCE\\(SUM\\(actual_cost\\), 0\\).*FROM combined").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "platform", "actual_cost"}))
	mock.ExpectQuery("(?s)SELECT live_watermark, coverage_start.*usage_analytics_aggregation_state").
		WillReturnRows(sqlmock.NewRows([]string{"live_watermark", "coverage_start"}).AddRow(watermark, coverage))
	mock.ExpectQuery("(?s)WHERE user_id = ANY\\(\\$6\\) AND actual_cost > 0").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "platform", "actual_cost"}))

	_, ok, err := repo.getBatchUserUsageStatsFromAnalytics(context.Background(), []int64{7, 9}, start, end)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, mock.ExpectationsWereMet())
}
