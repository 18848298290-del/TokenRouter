//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestUsagePerformanceMigration preserves populated heap and partition storage while
// retiring historical latency. Lock contention must roll back within the configured
// budget, and retrying after the lock releases must apply the same migration.
func TestUsagePerformanceMigration(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("performance"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	// Run may return a container alongside a startup error. Register cleanup
	// before checking the error, and give termination its own deadline so test
	// cancellation still allows Docker to remove the partially started instance.
	if pg != nil {
		t.Cleanup(func() {
			cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancelCleanup()
			if err := pg.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate PostgreSQL container: %v", err)
			}
		})
	}
	require.NoError(t, err)
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	content, err := migrations.FS.ReadFile("298_unify_usage_performance.sql")
	require.NoError(t, err)

	for _, partitioned := range []bool{false, true} {
		t.Run(fmt.Sprintf("partitioned_%t", partitioned), func(t *testing.T) {
			name := fmt.Sprintf("fixture_%t", partitioned)
			conn, err := db.Conn(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, conn.Close()) })
			_, err = conn.ExecContext(ctx, "CREATE SCHEMA "+name+"; SET search_path TO "+name)
			require.NoError(t, err)
			createPerformanceMigrationFixture(t, conn, partitioned)
			before := performanceRelationFiles(t, conn, name)
			if partitioned {
				_, err = conn.ExecContext(ctx, "INSERT INTO settings VALUES('ttft_mode','first_output')")
				require.NoError(t, err)
			}

			// Hold the usage relation through another connection. The migration must
			// fail atomically instead of waiting behind a busy production writer indefinitely.
			locker, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			_, err = locker.ExecContext(ctx, "LOCK TABLE "+name+".usage_logs IN ACCESS EXCLUSIVE MODE")
			require.NoError(t, err)
			blocked, err := conn.BeginTx(ctx, nil)
			require.NoError(t, err)
			started := time.Now()
			_, err = blocked.ExecContext(ctx, string(content))
			require.Error(t, err)
			var postgresError *pq.Error
			require.ErrorAs(t, err, &postgresError)
			require.Equal(t, pq.ErrorCode("55P03"), postgresError.Code)
			require.Less(t, time.Since(started), 5*time.Second)
			require.NoError(t, blocked.Rollback())
			require.NoError(t, locker.Rollback())

			scopedDSN, err := pg.ConnectionString(ctx, "sslmode=disable", "search_path="+name)
			require.NoError(t, err)
			scopedDB, err := sql.Open("postgres", scopedDSN)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, scopedDB.Close()) })
			migrationFS := fstest.MapFS{"298_unify_usage_performance.sql": &fstest.MapFile{Data: content}}
			require.NoError(t, postgres.ApplyMigrations(ctx, scopedDB, migrationFS))
			require.NoError(t, postgres.ApplyMigrations(ctx, scopedDB, migrationFS))
			require.Equal(t, before, performanceRelationFiles(t, conn, name))
			var count, markers, timed int64
			require.NoError(t, conn.QueryRowContext(ctx, "SELECT count(*),sum(marker),count(timings) FROM usage_logs").Scan(&count, &markers, &timed))
			require.Equal(t, int64(5000), count)
			require.Equal(t, int64(12502500), markers)
			require.Zero(t, timed)
			var mode string
			require.NoError(t, conn.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='ttft_mode'").Scan(&mode))
			if partitioned {
				require.Equal(t, "first_output", mode)
			} else {
				require.Equal(t, "first_event", mode)
			}
			for _, table := range []string{"ops_system_metrics", "ops_metrics_hourly", "ops_metrics_daily"} {
				var retained int
				var latency sql.NullInt64
				require.NoError(t, conn.QueryRowContext(ctx, "SELECT retained,ttft_p50_ms FROM "+table).Scan(&retained, &latency))
				require.Equal(t, 42, retained)
				require.False(t, latency.Valid)
			}
		})
	}
}

// createPerformanceMigrationFixture creates populated relations matching only the
// columns touched by the migration. Each subtest owns its connection search path;
// generated markers detect accidental historical usage updates independently of row count.
func createPerformanceMigrationFixture(t *testing.T, conn *sql.Conn, partitioned bool) {
	t.Helper()
	ctx := context.Background()
	partition := ""
	if partitioned {
		partition = " PARTITION BY RANGE(id)"
	}
	_, err := conn.ExecContext(ctx, "CREATE TABLE usage_logs(id BIGINT,marker BIGINT,first_token_ms INT)"+partition)
	require.NoError(t, err)
	if partitioned {
		_, err = conn.ExecContext(ctx, "CREATE TABLE usage_p0 PARTITION OF usage_logs FOR VALUES FROM (0) TO (2501); CREATE TABLE usage_p1 PARTITION OF usage_logs FOR VALUES FROM (2501) TO (10000)")
		require.NoError(t, err)
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO usage_logs SELECT n,n,123 FROM generate_series(1,5000) n; CREATE TABLE ops_error_logs(time_to_first_token_ms BIGINT); CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO settings VALUES('openai_ttft_mode','semantic')")
	require.NoError(t, err)
	for _, table := range []string{"ops_system_metrics", "ops_metrics_hourly", "ops_metrics_daily"} {
		_, err = conn.ExecContext(ctx, "CREATE TABLE "+table+"(retained INT,ttft_p50_ms INT,ttft_p90_ms INT,ttft_p95_ms INT,ttft_p99_ms INT,ttft_avg_ms DOUBLE PRECISION,ttft_max_ms INT,ttft_sample_count BIGINT NOT NULL DEFAULT 0); INSERT INTO "+table+"(retained,ttft_p50_ms) VALUES(42,123)")
		require.NoError(t, err)
	}
}

// performanceRelationFiles reads physical relation identities within the fixture schema.
// Metadata-only changes retain each populated heap and partition file, allowing the
// test to detect a rewrite without constructing tens of millions of rows.
func performanceRelationFiles(t *testing.T, conn *sql.Conn, schema string) map[string]int64 {
	t.Helper()
	rows, err := conn.QueryContext(context.Background(), "SELECT c.relname,c.relfilenode::bigint FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind='r' AND (c.relname='usage_logs' OR c.relname LIKE 'usage_p%' OR c.relname LIKE 'ops_%' OR c.relname='settings')", schema)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	result := make(map[string]int64)
	for rows.Next() {
		var name string
		var file int64
		require.NoError(t, rows.Scan(&name, &file))
		result[name] = file
	}
	require.NoError(t, rows.Err())
	return result
}
