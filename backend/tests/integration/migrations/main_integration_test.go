//go:build integration

package migrations_test

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

// main_integration_test.go prepares the PostgreSQL database used to inspect the
// embedded migration history. Package tests share one container and SQL pool,
// with UTC selected for Go dates and database sessions. The runner applies all
// embedded migrations before cases compare the resulting schema or construct
// historical fixtures through the shared database connection.
//
// Resource teardown is registered during acquisition, including when container
// readiness fails after allocation. Ent closes the SQL pool once it wraps that
// pool, before the container is removed. Container cleanup uses a separate
// deadline, and cleanup errors make an otherwise successful test run fail.

// integrationEntClient wraps integrationDB so the package fixture has one close
// path for its PostgreSQL driver after the test run completes.
var integrationEntClient *dbent.Client

// TestMain sets the process-local timezone to UTC before initializing the
// PostgreSQL fixture. It exits with runPostgresTests' result after that helper
// has run its deferred resource cleanup.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(runPostgresTests(m))
}

// runPostgresTests starts PostgreSQL, applies the embedded migration history,
// and returns the test run's exit code. TOKENROUTER_TEST_POSTGRES_IMAGE selects
// the image when nonblank; otherwise the pinned PostgreSQL 18 image is used.
// Startup work shares a three-minute context, while container termination gets
// an independent thirty-second budget. Setup failures print their error and
// return one. Deferred database-close and container-termination failures print
// their diagnostics and change a successful selected exit code to one. A
// container returned with a startup error still receives deferred termination.
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
		tcpostgres.WithDatabase("migration_contracts"),
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
	integrationDSN = dsn
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
	if err := postgresinfra.ApplyMigrations(ctx, integrationDB, migrations.FS); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}
