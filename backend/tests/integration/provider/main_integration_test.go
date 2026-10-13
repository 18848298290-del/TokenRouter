//go:build integration

package provider_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// Integration-tagged provider tests share one migrated PostgreSQL container
// within the test process. The runner replaces the ordinary TestMain callback,
// opens the Ent client, applies embedded migrations, and closes the database
// client before terminating the container after test execution.

// integrationDB and integrationEntClient share the process's migrated PostgreSQL
// container. runPostgresTests initializes both before tests and closes the Ent
// client before terminating the container after the runner completes.
var (
	integrationDB        *sql.DB       // shared SQL connection pool
	integrationEntClient *dbent.Client // shared migrated Ent client
)

// init replaces the provider test runner with PostgreSQL integration setup.
func init() { runProviderTests = runPostgresTests }

// runPostgresTests starts one PostgreSQL container, applies embedded
// migrations, and returns the test runner's exit code. The trimmed environment
// image overrides the fixture default; container setup and migrations receive
// a three-minute context, while tests supply their own contexts.
//
// Setup failures print diagnostics and return one. Deferred cleanup closes Ent
// before terminating the container with a separate thirty-second context,
// printing cleanup errors while retaining the runner's selected exit code.
func runPostgresTests(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	image := strings.TrimSpace(os.Getenv("TOKENROUTER_TEST_POSTGRES_IMAGE"))
	if image == "" {
		image = "postgres:18.1-alpine3.23"
	}
	container, err := tcpostgres.Run(ctx, image, tcpostgres.WithDatabase("provider_contracts"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Register container teardown before Ent cleanup so deferred calls close the
	// application connection first and then remove the process's PostgreSQL fixture.
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := container.Terminate(cleanup); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	integrationDB, err = sql.Open("postgres", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	integrationEntClient = dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, integrationDB)))
	defer func() {
		if err := integrationEntClient.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()
	// Apply the embedded migrations before m.Run so repository assertions use
	// the same schema expected by their Ent client and raw SQL statements.
	if err := postgresinfra.ApplyMigrations(ctx, integrationDB, migrations.FS); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}
