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

	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestPlatformIndependentPricingMigration converts historical cross-platform price
// fixtures in PostgreSQL. It checks independent highest input/output prices,
// NULL versus zero handling, per-rule isolation, interval and time-rule retention,
// original archives, and edited-price replay. Incompatible fixtures must report
// a migration conflict. Savepoint rollback must restore the captured price and
// interval rows, retain the platform column, and remove the archive table.
func TestPlatformIndependentPricingMigration(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("pricing_migration"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
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
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	// Testing cleanup runs in reverse registration order. Close the SQL pool
	// before terminating the PostgreSQL container that hosts these transactional scenarios.
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	// Stop before migration 276 so each scenario inserts platform-bearing prices
	// into the historical schema that the conversion is expected to transform.
	before := fstest.MapFS{}
	files, err := fs.ReadDir(migrations.FS, ".")
	require.NoError(t, err)
	for _, file := range files {
		if file.Name() >= "276_" {
			continue
		}
		data, err := migrations.FS.ReadFile(file.Name())
		require.NoError(t, err)
		before[file.Name()] = &fstest.MapFile{Data: data}
	}
	require.NoError(t, postgres.ApplyMigrations(ctx, db, before))
	migration, err := migrations.FS.ReadFile("276_platform_independent_pricing.sql")
	require.NoError(t, err)
	t.Run("highest_prices_and_scoped_costs", func(t *testing.T) {
		// Roll back merged prices, archives, and schema changes with this fixture.
		// The next scenario then starts from the same historical platform-bearing tables.
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { require.NoError(t, tx.Rollback()) }()
		_, err = tx.ExecContext(ctx, `
INSERT INTO pricing_configs(id,name) VALUES (76001,'merge pricing');
INSERT INTO pricing_config_model_pricing(pricing_config_id,platform,models,input_price,output_price,cache_read_price) VALUES
 (76001,'anthropic','[" claude-test ","unique-test"]',3,15,0),
 (76001,'openai','["CLAUDE-TEST"]',5,10,0);
INSERT INTO pricing_config_account_stats_pricing_rules(id,pricing_config_id,name,account_ids) VALUES
 (76001,76001,'first',ARRAY[1]::BIGINT[]),(76002,76001,'second',ARRAY[2]::BIGINT[]);
INSERT INTO pricing_config_account_stats_model_pricing(rule_id,platform,models,input_price) VALUES
 (76001,'anthropic','["model"]',1),(76001,'openai','["model"]',2),(76002,'openai','["model"]',7);
INSERT INTO groups(name,model_pricing) VALUES ('group price','[{"platform":"anthropic","models":["model"],"input_price":3,"output_price":15},{"platform":"openai","models":["model"],"input_price":5,"output_price":10}]');
`)
		require.NoError(t, err)
		var originalConfig string
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT jsonb_agg(to_jsonb(p) ORDER BY id)::TEXT FROM pricing_config_model_pricing p WHERE pricing_config_id=76001`).Scan(&originalConfig))
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
		var input, output, cache float64
		var cacheWrite sql.NullFloat64
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT input_price,output_price,cache_read_price,cache_write_price FROM pricing_config_model_pricing WHERE pricing_config_id=76001 AND models='["claude-test"]'`).Scan(&input, &output, &cache, &cacheWrite))
		require.Equal(t, 5.0, input)
		require.Equal(t, 15.0, output)
		require.Zero(t, cache)
		require.False(t, cacheWrite.Valid)
		var priceCount int
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM pricing_config_model_pricing WHERE pricing_config_id=76001`).Scan(&priceCount))
		require.Equal(t, 2, priceCount)
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT input_price,output_price,cache_read_price,cache_write_price FROM pricing_config_model_pricing WHERE pricing_config_id=76001 AND models='["unique-test"]'`).Scan(&input, &output, &cache, &cacheWrite))
		require.Equal(t, 3.0, input)
		require.Equal(t, 15.0, output)
		require.Zero(t, cache)
		require.False(t, cacheWrite.Valid)
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT input_price FROM pricing_config_account_stats_model_pricing WHERE rule_id=76001`).Scan(&input))
		require.Equal(t, 2.0, input)
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT input_price FROM pricing_config_account_stats_model_pricing WHERE rule_id=76002`).Scan(&input))
		require.Equal(t, 7.0, input)
		var groupPrice string
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT model_pricing::TEXT FROM groups WHERE name='group price'`).Scan(&groupPrice))
		require.JSONEq(t, `[{"models":["model"],"billing_mode":"token","input_price":5,"output_price":15}]`, groupPrice)
		var archived int
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM platform_independent_pricing_archive WHERE scope='config' AND scope_id=76001`).Scan(&archived))
		require.Equal(t, 1, archived)
		var archivedConfig string
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT jsonb_agg(entry - 'intervals' ORDER BY ordinal)::TEXT FROM platform_independent_pricing_archive CROSS JOIN LATERAL jsonb_array_elements(entries) WITH ORDINALITY AS item(entry, ordinal) WHERE scope='config' AND scope_id=76001`).Scan(&archivedConfig))
		require.JSONEq(t, originalConfig, archivedConfig)
		// Edit a migrated price before replay so the second execution must retain
		// its new amount and row identity, while the archive still describes
		// the pre-conversion pricing entries.
		var priceID int64
		require.NoError(t, tx.QueryRowContext(ctx, `UPDATE pricing_config_model_pricing SET input_price=9 WHERE pricing_config_id=76001 AND models='["claude-test"]' RETURNING id`).Scan(&priceID))
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
		var replayID int64
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT id,input_price FROM pricing_config_model_pricing WHERE pricing_config_id=76001 AND models='["claude-test"]'`).Scan(&replayID, &input))
		require.Equal(t, priceID, replayID)
		require.Equal(t, 9.0, input)
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT jsonb_agg(entry - 'intervals' ORDER BY ordinal)::TEXT FROM platform_independent_pricing_archive CROSS JOIN LATERAL jsonb_array_elements(entries) WITH ORDINALITY AS item(entry, ordinal) WHERE scope='config' AND scope_id=76001`).Scan(&archivedConfig))
		require.JSONEq(t, originalConfig, archivedConfig)
	})
	t.Run("interval_prices_and_matching_time_rules", func(t *testing.T) {
		// Keep interval conversion inside this subtest's transaction. Rollback
		// removes its transformed tables and fixtures so later conflict cases
		// can still create prices with historical platform columns.
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { require.NoError(t, tx.Rollback()) }()
		_, err = tx.ExecContext(ctx, `
