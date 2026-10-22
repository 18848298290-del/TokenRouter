//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// TestCustomVideoResolutionMigration upgrades populated heap and partitioned usage
// storage. Existing charges remain intact, fifty-character labels and long durations
// round-trip, and repeated migration execution records a single applied identity.
func TestCustomVideoResolutionMigration(t *testing.T) {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("video"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
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
	content, err := migrations.FS.ReadFile("299_custom_video_resolution.sql")
	require.NoError(t, err)
	for _, partitioned := range []bool{false, true} {
		t.Run(fmt.Sprintf("partitioned_%t", partitioned), func(t *testing.T) {
			schema := fmt.Sprintf("video_%t", partitioned)
			dsn, err := pg.ConnectionString(ctx, "sslmode=disable", "search_path="+schema)
			require.NoError(t, err)
			db, err := sql.Open("postgres", dsn)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			_, err = db.ExecContext(ctx, "CREATE SCHEMA "+schema)
			require.NoError(t, err)
			statement := "CREATE TABLE usage_logs (id bigint, video_resolution varchar(10), video_duration_seconds integer, actual_cost numeric(20,8))"
			if partitioned {
				statement += " PARTITION BY RANGE (id)"
			}
			_, err = db.ExecContext(ctx, statement)
			require.NoError(t, err)
			if partitioned {
				_, err = db.ExecContext(ctx, "CREATE TABLE usage_logs_current PARTITION OF usage_logs FOR VALUES FROM (0) TO (100)")
				require.NoError(t, err)
			}
			_, err = db.ExecContext(ctx, "INSERT INTO usage_logs VALUES (1,'720p',15,0.12345678)")
			require.NoError(t, err)
			fs := fstest.MapFS{"299_custom_video_resolution.sql": &fstest.MapFile{Data: content}}
			require.NoError(t, postgres.ApplyMigrations(ctx, db, fs))
			require.NoError(t, postgres.ApplyMigrations(ctx, db, fs))
			label := strings.Repeat("界", 50)
			_, err = db.ExecContext(ctx, "INSERT INTO usage_logs VALUES (2,$1,999,1.2)", label)
			require.NoError(t, err)
			var saved string
			var seconds, applied int
			require.NoError(t, db.QueryRowContext(ctx, "SELECT video_resolution,video_duration_seconds FROM usage_logs WHERE id=2").Scan(&saved, &seconds))
			require.Equal(t, label, saved)
			require.Equal(t, 999, seconds)
			require.NoError(t, db.QueryRowContext(ctx, "SELECT actual_cost::text FROM usage_logs WHERE id=1").Scan(&saved))
			require.Equal(t, "0.12345678", saved)
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&applied))
			require.Equal(t, 1, applied)
			_, err = db.ExecContext(ctx, "INSERT INTO usage_logs VALUES (3,$1,60,0)", label+"x")
			require.Error(t, err)
		})
	}
}
