package postgres

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Summary and endpoint SQL must retain user-charge meanings even when a provider
// filter narrows records. Mocked reports check transaction-local JIT recovery,
// endpoint-source dispatch, model and transport predicates, and platform cost
// accumulation while keeping provider expense separate from actual_cost.

// TestProviderReportOwnsJITTransaction injects successful and failed report
// queries on a pool-backed store. It checks local JIT setup followed by commit
// on success or rollback on failure.
func TestProviderReportOwnsJITTransaction(t *testing.T) {
	for _, fail := range []bool{false, true} {
		db, mock := newSQLMock(t)
		r := &Store{sql: db, db: db}
		mock.ExpectBegin()
		mock.ExpectExec("SET LOCAL jit = off").WillReturnResult(sqlmock.NewResult(0, 0))
		query := mock.ExpectQuery("(?s)WITH scoped.*GROUP BY GROUPING SETS")
		if fail {
			query.WillReturnError(errors.New("report unavailable"))
			mock.ExpectRollback()
		} else {
			query.WillReturnRows(sqlmock.NewRows([]string{"kind", "label", "requests", "input", "output", "creation", "read", "cost", "user_cost", "provider_cost", "duration", "count"}))
			mock.ExpectCommit()
		}
		_, err := r.GetProviderUsageStats(context.Background(), 1, time.Now().Add(-time.Hour), time.Now())
		if fail {
			require.ErrorContains(t, err, "report unavailable")
		} else {
			require.NoError(t, err)
		}
		require.NoError(t, mock.ExpectationsWereMet())
	}
}

// TestStatsEndpointSourceQueries selects all or one endpoint dimension for a raw
// summary. It checks only selected detail queries execute and only their
// corresponding response lists are populated.
func TestStatsEndpointSourceQueries(t *testing.T) {
	for _, source := range []string{"", "inbound", "upstream", "path"} {
		db, mock := newSQLMock(t)
		r := &Store{sql: db}
		mock.ExpectQuery("(?s)SELECT.*total_requests.*FROM usage_logs").WillReturnRows(sqlmock.NewRows([]string{"requests", "input", "output", "cache", "creation", "read", "cost", "actual", "provider", "average"}).AddRow(1, 2, 3, 4, 5, 6, 7, 8, 9, 10))
		for _, kind := range []string{"inbound", "upstream", "path"} {
			if source != "" && source != kind {
				continue
			}
			pattern := "(?s)SELECT.*TRIM\\(" + kind + "_endpoint\\).*GROUP BY endpoint"
			if kind == "path" {
				pattern = "(?s)SELECT.*CONCAT.*GROUP BY endpoint"
			}
			mock.ExpectQuery(pattern).WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "tokens", "cost", "actual"}).AddRow(kind, 1, 2, 3, 4))
		}
		result, err := r.GetStatsWithFilters(context.Background(), UsageLogFilters{EndpointSource: source})
		require.NoError(t, err)
		if source == "" || source == "inbound" {
			require.Len(t, result.Endpoints, 1)
		} else {
			require.Empty(t, result.Endpoints)
		}
		if source == "" || source == "upstream" {
			require.Len(t, result.UpstreamEndpoints, 1)
		} else {
			require.Empty(t, result.UpstreamEndpoints)
		}
		if source == "" || source == "path" {
			require.Len(t, result.EndpointPaths, 1)
		} else {
			require.Empty(t, result.EndpointPaths)
		}
		require.NoError(t, mock.ExpectationsWereMet())
	}
}

