package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Model and group distributions keep base cost, user charges, and provider
// expense separate while choosing fixed SQL expressions for
// requested/upstream/mapping dimensions. Mocked trends and rankings check
// selector precedence, current payer identity, and decoded monetary columns
// without executing the database's aggregation calculations.

// TestResolveModelDimensionExpression supplies supported, empty, and invalid
// dimension labels. It checks the fixed requested/upstream/mapping SQL
// expressions and requested selection for unsupported labels.
func TestResolveModelDimensionExpression(t *testing.T) {
	tests := []struct {
		modelType string // supplied model dimension label
		want      string // expected fixed model expression
	}{
		{usage.ModelSourceRequested, "COALESCE(NULLIF(TRIM(model), ''), NULLIF(TRIM(requested_model), ''), '')"},
		{usage.ModelSourceUpstream, "COALESCE(NULLIF(TRIM(upstream_model), ''), model)"},
		{usage.ModelSourceMapping, "(COALESCE(NULLIF(TRIM(model), ''), NULLIF(TRIM(requested_model), ''), '') || ' -> ' || COALESCE(NULLIF(TRIM(upstream_model), ''), model))"},
		{"", "COALESCE(NULLIF(TRIM(model), ''), NULLIF(TRIM(requested_model), ''), '')"},
		{"invalid", "COALESCE(NULLIF(TRIM(model), ''), NULLIF(TRIM(requested_model), ''), '')"},
	}

	for _, tc := range tests {
		t.Run(tc.modelType, func(t *testing.T) {
			got := resolveModelDimensionExpression(tc.modelType)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestProviderFilteredModelStatsKeepUserActualCost supplies unequal user charges
// and provider/base costs under provider scope. It checks the expected SQL
// expressions and distinct monetary fields in the decoded model row.
func TestProviderFilteredModelStatsKeepUserActualCost(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}
	start := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)
	providerID := int64(3667)

	// Choose widely different charge and expense values so either provider-filtered
	// column being substituted for the other fails the decoded-field assertions.
	actualExpr := regexp.QuoteMeta("COALESCE(SUM(actual_cost), 0) as actual_cost")
	providerExpr := regexp.QuoteMeta("COALESCE(SUM(COALESCE(provider_stats_cost, total_cost) * COALESCE(provider_rate_multiplier, 1)), 0) as provider_cost")
	mock.ExpectQuery("(?s)"+actualExpr+".*"+providerExpr+".*provider_id = \\$3").
		WithArgs(start, end, providerID).
		WillReturnRows(sqlmock.NewRows([]string{
			"model", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "total_tokens",
			"cost", "actual_cost", "provider_cost",
		}).AddRow("glm-5.2", int64(1247), int64(100), int64(200), int64(0), int64(0), int64(300), 10.603, 530.797, 10.603))

	rows, err := repo.GetModelStatsWithFilters(context.Background(), start, end, 0, 0, providerID, 0, nil, nil, nil)

	require.NoError(t, err)
	require.Len(t, rows, 1)
	// Compare all three monetary columns with an absolute float tolerance while
	// keeping their widely separated fixture values distinguishable.
	require.InDelta(t, 530.797, rows[0].ActualCost, 0.000001)
	require.InDelta(t, 10.603, rows[0].ProviderCost, 0.000001)
	require.InDelta(t, 10.603, rows[0].Cost, 0.000001)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestGetUserBreakdownStatsRequestTypeIncludesLegacyFallback selects stream
// usage in a payer breakdown. It checks legacy transport matching and
// billing-user grouping SQL while decoding an empty result.
func TestGetUserBreakdownStatsRequestTypeIncludesLegacyFallback(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	requestType := int16(usage.RequestTypeStream)

	legacyFilter := `(ul.request_type = $3 OR (ul.request_type = 0 AND ul.stream = TRUE AND ul.openai_ws_mode = FALSE))`
	mock.ExpectQuery("(?s)COALESCE\\(ul\\.billing_user_id, ul\\.user_id, 0\\).*LEFT JOIN users u ON u\\.id = COALESCE\\(ul\\.billing_user_id, ul\\.user_id\\).*"+regexp.QuoteMeta(legacyFilter)+".*GROUP BY COALESCE\\(ul\\.billing_user_id, ul\\.user_id, 0\\)").
		WithArgs(start, end, requestType).
		WillReturnRows(sqlmock.NewRows([]string{
			"user_id", "email", "requests", "input_tokens", "output_tokens",
			"cache_tokens", "total_tokens", "cost", "actual_cost", "provider_cost",
		}))

	rows, err := repo.GetUserBreakdownStats(context.Background(), start, end, usage.UserBreakdownDimension{
		RequestType: &requestType,
	}, 0)

	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryGetUsageTrendWithFiltersRequestTypePriority supplies
// stream type and a legacy stream selector. It checks raw trend SQL binds the
// request category alone and returns an empty decoded trend.
func TestUsageLogRepositoryGetUsageTrendWithFiltersRequestTypePriority(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	requestType := int16(usage.RequestTypeStream)
	stream := true

	mock.ExpectQuery("AND \\(request_type = \\$3 OR \\(request_type = 0 AND stream = TRUE AND openai_ws_mode = FALSE\\)\\)").
		WithArgs(start, end, requestType).
		WillReturnRows(sqlmock.NewRows([]string{"date", "requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_tokens", "cost", "actual_cost"}))

	trend, err := repo.GetUsageTrendWithFilters(context.Background(), start, end, "day", 0, 0, 0, 0, "", &requestType, &stream, nil)
	require.NoError(t, err)
	require.Empty(t, trend)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryGetUsageTrendWithUsageFiltersRequestedModelSource
// selects internal-model filtering through unified selectors. It checks the
// compatibility expression and model argument on an empty mocked trend.
func TestUsageLogRepositoryGetUsageTrendWithUsageFiltersRequestedModelSource(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	filters := usage.UsageLogFilters{
		Model:             "gpt-5",
		ModelFilterSource: usage.ModelSourceRequested,
	}

	mock.ExpectQuery("AND COALESCE\\(NULLIF\\(TRIM\\(model\\), ''\\), NULLIF\\(TRIM\\(requested_model\\), ''\\), ''\\) = \\$3").
		WithArgs(start, end, "gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{"date", "requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_tokens", "cost", "actual_cost"}))

	trend, err := repo.GetUsageTrendWithUsageFilters(context.Background(), start, end, "day", filters)
	require.NoError(t, err)
	require.Empty(t, trend)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryGetModelStatsWithFiltersRequestTypePriority supplies
// WebSocket type with false stream selection. It checks model SQL uses
// type/legacy compatibility alone and decodes an empty model result.
func TestUsageLogRepositoryGetModelStatsWithFiltersRequestTypePriority(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	requestType := int16(usage.RequestTypeWSV2)
	stream := false

	mock.ExpectQuery("AND \\(request_type = \\$3 OR \\(request_type = 0 AND openai_ws_mode = TRUE\\)\\)").
		WithArgs(start, end, requestType).
		WillReturnRows(sqlmock.NewRows([]string{"model", "requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_tokens", "cost", "actual_cost", "provider_cost"}))

	stats, err := repo.GetModelStatsWithFilters(context.Background(), start, end, 0, 0, 0, 0, &requestType, &stream, nil)
	require.NoError(t, err)
	require.Empty(t, stats)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryGetUserModelStatsUsesInternalRequestedModel supplies
// current owner membership with one model row. It checks disjoint actor/team raw
// branches, internal-model grouping, and the decoded model name.
func TestUsageLogRepositoryGetUserModelStatsUsesInternalRequestedModel(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("(?s)SELECT \\(.*FROM team_memberships.*tm.user_id = \\$1.*tm.role = 'owner'").
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(42)))
	mock.ExpectQuery("(?s)SELECT\\s+COALESCE\\(NULLIF\\(TRIM\\(model\\), ''\\), NULLIF\\(TRIM\\(requested_model\\), ''\\), ''\\) as model,.*SELECT \\* FROM usage_logs WHERE user_id = \\$3.*UNION ALL.*SELECT \\* FROM usage_logs WHERE team_id = \\$4 AND user_id <> \\$3.*WHERE created_at >= \\$1 AND created_at < \\$2.*GROUP BY COALESCE\\(NULLIF\\(TRIM\\(model\\), ''\\), NULLIF\\(TRIM\\(requested_model\\), ''\\), ''\\) ORDER BY total_tokens DESC").
		WithArgs(start, end, int64(7), int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{
			"model", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "total_tokens",
			"cost", "actual_cost", "provider_cost",
		}).AddRow("gpt-5.5", int64(2), int64(10), int64(20), int64(0), int64(0), int64(30), 0.1, 0.08, 0.07))

	stats, err := repo.GetUserModelStats(context.Background(), 7, start, end)
	require.NoError(t, err)
	require.Len(t, stats, 1)
	require.Equal(t, "gpt-5.5", stats[0].Model)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryGetModelStatsProviderCostColumn supplies two model rows
// with separate monetary measures. It checks both decode correctly and provider
// expense does not overwrite base cost or actual user charges.
func TestUsageLogRepositoryGetModelStatsProviderCostColumn(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("FROM usage_logs").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"model", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "total_tokens",
			"cost", "actual_cost", "provider_cost",
		}).
			AddRow("claude-opus-4-6", int64(10), int64(100), int64(200), int64(5), int64(3), int64(308), 2.5, 2.0, 1.8).
			AddRow("claude-sonnet-4-6", int64(5), int64(50), int64(100), int64(0), int64(0), int64(150), 1.0, 0.8, 0.7))

	results, err := repo.GetModelStatsWithFilters(context.Background(), start, end, 0, 0, 0, 0, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "claude-opus-4-6", results[0].Model)
	require.Equal(t, 2.5, results[0].Cost)
	require.Equal(t, 2.0, results[0].ActualCost)
	require.Equal(t, 1.8, results[0].ProviderCost)
	require.Equal(t, "claude-sonnet-4-6", results[1].Model)
	require.Equal(t, 0.7, results[1].ProviderCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryGetModelStatsWithUsageFiltersAppliesInternalModelFilter
// selects one internal requested model through unified filters. It checks
// matching SQL/arguments and decodes the model retained in the response.
func TestUsageLogRepositoryGetModelStatsWithUsageFiltersAppliesInternalModelFilter(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	filters := usage.UsageLogFilters{Model: "gpt-5"}

	mock.ExpectQuery("AND COALESCE\\(NULLIF\\(TRIM\\(model\\), ''\\), NULLIF\\(TRIM\\(requested_model\\), ''\\), ''\\) = \\$3").
		WithArgs(start, end, "gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{
			"model", "requests", "input_tokens", "output_tokens",
			"cache_creation_tokens", "cache_read_tokens", "total_tokens",
			"cost", "actual_cost", "provider_cost",
		}).AddRow("gpt-5", int64(1), int64(10), int64(20), int64(0), int64(0), int64(30), 0.1, 0.08, 0.07))

	results, err := repo.GetModelStatsWithUsageFiltersBySource(context.Background(), start, end, filters, usage.ModelSourceRequested)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "gpt-5", results[0].Model)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryGetGroupStatsProviderCostColumn supplies group names and
// distinct base, user, and provider amounts. It checks IDs, display names, and
// monetary columns survive group-row decoding.
func TestUsageLogRepositoryGetGroupStatsProviderCostColumn(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("FROM usage_logs").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "group_name", "requests", "total_tokens",
			"cost", "actual_cost", "provider_cost",
		}).
			AddRow(int64(1), "azure-cc", int64(100), int64(5000), 10.0, 8.5, 7.2).
			AddRow(int64(2), "max", int64(50), int64(2000), 5.0, 4.0, 3.5))

	results, err := repo.GetGroupStatsWithFilters(context.Background(), start, end, 0, 0, 0, 0, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, int64(1), results[0].GroupID)
	require.Equal(t, "azure-cc", results[0].GroupName)
	require.Equal(t, 10.0, results[0].Cost)
	require.Equal(t, 8.5, results[0].ActualCost)
	require.Equal(t, 7.2, results[0].ProviderCost)
	require.Equal(t, int64(2), results[1].GroupID)
	require.Equal(t, 3.5, results[1].ProviderCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryGetGroupStatsWithUsageFiltersAppliesRequestedModelFilter
// selects one internal model for group totals. It checks the ul-qualified model
// expression, bound value, and decoded matching group ID.
func TestUsageLogRepositoryGetGroupStatsWithUsageFiltersAppliesRequestedModelFilter(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	filters := usage.UsageLogFilters{Model: "gpt-5"}

	mock.ExpectQuery("AND COALESCE\\(NULLIF\\(TRIM\\(ul.model\\), ''\\), NULLIF\\(TRIM\\(ul.requested_model\\), ''\\), ''\\) = \\$3").
		WithArgs(start, end, "gpt-5").
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "group_name", "requests", "total_tokens",
			"cost", "actual_cost", "provider_cost",
		}).AddRow(int64(1), "default", int64(1), int64(30), 0.1, 0.08, 0.07))

	results, err := repo.GetGroupStatsWithUsageFilters(context.Background(), start, end, filters)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, int64(1), results[0].GroupID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryGetUserSpendingRanking supplies limited payer rows with
