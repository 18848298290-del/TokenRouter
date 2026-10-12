//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestAntigravityRetirementMigration checks retirement, replay, and retained records while an exclusive
// usage lock detects unexpected access.
func TestAntigravityRetirementMigration(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase("retirement"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Terminate(ctx)) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	_, err = db.ExecContext(ctx, `INSERT INTO providers (id,name,platform,type,credentials,status,schedulable,deleted_at) VALUES
 (901,'legacy-key','antigravity','apikey','{"api_key":"fixture","upstream_protocols":[]}', 'active',true,NULL),
 (902,'legacy-static','antigravity','upstream','{"api_key":"fixture","upstream_protocols":["anthropic_messages"]}', 'error',true,NULL),
 (903,'oauth','antigravity','oauth','{}','active',true,NULL),
 (904,'gemini','gemini','apikey','{}','active',true,NULL),
 (905,'deleted','antigravity','apikey','{}','active',true,NOW())`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO groups(id,name) VALUES(901,'retirement');
 INSERT INTO provider_groups(provider_id,group_id) VALUES(901,901)`)
	require.NoError(t, err)
	// The migration must complete while another connection holds the usage table lock.
	locked, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = locked.Rollback() }()
	_, err = locked.ExecContext(ctx, `LOCK TABLE usage_logs IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	_, err = conn.ExecContext(ctx, `SET lock_timeout = '1s'`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("286_disable_antigravity_static_providers.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = conn.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	for _, id := range []int{901, 902, 903, 904, 905} {
		var status, kind, credentials string
		var schedulable bool
		require.NoError(t, conn.QueryRowContext(ctx, `SELECT status,type,credentials::text,schedulable FROM providers WHERE id=$1`, id).Scan(&status, &kind, &credentials, &schedulable))
		if id == 901 || id == 902 {
			require.Equal(t, "inactive", status)
			require.False(t, schedulable)
			require.Contains(t, credentials, "fixture")
		} else {
			require.Equal(t, "active", status)
			require.True(t, schedulable)
		}
		if id == 902 {
			require.Equal(t, "upstream", kind)
			require.Contains(t, credentials, "anthropic_messages")
		}
	}
	var count int
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE provider_id IN (901,902) AND event_type='provider_changed'`).Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_groups WHERE provider_id=901 AND group_id=901`).Scan(&count))
	require.Equal(t, 1, count)
}
