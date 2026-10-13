//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/migrations"
)

// User API-key limits are introduced on a minimal users table that shadows the
// shared schema for this transaction. Existing rows and later inserts receive
// 100, while separate savepoints keep rejected negative and NULL writes from
// preventing the next constraint check.

// TestMigration213_AddsUserAPIKeyLimitWithDataAndConstraints applies migration
// 213 to a pre-column users fixture. It checks the existing-row value, the
// insert default, and rejection of negative and NULL values.
func TestMigration213_AddsUserAPIKeyLimitWithDataAndConstraints(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)

	// The temporary users table takes precedence for unqualified SQL, so
	// this migration can add its column even though the shared public table
	// already has it.
	_, err := tx.ExecContext(ctx, `CREATE TEMP TABLE users (id BIGSERIAL PRIMARY KEY) ON COMMIT DROP`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO users DEFAULT VALUES`)
	require.NoError(t, err)

	migrationSQL, err := migrations.FS.ReadFile("213_add_user_api_key_limit.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	var migratedValue int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT api_key_limit FROM users WHERE id = 1`).Scan(&migratedValue))
	require.Equal(t, 100, migratedValue)

	var defaultValue int
	require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO users DEFAULT VALUES RETURNING api_key_limit`).Scan(&defaultValue))
	require.Equal(t, 100, defaultValue)

	// An expected PostgreSQL constraint error aborts the transaction until
	// the savepoint is restored; isolate each rejected write so the NULL
	// check still runs.
	_, err = tx.ExecContext(ctx, `SAVEPOINT api_key_limit_negative`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO users (api_key_limit) VALUES (-1)`)
	require.Error(t, err)
	_, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT api_key_limit_negative`)
	require.NoError(t, rollbackErr)

	_, err = tx.ExecContext(ctx, `SAVEPOINT api_key_limit_null`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO users (api_key_limit) VALUES (NULL)`)
	require.Error(t, err)
	_, rollbackErr = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT api_key_limit_null`)
	require.NoError(t, rollbackErr)
}