// population totals larger than their visible sum. It checks billing-user SQL,
// row fields, and the separately decoded complete totals.
func TestUsageLogRepositoryGetUserSpendingRanking(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	rows := sqlmock.NewRows([]string{"user_id", "email", "username", "actual_cost", "requests", "tokens", "total_actual_cost", "total_requests", "total_tokens"}).
		AddRow(int64(2), "beta@example.com", "beta", 12.5, int64(9), int64(900), 40.0, int64(30), int64(2600)).
		AddRow(int64(1), "alpha@example.com", "alpha", 12.5, int64(8), int64(800), 40.0, int64(30), int64(2600)).
		AddRow(int64(3), "gamma@example.com", "", 4.25, int64(5), int64(300), 40.0, int64(30), int64(2600))

	mock.ExpectQuery("(?s)WITH user_spend AS \\(.*COALESCE\\(u\\.billing_user_id, u\\.user_id\\).*GROUP BY COALESCE\\(u\\.billing_user_id, u\\.user_id\\)").
		WithArgs(start, end, 12).
		WillReturnRows(rows)

	got, err := repo.GetUserSpendingRanking(context.Background(), start, end, 12)
	require.NoError(t, err)
	require.Equal(t, &usage.UserSpendingRankingResponse{
		Ranking: []usage.UserSpendingRankingItem{
			{UserID: 2, Email: "beta@example.com", Username: "beta", ActualCost: 12.5, Requests: 9, Tokens: 900},
			{UserID: 1, Email: "alpha@example.com", Username: "alpha", ActualCost: 12.5, Requests: 8, Tokens: 800},
			{UserID: 3, Email: "gamma@example.com", ActualCost: 4.25, Requests: 5, Tokens: 300},
		},
		TotalActualCost: 40.0,
		TotalRequests:   30,
		TotalTokens:     2600,
	}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageLogRepositoryGetUserUsageTrendGroupsByBillingUser supplies one owner
// trend row representing team activity. It checks historical payer recovery and
// current identity joins, then decodes the payer's usage values.
func TestUsageLogRepositoryGetUserUsageTrendGroupsByBillingUser(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}
	start := time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("(?s)WITH top_users AS \\(.*SELECT COALESCE\\(billing_user_id, user_id\\) AS user_id.*GROUP BY COALESCE\\(billing_user_id, user_id\\).*COALESCE\\(u\\.billing_user_id, u\\.user_id\\) AS user_id.*LEFT JOIN users us ON COALESCE\\(u\\.billing_user_id, u\\.user_id\\) = us\\.id").
		WithArgs(start, end, 12, start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"date", "user_id", "email", "username", "requests", "tokens", "cost", "actual_cost",
		}).AddRow("2025-01-03", int64(9), "owner@example.com", "owner", int64(3), int64(120), 2.5, 2.5))

	got, err := repo.GetUserUsageTrend(context.Background(), start, end, "day", 12)
	require.NoError(t, err)
	require.Equal(t, []usage.UserUsageTrendPoint{
		{Date: "2025-01-03", UserID: 9, Email: "owner@example.com", Username: "owner", Requests: 3, Tokens: 120, Cost: 2.5, ActualCost: 2.5},
	}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
