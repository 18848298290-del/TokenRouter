//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestMigration280RemovesMessagesOverride checks replay and retention of general group mappings and identity
// after override removal.
func TestMigration280RemovesMessagesOverride(t *testing.T) {
	ctx := context.Background()
	tx := historicalTx(t, "280_")
	_, err := tx.ExecContext(ctx, `INSERT INTO groups(id,name,messages_dispatch_model_config,routing_policy) VALUES (98001,'mapped group','{"exact_model_mappings":{"claude-sonnet-4-6":"old-target"}}','{"enabled":true,"model_mapping":{"public":"current-target"}}')`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("280_remove_messages_dispatch_model_config.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	var exists bool
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='groups' AND column_name='messages_dispatch_model_config')`).Scan(&exists))
	require.False(t, exists)
	var policy, name string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT routing_policy::text,name FROM groups WHERE id=98001`).Scan(&policy, &name))
	require.Equal(t, "mapped group", name)
	require.JSONEq(t, `{"enabled":true,"model_mapping":{"public":"current-target"}}`, policy)
}
