//go:build integration

package postgres

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

// Recorded usage platform remains attached to the request when its provider
// later changes platform. OpenAI and Anthropic rows share one group in this
// fixture, exposing accidental attribution through mutable provider metadata.
// Raw usage, usage hourly rollups and Ops hourly metrics are compared after the
// provider becomes Gemini.

// TestPlatformSnapshotSurvivesProviderChanges stores two platform snapshots in
// the same group, then changes the provider to Gemini. The saved OpenAI row
// and two non-Gemini raw groups survive; OpenAI usage hourly cost and Ops
// hourly counts agree with the recorded request.
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
	// Remove this scenario's hourly rollup rows before shared-database cleanup
	// restarts user identities, so later fixtures cannot inherit its stored totals.
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
	// Give the second request the same provider and group but a different recorded
	// platform, so metadata-based regrouping would merge or relabel the fixture.
	other := *log
	other.RequestID, other.Platform, other.ActualCost = "snapshot-anthropic", "anthropic", 3
	inserted, err = repo.Create(ctx, &other)
	require.NoError(t, err)
	require.True(t, inserted)
	// Change current provider metadata before any raw report or hourly rebuild;
	// attribution must come from the platform saved on each usage row.
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
