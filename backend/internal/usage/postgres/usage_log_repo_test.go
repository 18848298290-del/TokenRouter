package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

func TestAppendUsageLogBillingModeWhereCondition(t *testing.T) {
	tests := []struct {
		name          string
		billingMode   string
		wantCondition string
	}{
		{
			name:          "image includes explicit image and legacy image rows",
			billingMode:   string(routing.BillingModeImage),
			wantCondition: "(billing_mode = $1 OR ((billing_mode IS NULL OR billing_mode = '') AND COALESCE(video_duration_seconds, 0) <= 0 AND COALESCE(image_count, 0) > 0))",
		},
		{
			name:          "video includes explicit video and legacy video rows",
			billingMode:   string(routing.BillingModeVideo),
			wantCondition: "(billing_mode = $1 OR ((billing_mode IS NULL OR billing_mode = '') AND COALESCE(video_duration_seconds, 0) > 0))",
		},
		{
			name:          "token includes legacy non-media rows",
			billingMode:   string(routing.BillingModeToken),
			wantCondition: "(billing_mode = $1 OR ((billing_mode IS NULL OR billing_mode = '') AND COALESCE(video_duration_seconds, 0) <= 0 AND COALESCE(image_count, 0) <= 0))",
		},
		{
			name:          "per request remains exact",
			billingMode:   string(routing.BillingModePerRequest),
			wantCondition: "billing_mode = $1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conditions, args := appendUsageLogBillingModeWhereCondition(nil, nil, tt.billingMode)
			require.Equal(t, []string{tt.wantCondition}, conditions)
			require.Equal(t, []any{tt.billingMode}, args)
		})
	}
}

func TestAppendUsageLogBillingModeWhereConditionWithAlias(t *testing.T) {
	conditions, args := appendUsageLogBillingModeWhereConditionWithAlias(nil, nil, string(routing.BillingModeImage), "ul")

	require.Equal(t, []string{"(ul.billing_mode = $1 OR ((ul.billing_mode IS NULL OR ul.billing_mode = '') AND COALESCE(ul.video_duration_seconds, 0) <= 0 AND COALESCE(ul.image_count, 0) > 0))"}, conditions)
	require.Equal(t, []any{string(routing.BillingModeImage)}, args)
}

func TestAppendUsageLogBillingModeQueryFilter(t *testing.T) {
	query, args := appendUsageLogBillingModeQueryFilter("SELECT * FROM usage_logs WHERE user_id = $1", []any{int64(42)}, string(routing.BillingModeToken), "")

	require.Equal(t, "SELECT * FROM usage_logs WHERE user_id = $1 AND (billing_mode = $2 OR ((billing_mode IS NULL OR billing_mode = '') AND COALESCE(video_duration_seconds, 0) <= 0 AND COALESCE(image_count, 0) <= 0))", query)
	require.Equal(t, []any{int64(42), string(routing.BillingModeToken)}, args)
}

