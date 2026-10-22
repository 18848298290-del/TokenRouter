//go:build integration

package payment_test

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
	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// main_integration_test.go supplies a shared PostgreSQL pool and an Ent
// wrapper for persistent refund state, while ordinary cases in the same build
// still construct their own SQLite fixtures. TestMain selects Gin test mode and
// UTC before starting this SQL fixture and running the package cases.
//
// Startup and migrations share a three-minute budget. Container termination is
// registered for each returned container, including startup failures. The Ent
// cleanup closes its pool before that container is terminated. Cleanup uses a
// fresh bounded context and logs resource-close errors, which turn a
// successful test exit code into failure. Setup failures return one after
// already acquired resources are released.

// integrationDB and integrationEntClient share the migrated PostgreSQL fixture
// for payment cases; runPostgresTests owns their setup and cleanup after m.Run.
var (
	// integrationDB is borrowed by case helpers while the runner retains
	// its pool until test cleanup finishes; closing integrationEntClient
	// releases that same pool.
	integrationDB *sql.DB
	// integrationEntClient wraps the shared pool and supplies case
	// transactions. Cases finish their own transactions before runner
	// cleanup closes this wrapper.
	integrationEntClient *dbent.Client
)

// TestMain selects Gin test mode and UTC before the PostgreSQL-backed payment
// runner. It exits with that runner's result after its package tests and
// deferred resource cleanup have finished.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	time.Local = time.UTC
	os.Exit(runPostgresTests(m))
}

// runPostgresTests starts the shared migrated payment PostgreSQL fixture, runs
// package tests, and returns their exit code. TOKENROUTER_TEST_POSTGRES_IMAGE
// selects an optional image override, while setup and migrations share a
// three-minute context.
//
// Container cleanup is registered before startup errors are handled, and SQL
// cleanup is registered before Ent construction so partial setup is released.
// The Ent wrapper closes the shared pool before container termination uses a
// fresh thirty-second budget. Setup errors return one; cleanup failures are logged
// and change a successful runner result to one.
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
		tcpostgres.WithDatabase("payment_contracts"),
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
	if err := postgresinfra.ApplyMigrations(ctx, integrationDB, migrations.FS); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}
