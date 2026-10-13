//go:build integration

package provider_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
)

// Membership priority is removed by migration 240, leaving provider priority to
// order capacity results. The shared database already has every migration, so
// one SQL transaction reconstructs the legacy membership table, column, and
// priority indexes before running the embedded migration twice. The column and
// index queries check that both executions leave the retired structures absent.
//
// Capacity queries use the current membership representation after the migration
// checks. Fixture rollback restores the shared schema and removes inserted rows
// when the test completes. Provider and membership insertion orders deliberately
// differ, allowing the result assertions to distinguish global priority from
// membership order and to check provider ID as the tie-breaker.

// TestMigration240RemovesProviderGroupPriority rebuilds the legacy membership
// schema and executes the embedded migration twice in one transaction. The
// priority column and indexes disappear, and capacity results follow provider
// priority with ID tie-breaking.
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

	// Return the legacy table and column to current names before invoking
	// provider storage, which queries provider_groups while the transaction still owns schema changes.
	_, err = tx.ExecContext(ctx, `ALTER TABLE account_groups RENAME COLUMN account_id TO provider_id; ALTER TABLE account_groups RENAME TO provider_groups`)
	require.NoError(t, err)

	var groupID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO groups (name)
VALUES ('migration-240-capacity-order')
RETURNING id
`).Scan(&groupID))

	// Insert the larger numeric priority first so ID order differs from
	// provider-priority order, then add equal priorities to exercise the ID tie-break.
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

	// Bind the equal-priority providers in reverse ID order so membership
	// insertion order cannot satisfy the expected provider-priority and ID sequence.
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

// testTx begins a SQL transaction and registers an asserted rollback at test
// cleanup. Schema changes and fixture rows remain inside that transaction so
// the shared migrated database retains its current schema afterward.
func testTx(t *testing.T) *sql.Tx {
	t.Helper()
	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tx.Rollback()) })
	return tx
}
