//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestApplyMigrationsRequestLookupUpgrade restores the older lookup function and
// an invalid concurrent audit index in PostgreSQL. It checks the lookup function's
// PL/pgSQL language, restored audit index validity and column order, unchanged
// billing index identity, and reuse of the valid audit index during later replay.
func TestApplyMigrationsRequestLookupUpgrade(t *testing.T) {
	ctx := t.Context()
	image := strings.TrimSpace(os.Getenv("TOKENROUTER_TEST_POSTGRES_IMAGE"))
	if image == "" {
		image = "postgres:18.1-alpine3.23"
	}
	container, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("migration_upgrade"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies())
	// Run may return a container alongside a startup error. Register cleanup
	// before checking the error, and give termination its own deadline so test
	// cancellation still allows Docker to remove the partially started instance.
	if container != nil {
		t.Cleanup(func() {
			cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancelCleanup()
			if err := container.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate PostgreSQL container: %v", err)
			}
		})
	}
	require.NoError(t, err)
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	// Register pool cleanup last so it runs before container termination,
	// releasing this fixture's SQL connections while PostgreSQL is still available.
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	var billingIndex uint32
	require.NoError(t, db.QueryRowContext(ctx, `SELECT 'idx_usage_logs_billing_key_api_key'::regclass::oid`).Scan(&billingIndex))
	// Restore migration 294's lookup implementation and remove the two follow-up
	// records. The runner then encounters function upgrade and index recovery
	// as pending work in an otherwise initialized database.
	previous, err := migrations.FS.ReadFile("294_request_lookup_child_billing.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(previous))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
DELETE FROM schema_migrations WHERE filename IN ('295_request_lookup_stages.sql','296_audit_request_id_index_notx.sql');
DROP INDEX idx_audit_logs_request_id_created_at;
INSERT INTO audit_logs(request_id,status_code)
SELECT 'audit-'||(g % 10000),200 FROM generate_series(1,50000) g;`)
	require.NoError(t, err)
	// Duplicate request IDs make concurrent unique-index creation fail with an
	// invalid catalog entry. That artifact exercises the retry path used to
	// replace an unfinished audit index during upgrade.
	_, err = db.ExecContext(ctx, `CREATE UNIQUE INDEX CONCURRENTLY idx_audit_logs_request_id_created_at ON audit_logs(request_id)`)
	var duplicate *pq.Error
	require.ErrorAs(t, err, &duplicate)
	require.Equal(t, pq.ErrorCode("23505"), duplicate.Code)
	var valid bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid='idx_audit_logs_request_id_created_at'::regclass`).Scan(&valid))
	require.False(t, valid)

	started := time.Now()
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	t.Logf("5 万条审计记录的函数升级和索引恢复耗时：%s", time.Since(started))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid='idx_audit_logs_request_id_created_at'::regclass`).Scan(&valid))
	require.True(t, valid)
	var definition string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT pg_get_indexdef('idx_audit_logs_request_id_created_at'::regclass)`).Scan(&definition))
	require.Contains(t, definition, "(request_id, created_at DESC)")
	var language string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT l.lanname FROM pg_proc p JOIN pg_language l ON l.oid=p.prolang WHERE p.oid='request_lookup_ids(text)'::regprocedure`).Scan(&language))
	require.Equal(t, "plpgsql", language)
	var currentBillingIndex uint32
	require.NoError(t, db.QueryRowContext(ctx, `SELECT 'idx_usage_logs_billing_key_api_key'::regclass::oid`).Scan(&currentBillingIndex))
	require.Equal(t, billingIndex, currentBillingIndex)

	// Remove the migration record while keeping the valid audit index. Two
	// subsequent runner calls must retain its OID instead of rebuilding it
	// when the migration is encountered again.
	var auditIndex uint32
	require.NoError(t, db.QueryRowContext(ctx, `SELECT 'idx_audit_logs_request_id_created_at'::regclass::oid`).Scan(&auditIndex))
	_, err = db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE filename='296_audit_request_id_index_notx.sql'`)
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	}
	var currentAuditIndex uint32
	require.NoError(t, db.QueryRowContext(ctx, `SELECT 'idx_audit_logs_request_id_created_at'::regclass::oid`).Scan(&currentAuditIndex))
	require.Equal(t, auditIndex, currentAuditIndex)
}
