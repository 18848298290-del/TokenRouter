//go:build integration

package app

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/app/bootstrap"
)

// PostgreSQL integration fixtures share one pool between direct SQL and Ent
// access. A container-backed fixture applies the embedded migrations before
// exposing that pool, while constructor aliases let external integration
// packages use the application bindings under the integration build tag.

var (
	// NewProviderTestsForTest exposes provider probe construction with application
	// dependencies for integration packages that exercise the assembled test service.
	NewProviderTestsForTest = provideProviderTests

	// These health aliases expose the application's runtime and upstream-health
	// constructors so integration consumers resolve the production observation bindings.
	NewProviderHealthRuntimeForTest = provideProviderHealthRuntime
	NewUpstreamHealthForTest        = provideUpstreamHealth

	// NewProviderStoreForTest exposes application store configuration and event-write
	// construction to integration packages that need the assembled provider persistence.
	NewProviderStoreForTest = provideProviderStore
)

// databaseFixture exposes one migrated PostgreSQL container through SQL and Ent.
type databaseFixture struct {
	// db and client share the same pool through Ent's SQL driver. Test cleanup
	// closes the client before terminating the container; callers keep the fixture
	// available until their database operations finish.
	db     *sql.DB       // direct fixture queries
	client *dbent.Client // fixture entity operations
	dsn    string        // fixture connection string
	host   string        // mapped container hostname
	port   int           // mapped PostgreSQL port
}

// newDatabaseFixture starts a PostgreSQL container, opens its shared SQL/Ent
// pool, and applies migrations within a two-minute setup context. Failures
// stop the test. Test cleanup closes Ent before terminating any created
// container with a fresh thirty-second context, including after startup errors.
// Callers need no separate pool owner.
func newDatabaseFixture(t *testing.T) *databaseFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("test_contracts"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	// Register any created container before startup errors can stop this test.
	// Later client cleanups run first because testing reverses registration order.
	if pg != nil {
		t.Cleanup(func() {
			// Setup or test cancellation must not cancel Docker removal. Start the
			// bounded cleanup context when this callback runs.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := pg.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate PostgreSQL container: %v", err)
			}
		})
	}
	require.NoError(t, err)
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	// Bound fixture connections while Ent and SQL consumers share this pool.
	db.SetMaxOpenConns(16)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	// Apply the production schema before returning fixture access so downstream
	// constructors and direct SQL assertions operate on the migrated database.
	require.NoError(t, bootstrap.ApplyMigrations(ctx, db))
	host, err := pg.Host(ctx)
	require.NoError(t, err)
	port, err := pg.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	return &databaseFixture{db: db, client: client, dsn: dsn, host: host, port: port.Int()}
}
