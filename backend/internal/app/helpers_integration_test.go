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

var (
	// NewProviderTestsForTest exposes the application's provider test constructor to integration tests.
	NewProviderTestsForTest = provideProviderTests

	// Health integration tests use the application constructor.
	NewProviderHealthRuntimeForTest = provideProviderHealthRuntime
	NewUpstreamHealthForTest        = provideUpstreamHealth

	// NewProviderStoreForTest binds provider store configuration and event writes for integration tests.
	NewProviderStoreForTest = provideProviderStore
)

type databaseFixture struct {
	db     *sql.DB
	client *dbent.Client
	dsn    string
	host   string
	port   int
}

// newDatabaseFixture constructs the test database.
func newDatabaseFixture(t *testing.T) *databaseFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("test_contracts"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Terminate(context.Background())) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(16)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	require.NoError(t, bootstrap.ApplyMigrations(ctx, db))
	host, err := pg.Host(ctx)
	require.NoError(t, err)
	port, err := pg.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	return &databaseFixture{db: db, client: client, dsn: dsn, host: host, port: port.Int()}
}
