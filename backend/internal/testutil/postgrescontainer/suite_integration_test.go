//go:build integration

package postgrescontainer

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestSuiteIsolation runs parallel clones with identical table names and
// committed and rolled-back rows. Each clone must retain one row, test cleanup
// must remove clones, and repeated Close must leave the container unavailable.
func TestSuiteIsolation(t *testing.T) {
	var suite Suite
	t.Cleanup(func() { require.NoError(t, suite.Close()) })
	t.Run("parallel", func(t *testing.T) {
		for i := range 4 {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				t.Parallel()
				db := suite.New(t)
				_, err := db.Exec("CREATE TABLE isolation_fixture (id integer PRIMARY KEY)")
				require.NoError(t, err)
				// Commit one row and roll back the next in each isolated clone. Identical
				// fixture IDs then expose accidental sharing while the final count checks transaction effects.
				tx, err := db.Begin()
				require.NoError(t, err)
				_, err = tx.Exec("INSERT INTO isolation_fixture VALUES (1)")
				require.NoError(t, err)
				require.NoError(t, tx.Commit())
				tx, err = db.Begin()
				require.NoError(t, err)
				_, err = tx.Exec("INSERT INTO isolation_fixture VALUES (2)")
				require.NoError(t, err)
				require.NoError(t, tx.Rollback())
				var total int
				require.NoError(t, db.QueryRow("SELECT count(*) FROM isolation_fixture").Scan(&total))
				require.Equal(t, 1, total)
			})
		}
	})
	var total int
	require.NoError(t, suite.admin.QueryRow("SELECT count(*) FROM pg_database WHERE datname ~ '^verification_[0-9]+$'").Scan(&total))
	require.Zero(t, total)
	container := suite.container
	require.NoError(t, suite.Close())
	require.NoError(t, suite.Close())
	_, err := container.State(context.Background())
	require.Error(t, err)
}

// TestSuiteMatchesMigrations compares a cloned database with an independently
// migrated container across recorded checksums, columns, indexes, and
// constraints. The clone must use UTC and the migrated template must reject
// connections.
func TestSuiteMatchesMigrations(t *testing.T) {
	var suite Suite
	t.Cleanup(func() { require.NoError(t, suite.Close()) })
	cloned := suite.New(t)
	fresh := New(t)
	queries := []string{
		"SELECT filename || ':' || checksum FROM schema_migrations ORDER BY filename",
		"SELECT table_name || ':' || column_name || ':' || data_type || ':' || is_nullable || ':' || coalesce(column_default,'') FROM information_schema.columns WHERE table_schema = 'public' ORDER BY table_name, ordinal_position",
		"SELECT tablename || ':' || indexname || ':' || indexdef FROM pg_indexes WHERE schemaname = 'public' ORDER BY tablename,indexname",
		"SELECT conrelid::regclass::text || ':' || conname || ':' || pg_get_constraintdef(oid) FROM pg_constraint WHERE connamespace = 'public'::regnamespace ORDER BY conrelid::regclass::text,conname",
	}
	for _, query := range queries {
		require.Equal(t, queryStrings(t, fresh, query), queryStrings(t, cloned, query))
	}
	var timezone string
	require.NoError(t, cloned.QueryRow("SHOW timezone").Scan(&timezone))
	require.Equal(t, "UTC", timezone)
	blocked, err := sql.Open("postgres", suite.databaseDSN("verification_template"))
	require.NoError(t, err)
	defer func() { require.NoError(t, blocked.Close()) }()
	require.Error(t, blocked.Ping())
}

// queryStrings collects one string column from query in result order for
// fixture comparisons. It fails the test on query, scan, iteration, or close
// errors, and empty result sets return nil.
func queryStrings(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	require.NoError(t, err)
	// Release rows on every return from this helper. Closing before fixture pool
	// cleanup lets schema comparison finish using the same database connection resources.
	defer func() { require.NoError(t, rows.Close()) }()
	var values []string
	for rows.Next() {
		var value string
		require.NoError(t, rows.Scan(&value))
		values = append(values, value)
	}
	require.NoError(t, rows.Err())
	return values
}

// TestSuiteDropWithOpenConnection opens and pings a clone before forcing its
// deletion. The database must disappear from pg_database even while the fixture
// pool still refers to the removed database.
func TestSuiteDropWithOpenConnection(t *testing.T) {
	var suite Suite
	t.Cleanup(func() { require.NoError(t, suite.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	require.NoError(t, suite.initialize(ctx, migrations.FS))
	_, err := suite.admin.ExecContext(ctx, "CREATE DATABASE verification_leak TEMPLATE verification_template")
	require.NoError(t, err)
	leaked, err := sql.Open("postgres", suite.databaseDSN("verification_leak"))
	require.NoError(t, err)
	// Forced deletion can terminate the opened connection before local pool cleanup.
	// Discard that cleanup error so the test's database-existence assertion determines the observed result.
	defer func() { _ = leaked.Close() }()
	require.NoError(t, leaked.PingContext(ctx))
	require.NoError(t, suite.drop(ctx, "verification_leak"))
	var exists bool
	require.NoError(t, suite.admin.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname='verification_leak')").Scan(&exists))
	require.False(t, exists)
}

// TestSuiteMigrationFailure initializes a template using a deliberately invalid
// SQL migration. Initialization must fail while retaining its container
// reference, and Close must make that container unavailable to later state
// lookup.
func TestSuiteMigrationFailure(t *testing.T) {
	var suite Suite
	t.Cleanup(func() { require.NoError(t, suite.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err := suite.initialize(ctx, fstest.MapFS{"001_broken.sql": &fstest.MapFile{Data: []byte("INVALID SQL;")}})
	require.Error(t, err)
	container := suite.container
	require.NotNil(t, container)
	require.NoError(t, suite.Close())
	_, err = container.State(ctx)
	require.Error(t, err)
}
