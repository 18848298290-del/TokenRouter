//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"io/fs"
	"testing"
	"testing/fstest"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	infra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestPlatformIndependentGroupsMigration checks historical snapshots, replay, and rollback in PostgreSQL.
func TestPlatformIndependentGroupsMigration(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("groups_migration"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Terminate(ctx)) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	// Build the pre-upgrade schema from historical migrations, including its original columns and constraints.
	before := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Name() >= "277_" {
			continue
		}
		data, readErr := migrations.FS.ReadFile(entry.Name())
		require.NoError(t, readErr)
		before[entry.Name()] = &fstest.MapFile{Data: data}
	}
	require.NoError(t, infra.ApplyMigrations(ctx, db, before))
	_, err = db.ExecContext(ctx, `
        INSERT INTO users (id, email, password_hash) VALUES (7001, 'snapshot@example.test', 'hash');
        INSERT INTO groups (id, name, platform, is_default, routing_policy, allowed_protocols, protocol_fallbacks)
        VALUES (7001, 'mixed migration', 'anthropic', true,
            '{"model_mapping":{"anthropic":{"public":"claude-sonnet"},"openai":{"draft":"gpt-5"}},"allowed_models":{"anthropic":["claude-sonnet"],"openai":["gpt-5"]}}',
            '["anthropic_messages","openai_responses"]', '{"openai_responses":"anthropic_messages"}');
        INSERT INTO accounts (id, name, platform, type, credentials, extra)
        VALUES (7001, 'migrated account', 'openai', 'apikey', '{}', '{"openai_passthrough":true,"mixed_scheduling":true}'),
            (7002, 'empty range', 'openai', 'apikey', '{}', '{}'),
            (7003, 'restricted passthrough', 'openai', 'apikey', '{"model_whitelist":["gpt-5"]}', '{"openai_passthrough":true}');
        INSERT INTO api_keys (id, user_id, key, name, group_id, fallback_to_default_group_when_unavailable)
        VALUES (7001, 7001, 'migration-key', 'bound', 7001, true), (7002, 7001, 'unbound-key', 'unbound', NULL, false);
        INSERT INTO account_groups (account_id, group_id) VALUES (7001, 7001);
        INSERT INTO usage_logs (user_id, api_key_id, account_id, request_id, model, group_id, actual_cost)
        VALUES (7001, 7001, 7001, 'legacy-platform', 'public', 7001, 4.25);
        INSERT INTO ops_error_logs (request_id, account_id, group_id, platform, error_phase, error_type, severity, status_code, error_message)
        VALUES ('empty-platform', 7001, 7001, NULL, 'upstream', 'upstream_error', 'error', 502, 'failure'),
            ('explicit-platform', 7001, 7001, 'gemini', 'upstream', 'upstream_error', 'error', 502, 'failure');
        INSERT INTO creative_runs (run_id, user_id, group_id, api_key_id, account_id, model, operation, prompt_hash, request_fingerprint)
        VALUES ('legacy-creative', 7001, 7001, 7001, 7001, 'gpt-image-2', 'generate', 'hash', 'fingerprint');
        INSERT INTO user_platform_quotas (user_id, platform, daily_limit_usd) VALUES (7001, 'openai', 10);
        INSERT INTO settings (key, value) VALUES ('default_platform_quotas','{"openai":{"daily_limit_usd":10}}'), ('allow_ungrouped_key_scheduling','true');
    `)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("277_platform_independent_groups.sql")
	require.NoError(t, err)

	// A failure after migration SQL must roll back schema, backfills, and archives so the upgrade can be
	// retried.
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration)+"\nSELECT 1/0;")
	require.Error(t, err)
	require.NoError(t, tx.Rollback())
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='groups' AND column_name='platform')`).Scan(&exists))
	require.True(t, exists)
	var archive sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass('platform_independent_group_archive')::text`).Scan(&archive))
	require.False(t, archive.Valid)

	through280 := fstest.MapFS{}
	for _, entry := range entries {
		if entry.Name() >= "281_" {
			continue
		}
		data, readErr := migrations.FS.ReadFile(entry.Name())
		require.NoError(t, readErr)
		through280[entry.Name()] = &fstest.MapFile{Data: data}
	}
	require.NoError(t, infra.ApplyMigrations(ctx, db, through280))
	var platform, policy, fallbacks, allowed, whitelist string
	var cost float64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT platform, actual_cost FROM usage_logs WHERE request_id='legacy-platform'`).Scan(&platform, &cost))
	require.Equal(t, "anthropic", platform)
	require.Equal(t, 4.25, cost)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT routing_policy::text, protocol_fallbacks::text, allowed_protocols::text FROM groups WHERE id=7001`).Scan(&policy, &fallbacks, &allowed))
	require.JSONEq(t, `{"model_mapping":{"public":"claude-sonnet"},"allowed_models":["claude-sonnet"]}`, policy)
	require.Contains(t, fallbacks, `"openai_responses": ["anthropic_messages"]`)
	require.Contains(t, fallbacks, `"anthropic_messages": []`)
	require.JSONEq(t, `["anthropic_messages","openai_responses"]`, allowed)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT credentials->'model_whitelist' FROM accounts WHERE id=7001`).Scan(&whitelist))
	require.JSONEq(t, `["*"]`, whitelist)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT credentials ? 'model_whitelist' FROM accounts WHERE id=7002`).Scan(&exists))
	require.False(t, exists)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT credentials->'model_whitelist' FROM accounts WHERE id=7003`).Scan(&whitelist))
	require.JSONEq(t, `["gpt-5"]`, whitelist)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT group_id IS NULL FROM api_keys WHERE id=7002`).Scan(&exists))
	require.True(t, exists)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT fallback_when_group_unavailable FROM api_keys WHERE id=7001`).Scan(&exists))
	require.True(t, exists)
	var archived int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM removed_platform_quota_archive`).Scan(&archived))
	require.Equal(t, 2, archived)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM platform_independent_setting_archive WHERE key='allow_ungrouped_key_scheduling'`).Scan(&archived))
	require.Equal(t, 1, archived)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM settings WHERE key='allow_ungrouped_key_scheduling'`).Scan(&archived))
	require.Zero(t, archived)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT platform FROM ops_error_logs WHERE request_id='empty-platform'`).Scan(&platform))
	require.Equal(t, "anthropic", platform)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT platform FROM ops_error_logs WHERE request_id='explicit-platform'`).Scan(&platform))
	require.Equal(t, "gemini", platform)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT provider FROM creative_runs WHERE run_id='legacy-creative'`).Scan(&platform))
	require.Equal(t, "openai", platform)

	_, err = db.ExecContext(ctx, `UPDATE accounts SET platform='gemini' WHERE id=7001;
        UPDATE accounts SET extra='{"openai_passthrough":true}', credentials='{"model_whitelist":[]}' WHERE id=7002`)
	require.NoError(t, err)
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, db.QueryRowContext(ctx, `SELECT platform FROM usage_logs WHERE request_id='legacy-platform'`).Scan(&platform))
	require.Equal(t, "anthropic", platform)
	// An empty post-upgrade whitelist selects the default catalogue; replay must not widen it using the legacy
	// passthrough flag.
	require.NoError(t, db.QueryRowContext(ctx, `SELECT credentials->'model_whitelist' FROM accounts WHERE id=7002`).Scan(&whitelist))
	require.JSONEq(t, `[]`, whitelist)
}
