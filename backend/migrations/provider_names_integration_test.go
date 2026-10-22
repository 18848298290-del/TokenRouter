//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	infra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestProviderNamesMigration upgrades a historical account schema in PostgreSQL.
// It checks rollback after blocked renaming and conflicting credentials, retained
// usage totals, relfilenode comparisons, targeted configuration/template updates,
// unrelated names, and replay. The test then applies current migrations and
// rebuilds an empty schema to check fresh provider installation.
func TestProviderNamesMigration(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("provider_names"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
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
	// Register the SQL pool after container cleanup so reverse cleanup order
	// closes its connections before terminating the PostgreSQL instance hosting this fixture.
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	// Stop before migration 282 so the fixture can seed account-named tables
	// and settings before exercising the provider rename and its compatibility rules.
	historical := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Name() >= "282_" {
			continue
		}
		content, err := migrations.FS.ReadFile(entry.Name())
		require.NoError(t, err)
		historical[entry.Name()] = &fstest.MapFile{Data: content}
	}
	require.NoError(t, infra.ApplyMigrations(ctx, db, historical))
	_, err = db.ExecContext(ctx, `
 INSERT INTO users(id,email,password_hash) VALUES(71,'provider-migration@example.test','fixture');
 INSERT INTO accounts(id,name,platform,type,credentials) VALUES(71,'fixture','openai','api_key','{"account_mode":"payg","chatgpt_account_id":"external","account_uuid":"external-uuid","chatgpt_account_is_fedramp":true,"service_account_json":{"client_email":"fixture@example.test"},"model_mapping":{"account-report":"gpt-4.1","provider-report":"gpt-4.1-mini"},"header_overrides":{"x-account-id":"external-account"},"vendor_data":{"accountId":"vendor-id"}}');
 INSERT INTO api_keys(id,user_id,key,name) VALUES(71,71,'provider-migration-key','fixture');
 INSERT INTO usage_logs(user_id,billing_user_id,api_key_id,account_id,model,request_id,actual_cost)
 SELECT 71,71,71,71,'fixture','migration-'||n,0.25 FROM generate_series(1,10000) n;
 INSERT INTO settings(key,value) VALUES
 ('ops_email_notification_config','{"report":{"account_health_enabled":true,"account_health_schedule":"daily"}}'),
 ('openai_oauth_import_defaults','{"account":{"priority":2},"credentials":{"account_scheduling_threshold":80,"model_mapping":{"account-report":"gpt-4.1"}},"extra":{"account_custom":"preserve"}}'),
 ('ops_advanced_settings','{"openai_account_quota_auto_pause":{"default_threshold_5h":0.8},"ignore_no_available_accounts":true}'),
 ('custom_account_setting','opaque'),
 ('notification_email_template:account.quota_alert:en','{"subject":"{{ account_name }}","html":"{{\naccount_id\t}}"}'),
 ('notification_email_template:content_moderation.account_disabled:en','{"subject":"Login disabled"}');
 CREATE TABLE unrelated_accounts(id BIGSERIAL PRIMARY KEY, account_id BIGINT);
 CREATE TABLE migration_physical_probe AS SELECT oid,relfilenode FROM pg_class
 WHERE oid='usage_logs'::regclass OR oid IN(SELECT indexrelid FROM pg_index WHERE indrelid='usage_logs'::regclass);
 `)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("282_rename_accounts_to_providers.sql")
	require.NoError(t, err)
	// Stop the rename assertions at migration 282. Subsequent migrations retire
	// the OAuth import defaults that this historical fixture still needs to inspect.
	historical["282_rename_accounts_to_providers.sql"] = &fstest.MapFile{Data: migration}
	// Direct execution begins a transaction, applies the rename, and commits its
	// successful changes together. Deferred rollback clears a failed attempt;
	// the completed-transaction rollback error after commit is discarded.
	apply := func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err = tx.ExecContext(ctx, string(migration)); err != nil {
			return err
		}
		return tx.Commit()
	}
	// Hold a reader lock on usage_logs so the rename's exclusive lock cannot
	// be granted. The attempted transaction must time out and leave accounts
	// available under its historical name.
	holder, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = holder.ExecContext(ctx, "LOCK TABLE usage_logs IN ACCESS SHARE MODE")
	require.NoError(t, err)
	started := time.Now()
	require.ErrorContains(t, apply(), "lock timeout")
	// Migration 282 sets a ten-second lock timeout; this thirty-second bound
	// checks that the blocked attempt returns before the longer statement budget.
	require.Less(t, time.Since(started), 30*time.Second)
	var oldExists bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass('accounts') IS NOT NULL").Scan(&oldExists))
	require.True(t, oldExists)
	// Release the reader before exercising configuration conflicts and successful
	// upgrade, so subsequent attempts can acquire the table-rename lock normally.
	require.NoError(t, holder.Rollback())
	// Seed different account_mode and provider_mode values to make conversion
	// ambiguous. The failed attempt must retain the historical table and both
	// conflicting credential values for an administrator to resolve.
	_, err = db.ExecContext(ctx, `UPDATE accounts SET credentials = credentials || '{"provider_mode":"coding"}'::jsonb WHERE id=71`)
	require.NoError(t, err)
	require.ErrorContains(t, apply(), "provider configuration key conflict: provider_mode")
	require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass('accounts') IS NOT NULL").Scan(&oldExists))
	require.True(t, oldExists)
	var conflictingCredentials string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT jsonb_build_object('account_mode',credentials->'account_mode','provider_mode',credentials->'provider_mode')::text FROM accounts WHERE id=71").Scan(&conflictingCredentials))
	require.JSONEq(t, `{"account_mode":"payg","provider_mode":"coding"}`, conflictingCredentials)
	_, err = db.ExecContext(ctx, "UPDATE accounts SET credentials = credentials - 'provider_mode' WHERE id=71")
	require.NoError(t, err)
	started = time.Now()
	require.NoError(t, infra.ApplyMigrations(ctx, db, historical))
	t.Logf("provider migration took %s", time.Since(started))
	// Retry through the runner and then execute rename SQL directly. Subsequent
	// data and configuration assertions must observe the same completed conversion
	// after both forms of replay.
	require.NoError(t, infra.ApplyMigrations(ctx, db, historical))
	require.NoError(t, apply())
	var count, changed int
	var cost float64
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*),sum(actual_cost) FROM usage_logs WHERE provider_id=71").Scan(&count, &cost))
	require.Equal(t, 10000, count)
	require.Equal(t, float64(2500), cost)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM migration_physical_probe b JOIN pg_class c ON c.oid=b.oid WHERE c.relfilenode<>b.relfilenode").Scan(&changed))
	require.Zero(t, changed)
	var credentials string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT credentials::text FROM providers WHERE id=71").Scan(&credentials))
	require.JSONEq(t, `{"provider_mode":"payg","chatgpt_account_id":"external","account_uuid":"external-uuid","chatgpt_account_is_fedramp":true,"service_account_json":{"client_email":"fixture@example.test"},"model_mapping":{"account-report":"gpt-4.1","provider-report":"gpt-4.1-mini"},"header_overrides":{"x-account-id":"external-account"},"vendor_data":{"accountId":"vendor-id"}}`, credentials)
	var setting string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='openai_oauth_import_defaults'").Scan(&setting))
	require.JSONEq(t, `{"provider":{"priority":2},"credentials":{"provider_scheduling_threshold":80,"model_mapping":{"account-report":"gpt-4.1"}},"extra":{"account_custom":"preserve"}}`, setting)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='ops_advanced_settings'").Scan(&setting))
	require.JSONEq(t, `{"openai_provider_quota_auto_pause":{"default_threshold_5h":0.8},"ignore_no_available_providers":true}`, setting)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='custom_account_setting'").Scan(&setting))
	require.Equal(t, "opaque", setting)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='ops_email_notification_config'").Scan(&setting))
	require.JSONEq(t, `{"report":{"provider_health_enabled":true,"provider_health_schedule":"daily"}}`, setting)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='notification_email_template:provider.quota_alert:en'").Scan(&setting))
	require.JSONEq(t, `{"subject":"{{provider_name}}","html":"{{provider_id}}"}`, setting)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='notification_email_template:content_moderation.account_disabled:en'").Scan(&setting))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass('unrelated_accounts_pkey') IS NOT NULL AND to_regclass('unrelated_accounts_id_seq') IS NOT NULL").Scan(&oldExists))
	require.True(t, oldExists)
	// Advance beyond the historical rename assertions to current migrations.
	// The later retirement script must remove the OAuth import template that
	// the migration-282 fixture retained during rename and replay checks.
	require.NoError(t, infra.ApplyMigrations(ctx, db, migrations.FS))
	require.ErrorIs(t, db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='openai_oauth_import_defaults'").Scan(&setting), sql.ErrNoRows)
	// Rebuild an empty schema in the same isolated instance to check a fresh
	// installation of all migrations.
	_, err = db.ExecContext(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public")
	require.NoError(t, err)
	require.NoError(t, infra.ApplyMigrations(ctx, db, migrations.FS))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass('providers') IS NOT NULL").Scan(&oldExists))
	require.True(t, oldExists)
}
