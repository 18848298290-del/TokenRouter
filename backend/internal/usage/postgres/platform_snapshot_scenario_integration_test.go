//go:build integration

package postgres

// These scenarios cover usage_log_repo_insert.go, usage_log_repo_query.go, usage_log_repo_stats.go,
// and usage_analytics_aggregation_repo.go, comparing their platform attribution with Ops reports.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	opspg "github.com/TokenFlux/TokenRouter/internal/ops/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// TestPlatformSnapshotSurvivesProviderChanges checks that raw queries and both rollups retain the recorded platform.
func TestPlatformSnapshotSurvivesProviderChanges(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &identity.User{Email: "platform-snapshot@test.local"})
	group := mustCreateGroup(t, client, &routing.Group{Name: "mixed-platform-snapshot", RateMultiplier: 1})
	key := mustCreateApiKey(t, client, &apikey.APIKey{UserID: user.ID, Key: "platform-snapshot-key", Name: "snapshot", GroupID: &group.ID})
	provider := mustCreateProvider(t, client, &providercore.Record{Name: "snapshot-provider", Platform: "openai"})
	calendar := timezone.NewCalendar(time.UTC)
	repo := NewUsageLogRepositoryWithSQL(client, integrationDB, calendar)
	defer repo.StopUsageBatchers()
	start := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	t.Cleanup(func() {
		for _, table := range []string{"usage_analytics_hourly", "ops_metrics_hourly"} {
			_, err := integrationDB.ExecContext(ctx, "DELETE FROM "+table+" WHERE bucket_start=$1", start)
			require.NoError(t, err)
		}
	})
	log := &usage.UsageLog{UserID: user.ID, APIKeyID: key.ID, ProviderID: provider.ID, Platform: "openai", RequestID: "snapshot-openai", Model: "shared-model", GroupID: &group.ID, InputTokens: 12, ActualCost: 2, CreatedAt: start.Add(time.Minute)}
	inserted, err := repo.Create(ctx, log)
	require.NoError(t, err)
	require.True(t, inserted)
	// Anthropic usage within the same group must remain a separate platform total.
	other := *log
	other.RequestID, other.Platform, other.ActualCost = "snapshot-anthropic", "anthropic", 3
	inserted, err = repo.Create(ctx, &other)
	require.NoError(t, err)
	require.True(t, inserted)
	_, err = integrationDB.ExecContext(ctx, "UPDATE providers SET platform='gemini' WHERE id=$1", provider.ID)
	require.NoError(t, err)
	loaded, err := repo.GetByID(ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, "openai", loaded.Platform)
	raw, err := repo.GetBatchUserUsageStats(ctx, []int64{user.ID}, start, end)
	require.NoError(t, err)
	require.Len(t, raw[user.ID].ByPlatform, 2)
	for _, stat := range raw[user.ID].ByPlatform {
		require.NotEqual(t, "gemini", stat.Platform)
	}
	agg := NewAggregationStoreWithSQL(integrationDB, calendar)
	require.NoError(t, agg.AggregateUsageAnalyticsHourlyRange(ctx, start, end))
	var sum float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT SUM(actual_cost) FROM usage_analytics_hourly WHERE user_id=$1 AND platform='openai'", user.ID).Scan(&sum))
	require.Equal(t, 2.0, sum)

	opsRepo := opspg.NewOpsRepository(integrationDB)
	filter := &ops.OpsDashboardFilter{StartTime: start, EndTime: end, Platform: "openai"}
	trend, err := opsRepo.GetThroughputTrend(ctx, filter, 3600)
	require.NoError(t, err)
	require.Len(t, trend.Points, 1)
	require.Equal(t, int64(1), trend.Points[0].RequestCount)
	require.Len(t, trend.TopGroups, 1)
	require.Equal(t, group.ID, trend.TopGroups[0].GroupID)
	require.NoError(t, opsRepo.UpsertHourlyMetrics(ctx, start, end, nil))
	var count int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT success_count FROM ops_metrics_hourly WHERE bucket_start=$1 AND platform='openai' AND group_id=$2", start, group.ID).Scan(&count))
	require.Equal(t, trend.Points[0].RequestCount, count)
}
