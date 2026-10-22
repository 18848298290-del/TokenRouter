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

// TestProductBrandMigration applies the full migration chain and replays brand
// cleanup in PostgreSQL. It requires selected exact old-brand values, including
// case and whitespace variants, to become TokenRouter while custom and unrelated
// setting values remain unchanged.
func TestProductBrandMigration(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("brand"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
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
	// Cleanup runs in reverse registration order, so close the SQL pool before
	// terminating the PostgreSQL container that supplies this fixture's connections.
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	// Applying the complete chain again exercises the recorded-migration path
	// before the selected script is replayed directly against the brand fixtures.
	require.NoError(t, postgres.ApplyMigrations(ctx, db, migrations.FS))
	migration, err := migrations.FS.ReadFile("283_rename_product_defaults.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	initial := map[string]string{
		"site_name": "Sub2API", "site_name_zh": " sub2api ", "site_name_en": "SUB2API",
		"site_title_zh": "自定义站点", "site_title_en": "My Sub2API", "smtp_from_name": "\tSub2Api\n",
		"payment_product_name_prefix": "TokenRouter", "unrelated_setting": "Sub2API",
	}
	for key, value := range initial {
		_, err = db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,$2)`, key, value)
		require.NoError(t, err)
	}
	for range 2 {
		_, err = db.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	for key, value := range initial {
		want := value
		switch key {
		case "site_name", "site_name_zh", "site_name_en", "smtp_from_name":
			want = "TokenRouter"
		}
		var got string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&got))
		require.Equal(t, want, got, key)
	}
}
