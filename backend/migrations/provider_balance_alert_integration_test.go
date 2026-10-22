//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestProviderBalanceAlertMigration applies the full schema and repeats the reminder migration.
// Existing migration checksums remain accepted, the new table and index can be
// recreated idempotently, and the migration runner records one application.
func TestProviderBalanceAlertMigration(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("balance_alert"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
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
	// Register pool cleanup after container cleanup so database connections close
	// before the underlying PostgreSQL process is removed by the test framework.
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	raw, err := migrations.FS.ReadFile("297_provider_balance_alert_state.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(raw))
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations WHERE filename='297_provider_balance_alert_state.sql'").Scan(&count))
	require.Equal(t, 1, count)
}