INSERT INTO pricing_configs(id,name) VALUES (79001,'interval pricing');
INSERT INTO pricing_config_model_pricing(id,pricing_config_id,platform,models,time_pricing) VALUES
 (79001,79001,'anthropic','["model"]','{"timezone":"UTC","periods":[{"start_time":"09:00","end_time":"17:00","multiplier":1.5}]}'),
 (79002,79001,'openai','["model"]','{"timezone":"UTC","periods":[{"start_time":"09:00","end_time":"17:00","multiplier":1.5}]}');
INSERT INTO pricing_config_pricing_intervals(pricing_id,min_tokens,max_tokens,tier_label,input_price,output_price,cache_read_price,sort_order) VALUES
 (79001,0,100,'base',1,9,0,0),(79002,0,100,'base',2,4,0,0),
 (79001,100,NULL,'long',7,3,0,1),(79002,100,NULL,'long',5,8,0,1);
`)
		require.NoError(t, err)
		var original string
		snapshotSQL := `SELECT jsonb_agg(to_jsonb(p) || jsonb_build_object('intervals', (SELECT jsonb_agg(to_jsonb(i) ORDER BY sort_order,id) FROM pricing_config_pricing_intervals i WHERE i.pricing_id=p.id)) ORDER BY p.id)::TEXT FROM pricing_config_model_pricing p WHERE pricing_config_id=79001`
		require.NoError(t, tx.QueryRowContext(ctx, snapshotSQL).Scan(&original))
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
		var intervals, timeRules, archived string
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT jsonb_agg(jsonb_build_object('min_tokens',i.min_tokens,'max_tokens',i.max_tokens,'tier_label',i.tier_label,'input_price',i.input_price,'output_price',i.output_price,'cache_read_price',i.cache_read_price,'cache_write_price',i.cache_write_price) ORDER BY i.sort_order)::TEXT FROM pricing_config_pricing_intervals i JOIN pricing_config_model_pricing p ON p.id=i.pricing_id WHERE p.pricing_config_id=79001`).Scan(&intervals))
		require.JSONEq(t, `[
 {"min_tokens":0,"max_tokens":100,"tier_label":"base","input_price":2,"output_price":9,"cache_read_price":0,"cache_write_price":null},
 {"min_tokens":100,"max_tokens":null,"tier_label":"long","input_price":7,"output_price":8,"cache_read_price":0,"cache_write_price":null}
]`, intervals)
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT time_pricing::TEXT FROM pricing_config_model_pricing WHERE pricing_config_id=79001`).Scan(&timeRules))
		require.JSONEq(t, `{"timezone":"UTC","periods":[{"start_time":"09:00","end_time":"17:00","multiplier":1.5}]}`, timeRules)
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT entries::TEXT FROM platform_independent_pricing_archive WHERE scope='config' AND scope_id=79001`).Scan(&archived))
		require.JSONEq(t, original, archived)
	})
	// name identifies the subtest's incompatibility, while patch changes its
	// seeded price pair into that conflicting state. Every case compares the
	// patched fixture before conversion with the state restored by savepoint rollback.
	for _, tc := range []struct {
		name, patch string /* name labels the scenario; patch supplies its conflicting fixture SQL. */
	}{
		{"inheritance", `UPDATE pricing_config_model_pricing SET input_price=NULL WHERE platform='openai'`},
		{"billing_mode", `UPDATE pricing_config_model_pricing SET billing_mode='image',per_request_price=2 WHERE platform='openai'`},
		{"multiplier", `UPDATE pricing_config_model_pricing SET price_multiplier=2 WHERE platform='openai'`},
		{"fast_multiplier", `UPDATE pricing_config_model_pricing SET fast_multiplier=1.5 WHERE platform='openai'`},
		{"interval_shape", `INSERT INTO pricing_config_pricing_intervals(pricing_id,min_tokens,input_price) SELECT id,100,2 FROM pricing_config_model_pricing WHERE platform='openai'`},
		{"interval_inheritance", `INSERT INTO pricing_config_pricing_intervals(id,pricing_id,min_tokens,input_price) SELECT id,id,0,CASE WHEN platform='openai' THEN NULL ELSE 1 END FROM pricing_config_model_pricing WHERE pricing_config_id=77001`},
		{"time_rules", `UPDATE pricing_config_model_pricing SET time_pricing='{"timezone":"UTC","periods":[]}' WHERE platform='openai'`},
		{"overlapping_patterns", `UPDATE pricing_config_model_pricing SET models='["model*"]' WHERE platform='openai'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Roll back the case's patched fixtures when the subtest ends. This
			// keeps the repeated identifiers and historical schema available for
			// the next independently tested incompatibility.
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { require.NoError(t, tx.Rollback()) }()
			_, err = tx.ExecContext(ctx, `INSERT INTO pricing_configs(id,name) VALUES (77001,'conflict'); INSERT INTO pricing_config_model_pricing(id,pricing_config_id,platform,models,input_price) VALUES (77001,77001,'anthropic','["model"]',1),(77002,77001,'openai','["model"]',2);`+tc.patch)
			require.NoError(t, err)
			var original, after string
			snapshotSQL := `SELECT jsonb_agg(to_jsonb(p) || jsonb_build_object('intervals', (SELECT jsonb_agg(to_jsonb(i) ORDER BY sort_order,id) FROM pricing_config_pricing_intervals i WHERE i.pricing_id=p.id)) ORDER BY p.id)::TEXT FROM pricing_config_model_pricing p WHERE pricing_config_id=77001`
			require.NoError(t, tx.QueryRowContext(ctx, snapshotSQL).Scan(&original))
			// Save the patched input before conversion so a migration error can
			// roll back its schema changes while retaining this fixture for comparison.
			_, err = tx.ExecContext(ctx, `SAVEPOINT before_migration`)
			require.NoError(t, err)
			_, err = tx.ExecContext(ctx, string(migration))
			require.ErrorContains(t, err, "PRICE_MIGRATION_CONFLICT")
			if tc.name != "overlapping_patterns" {
				require.ErrorContains(t, err, "77001")
				require.ErrorContains(t, err, "77002")
			}
			// PostgreSQL leaves the transaction aborted after the expected error.
			// Restore the savepoint before reading the snapshot and schema assertions
			// that must still describe the patched pre-conversion fixture.
			_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT before_migration`)
			require.NoError(t, err)
			require.NoError(t, tx.QueryRowContext(ctx, snapshotSQL).Scan(&after))
			require.JSONEq(t, original, after)
			var exists bool
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='pricing_config_model_pricing' AND column_name='platform')`).Scan(&exists))
			require.True(t, exists)
			var archive sql.NullString
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT to_regclass('platform_independent_pricing_archive')::TEXT`).Scan(&archive))
			require.False(t, archive.Valid)
		})
	}
}
