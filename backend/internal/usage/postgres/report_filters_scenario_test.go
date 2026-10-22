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

// report_filters_scenario_test.go checks usage_log_repo_stats.go and
// usage_log_repo_trend.go through their endpoint and distribution readers.
// Each query receives distinct identity values so missing or reordered selectors
// fail SQL expectations, including actor ownership and explicit range arguments.

// TestReportFiltersRawQueryArguments supplies every raw selector to endpoint,
// trend, model, and group reports. It checks SQL restrictions, parameter order,
// transport precedence, model dimensions, and allocated empty results.
func TestReportFiltersRawQueryArguments(t *testing.T) {
	start := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	ignoredStart := start.Add(-time.Hour)
	ignoredEnd := end.Add(time.Hour)
	requestType := int16(usage.RequestTypeWSV2)
	stream := false
	compaction := false
	billingType := int8(1)
	filters := UsageLogFilters{
		UserID: 101, APIKeyID: 303, ProviderID: 404, GroupID: 505, TeamID: 606,
		IncludeOwnedTeam: true, PersonalOnly: true, RequestID: "  request-7  ",
		Model: "  model-8  ", ModelFilterSource: usage.ModelSourceUpstream,
		RequestType: &requestType, Stream: &stream, NativeCompactionV2: &compaction,
		BillingType: &billingType, BillingMode: " custom ",
		StartTime: &ignoredStart, EndTime: &ignoredEnd, EndpointSource: "path", ExactTotal: true,
	}
	cases := []struct {
		name      string                                     // report reader case name
		alias     string                                     // expected raw table alias
		modelExpr string                                     // expected model matching expression
		columns   []string                                   // mocked result column names
		read      func(*Store, UsageLogFilters) (any, error) // report reader under test
	}{
		{
			name: "inbound", modelExpr: "COALESCE(NULLIF(TRIM(upstream_model), ''), model)",
			columns: []string{"endpoint", "requests", "tokens", "cost", "actual"},
			read: func(r *Store, f UsageLogFilters) (any, error) {
				return r.getEndpointStatsByColumnWithFilters(context.Background(), "inbound_endpoint", start, end, f)
			},
		},
		{
			name: "upstream", modelExpr: "COALESCE(NULLIF(TRIM(upstream_model), ''), model)",
			columns: []string{"endpoint", "requests", "tokens", "cost", "actual"},
			read: func(r *Store, f UsageLogFilters) (any, error) {
				return r.getEndpointStatsByColumnWithFilters(context.Background(), "upstream_endpoint", start, end, f)
			},
		},
		{
			name: "path", modelExpr: "COALESCE(NULLIF(TRIM(upstream_model), ''), model)",
			columns: []string{"endpoint", "requests", "tokens", "cost", "actual"},
			read: func(r *Store, f UsageLogFilters) (any, error) {
				return r.getEndpointPathStatsWithFilters(context.Background(), start, end, f)
			},
		},
		{
			name: "trend", modelExpr: "COALESCE(NULLIF(TRIM(upstream_model), ''), model)",
			columns: []string{"date", "requests", "input", "output", "creation", "read", "tokens", "cost", "actual"},
			read: func(r *Store, f UsageLogFilters) (any, error) {
				return r.GetUsageTrendWithUsageFilters(context.Background(), start, end, "hour", f)
			},
		},
		{
			name: "model", modelExpr: "COALESCE(NULLIF(TRIM(model), ''), NULLIF(TRIM(requested_model), ''), '')",
			columns: []string{"model", "requests", "input", "output", "creation", "read", "tokens", "cost", "actual", "provider"},
			read: func(r *Store, f UsageLogFilters) (any, error) {
				return r.GetModelStatsWithUsageFiltersBySource(context.Background(), start, end, f, usage.ModelSourceRequested)
			},
		},
		{
			name: "group", alias: "ul.", modelExpr: "COALESCE(NULLIF(TRIM(ul.model), ''), NULLIF(TRIM(ul.requested_model), ''), '')",
			columns: []string{"group_id", "name", "requests", "tokens", "cost", "actual", "provider"},
			read: func(r *Store, f UsageLogFilters) (any, error) {
				return r.GetGroupStatsWithUsageFilters(context.Background(), start, end, f)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, useRequestType := range []bool{true, false} {
				db, mock := newSQLMock(t)
				repo := &Store{sql: db}
				input := filters
				var transport any = requestType
				transportSQL := "(request_type = $11 OR (request_type = 0 AND openai_ws_mode = TRUE))"
				if !useRequestType {
					input.RequestType = nil
					transport = stream
					transportSQL = "stream = $11"
				}
				mock.ExpectQuery("(?s)SELECT tm.team_id FROM team_memberships tm").WithArgs(int64(101)).
					WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(202)))
				prefix := tc.alias
				conditions := "WHERE " + prefix + "created_at >= $1 AND " + prefix + "created_at < $2" +
					" AND (" + prefix + "request_id = ANY(ARRAY(SELECT id FROM request_lookup_ids($5))) OR (" +
					prefix + "upstream_request_id = $5 AND NOT EXISTS (SELECT 1 FROM request_records exact_request WHERE exact_request.request_id=$5)))" +
					" AND " + prefix + "api_key_id = $6 AND " + prefix + "provider_id = $7 AND " + prefix + "group_id = $8" +
					" AND " + prefix + "team_id = $9 AND " + prefix + "team_id IS NULL AND " + tc.modelExpr + " = $10" +
					" AND " + transportSQL + " AND " + prefix + "native_compaction_v2 = $12 AND " + prefix + "billing_type = $13" +
					" AND " + prefix + "billing_mode = $14"
				ownership := "SELECT * FROM usage_logs WHERE user_id = $3 UNION ALL SELECT * FROM usage_logs WHERE team_id = $4 AND user_id <> $3"
				mock.ExpectQuery("(?s)"+regexp.QuoteMeta(ownership)+".*"+regexp.QuoteMeta(conditions)+" GROUP BY").
					WithArgs(start, end, int64(101), int64(202), "request-7", int64(303), int64(404), int64(505), int64(606), "  model-8  ", transport, false, int16(1), "custom").
					WillReturnRows(sqlmock.NewRows(tc.columns))
				result, err := tc.read(repo, input)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Empty(t, result)
				require.Equal(t, "  request-7  ", input.RequestID)
				require.Equal(t, usage.ModelSourceUpstream, input.ModelFilterSource)
				require.NoError(t, mock.ExpectationsWereMet())
			}
		})
	}
}
