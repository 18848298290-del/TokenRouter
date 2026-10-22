//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// historical_request_queries_scenario_integration_test.go exercises
// ops_repo_request_details.go and ops_repo_trends.go together. Migrated tables
// supply the foreign-key and nullability rules while entity deletion separates
// saved request dimensions from current group names and provider platforms.

// TestHistoricalRequestQueriesKeepSavedDimensions records usage and errors
// before their referenced entities are soft-deleted or physically removed.
// Counts, pagination, identity filters, platform snapshots, and token totals
// remain stable; group-name drilldown reflects the remaining group rows.
func TestHistoricalRequestQueriesKeepSavedDimensions(t *testing.T) {
	ctx := t.Context()
	start := time.Date(2042, 10, 23, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	var userID, keyID int64
	groupIDs := make([]int64, 3)
	providerIDs := make([]int64, 3)
	// Remove logs before their referenced entities so foreign keys remain valid.
	// Cleanup includes the exclusive-end row and runs after failed assertions too.
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, err := integrationDB.ExecContext(cleanupCtx,
			"DELETE FROM usage_logs WHERE created_at >= $1 AND created_at <= $2", start, end)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(cleanupCtx,
			"DELETE FROM ops_error_logs WHERE created_at >= $1 AND created_at <= $2", start, end)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(cleanupCtx, "DELETE FROM api_keys WHERE id = $1", keyID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(cleanupCtx, "DELETE FROM providers WHERE id = ANY($1)", pq.Array(providerIDs))
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(cleanupCtx, "DELETE FROM groups WHERE id = ANY($1)", pq.Array(groupIDs))
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(cleanupCtx, "DELETE FROM users WHERE id = $1", userID)
		require.NoError(t, err)
	})

	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"INSERT INTO users(email, password_hash) VALUES('historical-query@test.local', 'fixture') RETURNING id").Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"INSERT INTO api_keys(user_id, key, name) VALUES($1, 'historical-query-key', 'historical query') RETURNING id",
		userID).Scan(&keyID))
	for i, state := range []string{"current", "retired", "deleted"} {
		name := "historical-query-" + state
		require.NoError(t, integrationDB.QueryRowContext(ctx,
			"INSERT INTO groups(name) VALUES($1) RETURNING id", name).Scan(&groupIDs[i]))
		require.NoError(t, integrationDB.QueryRowContext(ctx,
			"INSERT INTO providers(name, platform, type) VALUES($1, 'gemini', 'apikey') RETURNING id",
			name).Scan(&providerIDs[i]))
	}

	// Usage requires a provider but permits a missing group. All three rows use
	// recorded platforms that differ from the provider's current Gemini platform.
	for i, row := range []struct {
		requestID  string // saved request correlation ID
		providerID int64  // existing referenced provider ID
		groupID    any    // nullable referenced group ID
		platform   string // saved execution platform name
		tokenScale int    // multiplier for four token categories
		duration   int    // recorded duration in milliseconds
	}{
		{"usage-current", providerIDs[0], groupIDs[0], "openai", 1, 90},
		{"usage-retired", providerIDs[1], groupIDs[1], "openai", 10, 120},
		{"usage-null-group", providerIDs[0], nil, "anthropic", 100, 0},
	} {
		_, err := integrationDB.ExecContext(ctx, `
INSERT INTO usage_logs (
  user_id, billing_user_id, api_key_id, provider_id, group_id, request_id,
  platform, model, input_tokens, output_tokens, cache_creation_tokens,
  cache_read_tokens, duration_ms, created_at
) VALUES ($1, $1, $2, $3, $4, $5, $6, 'saved-model', $7, $8, $9, $10, $11, $12)`,
			userID, keyID, row.providerID, row.groupID, row.requestID, row.platform,
			row.tokenScale, 2*row.tokenScale, 3*row.tokenScale, 4*row.tokenScale,
			row.duration, start.Add(time.Duration(i+1)*time.Minute))
		require.NoError(t, err)
	}
	for i, row := range []struct {
		requestID  string // saved request correlation ID
		providerID any    // nullable saved provider identity
		groupID    any    // nullable saved group identity
		platform   string // saved execution platform name
		duration   any    // nullable duration in milliseconds
	}{
		{"error-current", providerIDs[0], groupIDs[0], "openai", nil},
		{"error-retired", providerIDs[1], groupIDs[1], "openai", 200},
		{"error-deleted", providerIDs[2], groupIDs[2], "openai", 50},
		{"error-null", nil, nil, "", nil},
		{"error-only-platform", nil, nil, "azure", 10},
	} {
		_, err := integrationDB.ExecContext(ctx, `
INSERT INTO ops_error_logs (
  request_id, user_id, api_key_id, provider_id, group_id, platform, model,
  error_phase, error_type, status_code, duration_ms, created_at
) VALUES ($1, $2, $3, $4, $5, $6, 'saved-model', 'request', 'api_error', 503, $7, $8)`,
			row.requestID, userID, keyID, row.providerID, row.groupID, row.platform,
			row.duration, start.Add(time.Duration(i+4)*time.Minute))
		require.NoError(t, err)
	}
	_, err := integrationDB.ExecContext(ctx, `
INSERT INTO ops_error_logs(request_id, platform, error_phase, error_type, status_code, created_at)
VALUES('error-exclusive-end', 'openai', 'request', 'api_error', 503, $1)`, end)
	require.NoError(t, err)

	repo := NewOpsRepository(integrationDB)
	filter := &ops.OpsRequestDetailFilter{StartTime: &start, EndTime: &end, Page: 1, PageSize: 20}
	before, beforeTotal, err := repo.ListRequestDetails(ctx, filter)
	require.NoError(t, err)
	require.EqualValues(t, 8, beforeTotal)
	trendFilter := &ops.OpsDashboardFilter{StartTime: start, EndTime: end}
	beforeTrend, err := repo.GetThroughputTrend(ctx, trendFilter, 60)
	require.NoError(t, err)

	// Soft deletion leaves usage foreign keys valid. Physical deletion targets
	// entities referenced only by error snapshots, whose IDs remain stored.
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET deleted_at = $1 WHERE id = $2", end, groupIDs[1])
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE providers SET deleted_at = $1 WHERE id = $2", end, providerIDs[1])
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id = $1", groupIDs[2])
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "DELETE FROM providers WHERE id = $1", providerIDs[2])
	require.NoError(t, err)

	after, total, err := repo.ListRequestDetails(ctx, filter)
	require.NoError(t, err)
	require.Equal(t, beforeTotal, total)
	require.Equal(t, before, after)
	requestIDs := make([]string, 0, len(after))
	for _, item := range after {
		requestIDs = append(requestIDs, item.RequestID)
		require.Equal(t, &userID, item.UserID)
		require.Equal(t, &keyID, item.APIKeyID)
		require.Equal(t, "saved-model", item.Model)
	}
	require.Equal(t, []string{
		"error-only-platform", "error-null", "error-deleted", "error-retired",
		"error-current", "usage-null-group", "usage-retired", "usage-current",
	}, requestIDs)
	require.Nil(t, after[1].ProviderID)
	require.Nil(t, after[1].GroupID)
	require.Nil(t, after[1].DurationMs)
	require.Equal(t, "unknown", after[1].Platform)
	require.Equal(t, &providerIDs[2], after[2].ProviderID)
	require.Equal(t, &groupIDs[2], after[2].GroupID)
	require.Nil(t, after[5].GroupID)
	require.NotNil(t, after[5].DurationMs)
	require.Zero(t, *after[5].DurationMs)

	for _, check := range []struct {
		name   string                     // selection case display name
		filter ops.OpsRequestDetailFilter // independent requested selection
		want   []*ops.OpsRequestDetail    // expected records in query order
		total  int64                      // expected count before pagination
	}{
		{"page", ops.OpsRequestDetailFilter{Page: 2, PageSize: 3}, after[3:6], 8},
		{"past-last-page", ops.OpsRequestDetailFilter{Page: 4, PageSize: 3}, []*ops.OpsRequestDetail{}, 8},
		{"current-group", ops.OpsRequestDetailFilter{GroupID: &groupIDs[0]}, []*ops.OpsRequestDetail{after[4], after[7]}, 2},
		{"retired-group", ops.OpsRequestDetailFilter{GroupID: &groupIDs[1]}, []*ops.OpsRequestDetail{after[3], after[6]}, 2},
		{"deleted-group", ops.OpsRequestDetailFilter{GroupID: &groupIDs[2]}, []*ops.OpsRequestDetail{after[2]}, 1},
		{"current-provider", ops.OpsRequestDetailFilter{ProviderID: &providerIDs[0]}, []*ops.OpsRequestDetail{after[4], after[5], after[7]}, 3},
		{"retired-provider", ops.OpsRequestDetailFilter{ProviderID: &providerIDs[1]}, []*ops.OpsRequestDetail{after[3], after[6]}, 2},
		{"deleted-provider", ops.OpsRequestDetailFilter{ProviderID: &providerIDs[2]}, []*ops.OpsRequestDetail{after[2]}, 1},
		{"saved-platform", ops.OpsRequestDetailFilter{Platform: "openai"}, []*ops.OpsRequestDetail{after[2], after[3], after[4], after[6], after[7]}, 5},
		{"current-platform", ops.OpsRequestDetailFilter{Platform: "gemini"}, []*ops.OpsRequestDetail{}, 0},
		{"unknown-platform", ops.OpsRequestDetailFilter{Platform: "unknown"}, []*ops.OpsRequestDetail{after[1]}, 1},
		{"duration", ops.OpsRequestDetailFilter{Sort: "duration_desc"}, []*ops.OpsRequestDetail{after[3], after[6], after[7], after[2], after[0], after[5], after[1], after[4]}, 8},
	} {
		t.Run(check.name, func(t *testing.T) {
			check.filter.StartTime = &start
			check.filter.EndTime = &end
			items, total, err := repo.ListRequestDetails(ctx, &check.filter)
			require.NoError(t, err)
			require.Equal(t, check.total, total)
			require.Equal(t, check.want, items)
		})
	}

	afterTrend, err := repo.GetThroughputTrend(ctx, trendFilter, 60)
	require.NoError(t, err)
	require.Equal(t, beforeTrend, afterTrend)
	require.Nil(t, afterTrend.TopGroups)
	require.ElementsMatch(t, []*ops.OpsThroughputPlatformBreakdownItem{
		{Platform: "openai", RequestCount: 5, TokenConsumed: 110},
		{Platform: "anthropic", RequestCount: 1, TokenConsumed: 1000},
		{Platform: "azure", RequestCount: 1, TokenConsumed: 0},
	}, afterTrend.ByPlatform)
	require.Equal(t, "openai", afterTrend.ByPlatform[0].Platform)
	var requestCount, tokens int64
	for _, point := range afterTrend.Points {
		requestCount += point.RequestCount
		tokens += point.TokenConsumed
	}
	require.EqualValues(t, 8, requestCount)
	require.EqualValues(t, 1110, tokens)

	trendFilter.Platform = "openai"
	groupTrend, err := repo.GetThroughputTrend(ctx, trendFilter, 60)
	require.NoError(t, err)
	require.Nil(t, groupTrend.ByPlatform)
	require.ElementsMatch(t, []*ops.OpsThroughputGroupBreakdownItem{
		{GroupID: groupIDs[0], GroupName: "historical-query-current", RequestCount: 2, TokenConsumed: 10},
		{GroupID: groupIDs[1], GroupName: "historical-query-retired", RequestCount: 2, TokenConsumed: 100},
		{GroupID: groupIDs[2], GroupName: "", RequestCount: 1, TokenConsumed: 0},
	}, groupTrend.TopGroups)
	trendFilter.GroupID = &groupIDs[0]
	selectedGroup, err := repo.GetThroughputTrend(ctx, trendFilter, 60)
	require.NoError(t, err)
	require.Nil(t, selectedGroup.ByPlatform)
	require.Nil(t, selectedGroup.TopGroups)

	emptyStart := end.Add(time.Minute)
	emptyEnd := emptyStart.Add(time.Minute)
	emptyItems, emptyTotal, err := repo.ListRequestDetails(ctx,
		&ops.OpsRequestDetailFilter{StartTime: &emptyStart, EndTime: &emptyEnd})
	require.NoError(t, err)
	require.NotNil(t, emptyItems)
	require.Empty(t, emptyItems)
	require.Zero(t, emptyTotal)
	emptyTrend, err := repo.GetThroughputTrend(ctx,
		&ops.OpsDashboardFilter{StartTime: emptyStart, EndTime: emptyEnd}, 60)
	require.NoError(t, err)
	require.NotNil(t, emptyTrend.ByPlatform)
	require.Empty(t, emptyTrend.ByPlatform)
	require.Nil(t, emptyTrend.TopGroups)
}
