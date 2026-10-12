//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestUserActivityPartitionDrop checks partition deletion, rollback, and target validation in an isolated schema.
func TestUserActivityPartitionDrop(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`CREATE SCHEMA activity_partition_test;
		SET LOCAL search_path=activity_partition_test,public;
		CREATE TABLE users(id bigint PRIMARY KEY);
		CREATE TABLE usage_logs(user_id bigint,created_at timestamptz NOT NULL) PARTITION BY RANGE(created_at);
		CREATE INDEX ON usage_logs(user_id,created_at);
		CREATE TABLE usage_logs_202601 PARTITION OF usage_logs FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');
		CREATE TABLE usage_logs_202602 PARTITION OF usage_logs FOR VALUES FROM ('2026-02-01') TO ('2026-03-01');
		INSERT INTO users VALUES(1),(2)`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("289_usage_user_activity.sql")
	require.NoError(t, err)
	_, err = tx.Exec(string(migration))
	require.NoError(t, err)
	_, err = tx.Exec(`INSERT INTO usage_logs VALUES(1,'2026-01-10'),(1,'2026-02-10'),(2,'2026-01-20'); SAVEPOINT before_drop`)
	require.NoError(t, err)
	repo := NewAggregationStoreWithSQL(tx, timezone.NewCalendar(time.UTC))
	require.NoError(t, repo.CleanupUsageLogs(ctx, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)))
	var count int
	require.NoError(t, tx.QueryRow("SELECT COUNT(*) FROM usage_user_activity").Scan(&count))
	require.Equal(t, 1, count)
	_, err = tx.Exec("ROLLBACK TO SAVEPOINT before_drop")
	require.NoError(t, err)
	require.NoError(t, tx.QueryRow("SELECT COUNT(*) FROM usage_user_activity").Scan(&count))
	require.Equal(t, 2, count)
	_, err = tx.Exec("SELECT usage_drop_partition_with_activity('users')")
	require.ErrorContains(t, err, "直属分区")
}