func TestUsageLogRepositoryGetUsageRankingMasksEmail(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{sql: db}

	start := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	rows := sqlmock.NewRows([]string{
		"rank",
		"user_id",
		"email",
		"username",
		"avatar_url",
		"requests",
		"input_tokens",
		"output_tokens",
		"cache_creation_tokens",
		"cache_read_tokens",
		"total_tokens",
		"actual_cost",
		"total_requests",
		"ranking_total_tokens",
		"total_actual_cost",
	}).
		AddRow(1, int64(2), "beta@example.com", "beta", "https://cdn.example/beta.png", int64(9), int64(400), int64(300), int64(100), int64(100), int64(900), 1.25, int64(17), int64(1700), 2.0).
		AddRow(2, int64(1), "alpha@example.com", "", "", int64(8), int64(300), int64(300), int64(100), int64(100), int64(800), 0.75, int64(17), int64(1700), 2.0)

	mock.ExpectQuery("(?s)COALESCE\\(u\\.billing_user_id, u\\.user_id\\).*LEFT JOIN users us ON r\\.user_id = us\\.id").
		WithArgs(start, end, 20).
		WillReturnRows(rows)

	got, err := repo.GetUsageRanking(context.Background(), start, end, 20, usage.UsageRankingSortByTotalTokens)
	require.NoError(t, err)
	require.Equal(t, int64(17), got.TotalRequests)
	require.Equal(t, int64(1700), got.TotalTokens)
	require.Equal(t, 2.0, got.TotalActualCost)
	require.Equal(t, []usage.UsageRankingItem{
		{Rank: 1, UserID: 2, DisplayName: "beta", AvatarURL: "https://cdn.example/beta.png", Requests: 9, InputTokens: 400, OutputTokens: 300, CacheCreationTokens: 100, CacheReadTokens: 100, TotalTokens: 900, ActualCost: 1.25},
		{Rank: 2, UserID: 1, DisplayName: "a***a@example.com", Requests: 8, InputTokens: 300, OutputTokens: 300, CacheCreationTokens: 100, CacheReadTokens: 100, TotalTokens: 800, ActualCost: 0.75},
	}, got.Ranking)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBuildRequestTypeFilterConditionLegacyFallback(t *testing.T) {
	tests := []struct {
		name      string
		request   int16
		wantWhere string
		wantArg   int16
	}{
		{
			name:      "sync_with_legacy_fallback",
			request:   int16(usage.RequestTypeSync),
			wantWhere: "(request_type = $3 OR (request_type = 0 AND stream = FALSE AND openai_ws_mode = FALSE))",
			wantArg:   int16(usage.RequestTypeSync),
		},
		{
			name:      "stream_with_legacy_fallback",
			request:   int16(usage.RequestTypeStream),
			wantWhere: "(request_type = $3 OR (request_type = 0 AND stream = TRUE AND openai_ws_mode = FALSE))",
			wantArg:   int16(usage.RequestTypeStream),
		},
		{
			name:      "ws_v2_with_legacy_fallback",
			request:   int16(usage.RequestTypeWSV2),
			wantWhere: "(request_type = $3 OR (request_type = 0 AND openai_ws_mode = TRUE))",
			wantArg:   int16(usage.RequestTypeWSV2),
		},
		{
			name:      "cyber_without_legacy_fallback",
			request:   int16(usage.RequestTypeCyberBlocked),
			wantWhere: "request_type = $3",
			wantArg:   int16(usage.RequestTypeCyberBlocked),
		},
		{
			name:      "invalid_request_type_normalized_to_unknown",
			request:   int16(99),
			wantWhere: "request_type = $3",
			wantArg:   int16(usage.RequestTypeUnknown),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, args := buildRequestTypeFilterCondition(3, tt.request)
			require.Equal(t, tt.wantWhere, where)
			require.Equal(t, []any{tt.wantArg}, args)
		})
	}
}

func TestSafeDateFormat(t *testing.T) {
	tests := []struct {
		name        string
		granularity string
		expected    string
	}{
		// Allowed granularities
		{"hour", "hour", "YYYY-MM-DD HH24:00"},
		{"day", "day", "YYYY-MM-DD"},
		{"week", "week", "IYYY-IW"},
		{"month", "month", "YYYY-MM"},

		// Unknown values use the default format.
		{"空字符串", "", "YYYY-MM-DD"},
		{"未知粒度 year", "year", "YYYY-MM-DD"},
		{"未知粒度 minute", "minute", "YYYY-MM-DD"},

		// SQL injection input
		{"SQL 注入尝试", "'; DROP TABLE users; --", "YYYY-MM-DD"},
		{"带引号", "day'", "YYYY-MM-DD"},
		{"带括号", "day)", "YYYY-MM-DD"},
		{"Unicode", "日", "YYYY-MM-DD"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safeDateFormat(tc.granularity)
			require.Equal(t, tc.expected, got, "safeDateFormat(%q)", tc.granularity)
		})
	}
}

func TestUsageRankingQueryPartsFollowSelectedMetric(t *testing.T) {
	tests := []struct {
		name                 string
		sortBy               usage.UsageRankingSortBy
		rawEligibility       string
		analyticsEligibility string
		orderBy              string
	}{
		{
			name:                 "total tokens",
			sortBy:               usage.UsageRankingSortByTotalTokens,
			rawEligibility:       "SUM(u.input_tokens + u.output_tokens + u.cache_creation_tokens + u.cache_read_tokens)",
			analyticsEligibility: "SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens)",
			orderBy:              "total_tokens DESC, requests DESC, actual_cost DESC, user_id ASC",
		},
		{
			name:                 "requests",
			sortBy:               usage.UsageRankingSortByRequests,
			rawEligibility:       "COUNT(*) > 0",
			analyticsEligibility: "SUM(total_requests)",
			orderBy:              "requests DESC, total_tokens DESC, actual_cost DESC, user_id ASC",
		},
		{
			name:                 "actual cost",
			sortBy:               usage.UsageRankingSortByActualCost,
			rawEligibility:       "SUM(u.actual_cost)",
			analyticsEligibility: "SUM(actual_cost)",
			orderBy:              "actual_cost DESC, total_tokens DESC, requests DESC, user_id ASC",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rawEligibility, orderBy := usageRankingQueryParts(test.sortBy)
			require.Contains(t, rawEligibility, test.rawEligibility)
			require.Equal(t, test.orderBy, orderBy)
			require.Contains(t, usageRankingAnalyticsEligibility(test.sortBy), test.analyticsEligibility)
		})
	}
}