// TestResolveEndpointColumn supplies inbound, upstream, path, empty, and unknown
// labels. It checks fixed SQL expressions and inbound selection for unsupported
// labels, keeping caller text out of identifiers.
func TestResolveEndpointColumn(t *testing.T) {
	tests := []struct {
		endpointType string // supplied endpoint grouping label
		want         string // expected fixed SQL expression
	}{
		{"inbound", "ul.inbound_endpoint"},
		{"upstream", "ul.upstream_endpoint"},
		{"path", "ul.inbound_endpoint || ' -> ' || ul.upstream_endpoint"},
		{"", "ul.inbound_endpoint"},        // Default column
		{"unknown", "ul.inbound_endpoint"}, // Unknown types use the default column.
	}

	for _, tc := range tests {
		t.Run(tc.endpointType, func(t *testing.T) {
			got := resolveEndpointColumn(tc.endpointType)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestProviderFilteredEndpointStatsKeepUserActualCost supplies unequal base cost
// and user charges under provider scope. It checks inbound, upstream, and path
// queries retain actual_cost and decode distinct monetary values.
func TestProviderFilteredEndpointStatsKeepUserActualCost(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}
	start := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)
	providerID := int64(3667)
	actualExpr := regexp.QuoteMeta("COALESCE(SUM(actual_cost), 0) as actual_cost")

	// Supply a user charge far above base cost for each endpoint shape, making a
	// provider-cost substitution fail the same decoded-value assertions.
	for _, endpoint := range []string{"/v1/chat/completions", "/api/v1/coding/chat", "/v1/chat/completions -> /api/v1/coding/chat"} {
		mock.ExpectQuery("(?s)SELECT.*"+actualExpr+".*provider_id = \\$3").
			WithArgs(start, end, providerID).
			WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}).
				AddRow(endpoint, int64(1247), int64(300), 10.603, 530.797))
	}

	inbound, err := repo.GetEndpointStatsWithFilters(context.Background(), start, end, 0, 0, providerID, 0, "", nil, nil, nil)
	require.NoError(t, err)
	upstream, err := repo.GetUpstreamEndpointStatsWithFilters(context.Background(), start, end, 0, 0, providerID, 0, "", nil, nil, nil)
	require.NoError(t, err)
	paths, err := repo.getEndpointPathStatsWithFilters(context.Background(), start, end, UsageLogFilters{ProviderID: providerID})
	require.NoError(t, err)

	// Apply one absolute tolerance to monetary readback in all three endpoint
	// shapes, allowing decimal-to-float conversion differences.
	for _, rows := range [][]EndpointStat{inbound, upstream, paths} {
		require.Len(t, rows, 1)
		require.InDelta(t, 530.797, rows[0].ActualCost, 0.000001)
		require.InDelta(t, 10.603, rows[0].Cost, 0.000001)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestGeminiUsageTotalsBatchUsesProviderCost supplies mocked Flash/Pro expense
// and request totals. It checks both SQL cost expressions use recorded provider
// bases/multipliers and decodes the separate model-category results.
func TestGeminiUsageTotalsBatchUsesProviderCost(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}
	start := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	providerID := int64(3667)
	providerCostExpr := regexp.QuoteMeta("COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1)")

	// The mocked batch contains separate Flash/Pro expense expressions; matching
	// both recorded-base formulas makes accidental actual-charge substitution
	// observable.
	mock.ExpectQuery("(?s)"+providerCostExpr+".*"+providerCostExpr+".*FROM usage_logs").
		WithArgs(sqlmock.AnyArg(), start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"provider_id", "flash_requests", "pro_requests", "flash_tokens", "pro_tokens", "flash_cost", "pro_cost",
		}).AddRow(providerID, int64(3), int64(2), int64(400), int64(300), 2.0, 10.0))

	totals, err := repo.GetGeminiUsageTotalsBatch(context.Background(), []int64{providerID}, start, end)

	require.NoError(t, err)
	require.Equal(t, int64(3), totals[providerID].FlashRequests)
	require.Equal(t, int64(2), totals[providerID].ProRequests)
	// Compare both decoded expense values with the same absolute float tolerance.
	require.InDelta(t, 2, totals[providerID].FlashCost, 0.000001)
	require.InDelta(t, 10, totals[providerID].ProCost, 0.000001)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryGetStatsWithFiltersRequestedModelSource selects internal
// requested-model filtering. It checks summary and endpoint queries carry the
// same model value and the mocked request total reaches the response.
func TestUsageLogRepositoryGetStatsWithFiltersRequestedModelSource(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	filters := usage.UsageLogFilters{
		Model:             "gpt-5",
		ModelFilterSource: usage.ModelSourceRequested,
	}

	mock.ExpectQuery("FROM usage_logs\\s+WHERE COALESCE\\(NULLIF\\(TRIM\\(model\\), ''\\), NULLIF\\(TRIM\\(requested_model\\), ''\\), ''\\) = \\$1").
		WithArgs("gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests",
			"total_input_tokens",
			"total_output_tokens",
			"total_cache_tokens",
			"total_cache_creation_tokens",
			"total_cache_read_tokens",
			"total_cost",
			"total_actual_cost",
			"total_provider_cost",
			"avg_duration_ms",
		}).AddRow(int64(1), int64(2), int64(3), int64(4), int64(1), int64(3), 1.2, 1.0, 1.2, 20.0))
	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(inbound_endpoint\\), ''\\), 'unknown'\\) AS endpoint").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))
	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(upstream_endpoint\\), ''\\), 'unknown'\\) AS endpoint").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))
	mock.ExpectQuery("SELECT CONCAT\\(").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.TotalRequests)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryGetStatsWithFiltersRequestTypePriority supplies
