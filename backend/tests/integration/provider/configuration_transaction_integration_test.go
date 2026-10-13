//go:build integration

package provider_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Configuration updates and their scheduler event share an Ent transaction.
// PostgreSQL assertions compare the pending write inside an outer transaction
// with its state after rollback, and inject a failing outbox statement to check
// that the configuration remains unchanged.

// TestConfigurationTransactionAndOutbox writes a name change inside an outer
// transaction or through a failing outbox adapter. Pending configuration and
// event rows disappear on rollback, and outside reads retain the original name
// and quota value.
func TestConfigurationTransactionAndOutbox(t *testing.T) {
	for _, mode := range []string{"outer_rollback", "outbox_failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			row, err := client.Provider.Create().SetName("before-config").SetPlatform(capability.PlatformOpenAI).
				SetType(capability.ProviderTypeAPIKey).SetExtra(map[string]any{"quota_used": 2.0}).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
			store := providerpostgres.NewProviderStore(client, integrationDB, providerpostgres.ProviderStoreOptions{OllamaIdentity: acctcore.IsOllamaCloudUsageProvider, Events: providerEventsFixture{}})
			var tx *dbent.Tx
			if mode == "outer_rollback" {
				tx, err = client.Tx(ctx)
				require.NoError(t, err)
				defer func() { _ = tx.Rollback() }()
				ctx = dbent.NewTxContext(ctx, tx)
			} else {
				store.SetEvents(failConfigurationOutbox{})
			}
			value := &acctcore.Record{ID: row.ID, Name: "pending-config"}
			err = store.UpdateConfiguration(ctx, value, acctcore.ConfigurationChange{Fields: acctcore.ConfigName})
			if mode == "outer_rollback" {
				require.NoError(t, err)
				inside, err := tx.Client().Provider.Get(ctx, row.ID)
				require.NoError(t, err)
				require.Equal(t, "pending-config", inside.Name)
				var count int
				rows, err := tx.Client().QueryContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID)
				require.NoError(t, err)
				require.True(t, rows.Next())
				require.NoError(t, rows.Scan(&count))
				require.NoError(t, rows.Close())
				require.Equal(t, 1, count)
				// The pending row and event were visible through tx.Client; rolling
				// back now lets the separate client verify that both writes disappear.
				require.NoError(t, tx.Rollback())
			} else {
				require.ErrorContains(t, err, "test_missing_outbox_fixture")
			}
			outside, err := client.Provider.Get(context.Background(), row.ID)
			require.NoError(t, err)
			require.Equal(t, "before-config", outside.Name)
			require.Equal(t, 2.0, outside.Extra["quota_used"])
			var count int
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT count(*) FROM scheduler_outbox WHERE provider_id=$1", row.ID).Scan(&count))
			require.Zero(t, count)
		})
	}
}
