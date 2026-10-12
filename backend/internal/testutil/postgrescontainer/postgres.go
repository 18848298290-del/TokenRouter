//go:build integration

package postgrescontainer

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// PostgreSQL storage fixtures apply the application's embedded migrations
// before domain records are read or written. New starts a dedicated container,
// obtains a UTC connection, and registers cleanup so the SQL pool closes before
// container termination at test completion.

// New returns a pool for a dedicated migrated PostgreSQL fixture or fails the
// calling test. Container startup, connection-string discovery, and migrations
// share a two-minute context; TOKENROUTER_TEST_POSTGRES_IMAGE can replace the
// default image. Cleanup closes the pool before container termination.
func New(t *testing.T) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	image := strings.TrimSpace(os.Getenv("TOKENROUTER_TEST_POSTGRES_IMAGE"))
	if image == "" {
		image = "postgres:18.1-alpine3.23"
	}
	container, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("storage_contracts"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	// Register container termination immediately after acquisition. Later pool and
	// application cleanups run first because testing executes cleanup callbacks in reverse order.
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, postgresinfra.ApplyMigrations(ctx, db, migrations.FS))
	return db
}