// conflicting sync and stream selectors. It checks all raw queries use sync
// compatibility and decodes token buckets plus allocated provider cost.
func TestUsageLogRepositoryGetStatsWithFiltersRequestTypePriority(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	requestType := int16(usage.RequestTypeSync)
	stream := true
	filters := usage.UsageLogFilters{
		RequestType: &requestType,
		Stream:      &stream,
	}

	mock.ExpectQuery("FROM usage_logs\\s+WHERE \\(request_type = \\$1 OR \\(request_type = 0 AND stream = FALSE AND openai_ws_mode = FALSE\\)\\)").
		WithArgs(requestType).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests",
			"total_input_tokens",
			"total_output_tokens",
			"total_cache_tokens",
			"total_cache_creation_tokens",
			"total_cache_read_tokens",
			"total_cost",
			"total_actual_cost",
			"total_provider_cost",
			"avg_duration_ms",
		}).AddRow(int64(1), int64(2), int64(3), int64(4), int64(1), int64(3), 1.2, 1.0, 1.2, 20.0))
	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(inbound_endpoint\\), ''\\), 'unknown'\\) AS endpoint").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), requestType).
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))
	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(upstream_endpoint\\), ''\\), 'unknown'\\) AS endpoint").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), requestType).
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))
	mock.ExpectQuery("SELECT CONCAT\\(").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), requestType).
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.TotalRequests)
	require.Equal(t, int64(9), stats.TotalTokens)
	require.Equal(t, int64(1), stats.TotalCacheCreationTokens)
	require.Equal(t, int64(3), stats.TotalCacheReadTokens)
	require.NotNil(t, stats.TotalProviderCost, "TotalProviderCost should always be returned")
	require.Equal(t, 1.2, *stats.TotalProviderCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryGetBatchUserUsageStatsSplitsByEffectivePlatform supplies
// several platforms and a null label for one actor. It checks sums include every
// row while named breakdowns omit the null platform.
func TestUsageLogRepositoryGetBatchUserUsageStatsSplitsByEffectivePlatform(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * 24 * time.Hour)
	rows := sqlmock.NewRows([]string{"user_id", "platform", "total_cost", "today_cost"}).
		AddRow(int64(1), "anthropic", 3.5, 1.5).
		AddRow(int64(1), "openai", 2.25, 0.25).
		AddRow(int64(1), nil, 0.75, 0.5).
		AddRow(int64(2), "gemini", 7.0, 4.0)

	mock.ExpectQuery("LEFT JOIN groups g ON g\\.id = ul\\.group_id").
		WithArgs(sqlmock.AnyArg(), start, end, sqlmock.AnyArg()).
		WillReturnRows(rows)

	stats, err := repo.GetBatchUserUsageStats(context.Background(), []int64{1, 2}, start, end)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	require.Len(t, stats, 2)
	// Allow float accumulation differences while checking range and today charges
	// for both actors and the accompanying platform entries.
	require.InDelta(t, 6.5, stats[1].TotalActualCost, 0.000001)
	require.InDelta(t, 2.25, stats[1].TodayActualCost, 0.000001)
	require.Equal(t, []PlatformUsage{
		{Platform: "anthropic", TotalActualCost: 3.5, TodayActualCost: 1.5},
		{Platform: "openai", TotalActualCost: 2.25, TodayActualCost: 0.25},
	}, stats[1].ByPlatform)

	require.InDelta(t, 7.0, stats[2].TotalActualCost, 0.000001)
	require.InDelta(t, 4.0, stats[2].TodayActualCost, 0.000001)
	require.Equal(t, []PlatformUsage{
		{Platform: "gemini", TotalActualCost: 7.0, TodayActualCost: 4.0},
	}, stats[2].ByPlatform)
}

// TestUsageLogRepositoryGetStatsWithFiltersAlwaysReturnsProviderCost requests an
// unrestricted summary with no provider selector. It checks provider expense
// remains allocated alongside distinct cache-creation/read totals in the decoded
// result.
func TestUsageLogRepositoryGetStatsWithFiltersAlwaysReturnsProviderCost(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	// Global selected rows still carry provider expense. Leave ProviderID unset so
	// the test catches response omission tied incorrectly to provider-only scope.
	filters := usage.UsageLogFilters{}

	mock.ExpectQuery("FROM usage_logs").
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens",
			"total_cache_tokens", "total_cache_creation_tokens", "total_cache_read_tokens", "total_cost", "total_actual_cost",
			"total_provider_cost", "avg_duration_ms",
		}).AddRow(int64(50), int64(1000), int64(2000), int64(100), int64(25), int64(75), 15.0, 12.5, 11.0, 100.0))
	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(inbound_endpoint\\)").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))
	mock.ExpectQuery("SELECT COALESCE\\(NULLIF\\(TRIM\\(upstream_endpoint\\)").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))
	mock.ExpectQuery("SELECT CONCAT\\(").
		WillReturnRows(sqlmock.NewRows([]string{"endpoint", "requests", "total_tokens", "cost", "actual_cost"}))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.NotNil(t, stats.TotalProviderCost, "TotalProviderCost must always be returned, even without ProviderID filter")
	require.Equal(t, 11.0, *stats.TotalProviderCost)
	require.Equal(t, int64(25), stats.TotalCacheCreationTokens)
	require.Equal(t, int64(75), stats.TotalCacheReadTokens)
	require.NoError(t, mock.ExpectationsWereMet())
}
