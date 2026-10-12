//go:build integration

package provider_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
)

// TestMigration240RemovesProviderGroupPriority checks complete and repeatable removal of membership priority.
func TestMigration240RemovesProviderGroupPriority(t *testing.T) {
	ctx := context.Background()
	migrationSQL, err := dbmigrations.FS.ReadFile("240_remove_account_group_priority.sql")
	require.NoError(t, err)
	tx := testTx(t)

	// Restore the pre-migration schema inside a transaction because the test database already has all migrations.
	_, err = tx.ExecContext(ctx, `
ALTER TABLE provider_groups RENAME TO account_groups;
ALTER TABLE account_groups RENAME COLUMN provider_id TO account_id;
ALTER TABLE account_groups
    ADD COLUMN priority INTEGER NOT NULL DEFAULT 50;
CREATE INDEX idx_account_groups_priority
    ON account_groups (priority);
CREATE INDEX idx_account_groups_group_priority_account
    ON account_groups (group_id, priority, account_id);
CREATE INDEX idx_account_groups_account_priority_group
    ON account_groups (account_id, priority, group_id);
`)
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	var columnCount int
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'account_groups'
  AND column_name = 'priority'
`).Scan(&columnCount))
	require.Zero(t, columnCount)

	var indexCount int
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM pg_indexes
WHERE schemaname = current_schema()
  AND indexname IN (
      'idx_account_groups_priority',
      'idx_account_groups_group_priority_account',
      'idx_account_groups_account_priority_group'
  )
`).Scan(&indexCount))
	require.Zero(t, indexCount)

	// Restore current table names before checking repository ordering.
	_, err = tx.ExecContext(ctx, `ALTER TABLE account_groups RENAME COLUMN account_id TO provider_id; ALTER TABLE account_groups RENAME TO provider_groups`)
	require.NoError(t, err)

	var groupID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO groups (name)
VALUES ('migration-240-capacity-order')
RETURNING id
`).Scan(&groupID))

	// Insert lower priority first so ID and priority order differ; equal priorities must sort by ID.
	insertProvider := func(name string, priority int) int64 {
		t.Helper()
		var providerID int64
		require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO providers (name, platform, type, priority)
VALUES ($1, 'openai', 'apikey', $2)
RETURNING id
`, name, priority).Scan(&providerID))
		return providerID
	}
	lowPriorityID := insertProvider("migration-240-low-priority", 20)
	highPriorityFirstID := insertProvider("migration-240-high-priority-first", 10)
	highPrioritySecondID := insertProvider("migration-240-high-priority-second", 10)

	// Insert memberships out of order to check that capacity queries use provider priority.
	for _, providerID := range []int64{lowPriorityID, highPrioritySecondID, highPriorityFirstID} {
		_, err = tx.ExecContext(ctx, `
INSERT INTO provider_groups (provider_id, group_id)
VALUES ($1, $2)
`, providerID, groupID)
		require.NoError(t, err)
	}

	repo := newProviderStoreContract(nil, tx, nil)
	capacityRows, err := repo.ListSchedulableCapacityByGroupIDs(ctx, []int64{groupID})
	require.NoError(t, err)
	require.Len(t, capacityRows, 3)
	require.Equal(t, []int64{highPriorityFirstID, highPrioritySecondID, lowPriorityID}, []int64{
		capacityRows[0].ProviderID,
		capacityRows[1].ProviderID,
		capacityRows[2].ProviderID,
	})
}

// testTx creates a transaction whose schema changes roll back when the test ends.
func testTx(t *testing.T) *sql.Tx {
	t.Helper()
	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tx.Rollback()) })
	return tx
}
