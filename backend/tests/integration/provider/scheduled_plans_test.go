//go:build integration

package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// Scheduled provider tests store the next cron occurrence and retain a bounded
// result history. PostgreSQL assertions compare the due-time edge, trim three
// stored results to two, update the plan schedule, and check that deleting the
// plan removes its results.

// TestScheduledPlanStorageContract creates a cron plan and queries immediately
// before and at its next run. PostgreSQL retains two of three results, updates
// the disabled plan's schedule, and removes results when the plan is deleted.
func TestScheduledPlanStorageContract(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	row, err := client.Provider.Create().SetName("test-scheduled-fixture").SetPlatform(provider.PlatformOpenAI).SetType(provider.ProviderTypeAPIKey).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
	plans := providerpostgres.NewScheduledTestPlanRepository(integrationDB)
	results := providerpostgres.NewScheduledTestResultRepository(integrationDB)
	now := time.Date(2026, 9, 13, 10, 2, 0, 0, time.UTC)
	svc := provider.NewScheduledTestService(plans, results, provider.ScheduledTestOptions{Now: func() time.Time { return now }, NextRun: provideradapter.NextScheduledTestRun})
	plan, err := svc.CreatePlan(ctx, &provider.ScheduledTestPlan{ProviderID: row.ID, ModelID: "fixture-model", CronExpression: "*/5 * * * *", Enabled: true})
	require.NoError(t, err)
	require.Equal(t, 50, plan.MaxResults)
	require.NotNil(t, plan.NextRunAt)
	// Use PostgreSQL's stored microsecond precision for the before-due comparison,
	// keeping that request distinct from the exact next-run timestamp checked afterward.
	before, err := plans.ListDue(ctx, plan.NextRunAt.Add(-time.Microsecond))
	require.NoError(t, err)
	for _, p := range before {
		require.NotEqual(t, plan.ID, p.ID)
	}
	due, err := plans.ListDue(ctx, *plan.NextRunAt)
	require.NoError(t, err)
	found := false
	for _, p := range due {
		found = found || p.ID == plan.ID
	}
	require.True(t, found)
	for range 3 {
		err = svc.SaveResult(ctx, plan.ID, 2, &provider.ScheduledTestResult{Status: "success", ResponseText: "fixture", StartedAt: now, FinishedAt: now.Add(time.Second), LatencyMs: 1000})
		require.NoError(t, err)
	}
	retained, err := svc.ListResults(ctx, plan.ID, 0)
	require.NoError(t, err)
	require.Len(t, retained, 2)
	require.Equal(t, "fixture", retained[0].ResponseText)
	plan.CronExpression = "0 * * * *"
	plan.Enabled = false
	plan, err = svc.UpdatePlan(ctx, plan)
	require.NoError(t, err)
	require.False(t, plan.Enabled)
	require.Equal(t, 0, plan.NextRunAt.Minute())
	require.NoError(t, svc.DeletePlan(ctx, plan.ID))
	retained, err = svc.ListResults(ctx, plan.ID, 50)
	require.NoError(t, err)
	require.Empty(t, retained)
}
