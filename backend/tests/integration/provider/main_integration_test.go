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

// main_integration_test.go connects provider integration cases to one migrated
// PostgreSQL database for the test process. The integration initializer replaces
// the ordinary runner callback, then the runner applies embedded migrations
// before exposing SQL and Ent clients to package tests. Startup work uses a
// bounded context, while each test supplies its own query contexts.
//
// Containers returned with startup errors register teardown before that error
// is handled. The runner closes the SQL pool through Ent after wrapping and
// removes the container using an independent cleanup deadline. Errors during
// cleanup are reported and make a successful test run fail.

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
// including containers returned with startup errors. Cleanup failures are
// printed and change a successful runner result to one.
func runPostgresTests(m *testing.M) (exitCode int) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	image := strings.TrimSpace(os.Getenv("TOKENROUTER_TEST_POSTGRES_IMAGE"))
	if image == "" {
		image = "postgres:18.1-alpine3.23"
	}
	container, err := tcpostgres.Run(
		ctx,
		image,
		tcpostgres.WithDatabase("provider_contracts"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	// A readiness failure can accompany an allocated container. Register its
	// teardown before handling the error so partial startup is also released
	// under a deadline independent of the setup context.
	if container != nil {
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			if err := container.Terminate(cleanup); err != nil {
				fmt.Fprintf(os.Stderr, "failed to terminate postgres container: %v\n", err)
				if exitCode == 0 {
					exitCode = 1
				}
			}
		}()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
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
	// Register SQL cleanup as soon as the pool exists. Ent owns that same pool
	// after construction, so this defer selects one closer before container
	// removal, including failures during migration setup.
	defer func() {
		var closeErr error
		if integrationEntClient != nil {
			closeErr = integrationEntClient.Close()
		} else {
			closeErr = integrationDB.Close()
		}
		if closeErr != nil {
			fmt.Fprintf(os.Stderr, "failed to close postgres client: %v\n", closeErr)
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}()
	integrationEntClient = dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, integrationDB)))
	// Apply the embedded migrations before m.Run so repository assertions use
	// the same schema expected by their Ent client and raw SQL statements.
	if err := postgresinfra.ApplyMigrations(ctx, integrationDB, migrations.FS); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}
