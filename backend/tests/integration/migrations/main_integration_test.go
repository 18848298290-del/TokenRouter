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

// Migration assertions run against PostgreSQL with the embedded SQL history
// already applied, allowing later tests to compare current schema and rebuild
// historical fixtures. The package uses UTC for Go time values and the database
// session, while one container and pool remain available through the test run.

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
// their diagnostics without replacing the selected exit code.
func runPostgresTests(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	image := strings.TrimSpace(os.Getenv("TOKENROUTER_TEST_POSTGRES_IMAGE"))
	if image == "" {
		image = "postgres:18.1-alpine3.23"
	}
	container, err := tcpostgres.Run(ctx, image, tcpostgres.WithDatabase("migration_contracts"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Use an independent cleanup context so expired startup work still
	// permits container termination. Termination errors are printed and
	// leave the selected exit code unchanged.
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
	integrationDSN = dsn
	integrationDB, err = sql.Open("postgres", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Register client closure after container cleanup so the driver
	// releases its pool first when deferred calls unwind, including
	// failures while applying the embedded migrations.
	integrationEntClient = dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, integrationDB)))
	defer func() {
		if err := integrationEntClient.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()
	if err := postgresinfra.ApplyMigrations(ctx, integrationDB, migrations.FS); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}
