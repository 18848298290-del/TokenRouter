//go:build integration

package migrations_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
)

// Scheduler overrides are sparse JSON objects, with an empty object supplied
// for a newly inserted group. The pre-239 database lets the test add the
// column, repeat the SQL, and recover from a rejected array before checking
// that an object containing explicit zero values can be stored.

// TestMigration239AddsGroupAdvancedSchedulerOverrides starts before migration
// 239 and executes it twice. It checks the empty-object default, rejection of
// an array, and acceptance of an object with an explicit zero weight.
func TestMigration239AddsGroupAdvancedSchedulerOverrides(t *testing.T) {
	ctx := context.Background()
	migrationSQL, err := dbmigrations.FS.ReadFile("239_add_group_advanced_scheduler_overrides.sql")
	require.NoError(t, err)
	tx := historicalTx(t, "239_")

	// The first execution adds the column to the pre-239 schema. Repeating
	// it exercises the guards around column and constraint creation before
	// inserting a group.
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	name := fmt.Sprintf("migration-239-%d", time.Now().UnixNano())
	var groupID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO groups (name, platform)
VALUES ($1, 'gemini')
RETURNING id
`, name).Scan(&groupID))

	var stored string
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT advanced_scheduler_overrides::text
FROM groups
WHERE id = $1
`, groupID).Scan(&stored))
	require.JSONEq(t, `{}`, stored)

	// The array write deliberately violates the object constraint. Restore
	// and release its savepoint so the following object write can use the
	// same transaction.
	_, err = tx.ExecContext(ctx, "SAVEPOINT invalid_advanced_scheduler_overrides")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
UPDATE groups
SET advanced_scheduler_overrides = '[]'::jsonb
WHERE id = $1
`, groupID)
	require.Error(t, err, "数组不能作为分组高级调度器覆盖")
	_, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT invalid_advanced_scheduler_overrides")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "RELEASE SAVEPOINT invalid_advanced_scheduler_overrides")
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, `
UPDATE groups
SET advanced_scheduler_overrides = '{"lb_top_k":3,"weight_queue":0}'::jsonb
WHERE id = $1
`, groupID)
	require.NoError(t, err)
}
