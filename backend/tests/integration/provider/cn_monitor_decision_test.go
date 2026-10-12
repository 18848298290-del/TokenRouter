//go:build integration

package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
)

// TestCNMonitorDecisionCAS checks PostgreSQL version guards, identity-matched recovery, stale observations, and
// best-effort notification.
func TestCNMonitorDecisionCAS(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	row, err := client.Provider.Create().SetName("cn-monitor-cas").SetPlatform(acctcore.PlatformKimi).SetType(acctcore.ProviderTypeAPIKey).SetCredentials(map[string]any{"api_key": "old-fixture"}).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
	store := providerpostgres.NewProviderStore(client, integrationDB, providerpostgres.ProviderStoreOptions{Events: providerEventsFixture{}})
	count := func() int {
		t.Helper()
		var n int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&n))
		return n
	}
	before := count()
	until := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	reason := "cn_usage_monitor:fixture: 余额低于监控阈值"
	_, err = integrationDB.ExecContext(ctx, `UPDATE providers SET credentials='{"api_key":"new-fixture"}'::jsonb,updated_at=updated_at+interval '1 second' WHERE id=$1`, row.ID)
	require.NoError(t, err)
	wrote, err := store.SetCNUsageDecisionCAS(ctx, row.ID, row.UpdatedAt, until, reason, false)
	require.NoError(t, err)
	require.False(t, wrote)
	require.Equal(t, before, count())
	current, err := client.Provider.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Nil(t, current.TempUnschedulableUntil)
	wrote, err = store.SetCNUsageDecisionCAS(ctx, row.ID, current.UpdatedAt, until, reason, false)
	require.NoError(t, err)
	require.True(t, wrote)
	require.Equal(t, before+1, count())
	current, err = client.Provider.Get(ctx, row.ID)
	require.NoError(t, err)
	require.True(t, until.Equal(*current.TempUnschedulableUntil))
	wrote, err = store.SetCNUsageDecisionCAS(ctx, row.ID, current.UpdatedAt, until.Add(-time.Minute), reason, false)
	require.NoError(t, err)
	require.False(t, wrote)
	wrote, err = store.SetCNUsageDecisionCAS(ctx, row.ID, current.UpdatedAt, time.Time{}, "other-monitor", true)
	require.NoError(t, err)
	require.False(t, wrote)
	// A matching timestamp still requires a matching reason to protect another monitor's suspension.
	_, err = integrationDB.ExecContext(ctx, "UPDATE providers SET temp_unschedulable_reason='manual-cooldown' WHERE id=$1", row.ID)
	require.NoError(t, err)
	wrote, err = store.SetCNUsageDecisionCAS(ctx, row.ID, current.UpdatedAt, time.Time{}, reason, true)
	require.NoError(t, err)
	require.False(t, wrote)
	_, err = integrationDB.ExecContext(ctx, "UPDATE providers SET temp_unschedulable_reason=$2 WHERE id=$1", row.ID, reason)
	require.NoError(t, err)
	// An outbox failure leaves the independently committed health write intact.
	store.SetEvents(failConfigurationOutbox{})
	wrote, err = store.SetCNUsageDecisionCAS(ctx, row.ID, current.UpdatedAt, time.Time{}, reason, true)
	require.NoError(t, err)
	require.True(t, wrote)
	current, err = client.Provider.Get(ctx, row.ID)
	require.NoError(t, err)
	require.Nil(t, current.TempUnschedulableUntil)
	require.Equal(t, "new-fixture", current.Credentials["api_key"])
	require.Equal(t, before+1, count())
}
