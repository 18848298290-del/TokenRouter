//go:build integration

package billing_test

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

// main_integration_test.go provides a migrated PostgreSQL database and an Ent
// client using its connection pool. Committed fixtures clean shared tables
// after their cases, while repository suites use caller-owned transactions for
// rollback isolation. TestMain starts the container before invoking the package
// runner, so integration-tagged and ordinary cases in this package use the same
// migrated schema.
//
// Startup failures return exit code one after releasing resources already
// acquired, including containers returned with readiness errors. Ent closes
// the pool before deferred container termination runs, and both cleanup paths
// log errors. Container cleanup uses a fresh bounded context after the setup
// budget has ended.
// Cleanup errors turn a successful runner exit code into failure.

// integrationDB and integrationEntClient share the migrated billing database
// for package tests; runPostgresTests owns their setup and closes them after
// m.Run.
var (
	// integrationDB remains available through package test cleanup and
	// shares its pool with integrationEntClient, whose Close releases that
	// pool before container termination.
	integrationDB *sql.DB

	// integrationEntClient wraps integrationDB for Ent fixtures; cases
	// borrow it and keep their own transactions separate from its
	// package-wide connection lifetime.
	integrationEntClient *dbent.Client
)

// TestMain starts the shared billing PostgreSQL fixture and exits with its
// runner result. Database setup precedes all cases, and deferred resource
// cleanup completes before process exit.
func TestMain(m *testing.M) { os.Exit(runPostgresTests(m)) }

// runPostgresTests prepares the shared migrated PostgreSQL fixture, runs
// package tests, and returns their exit code. The image can be selected through
// TOKENROUTER_TEST_POSTGRES_IMAGE, and startup plus migrations share a
// three-minute budget.
//
// Each acquired resource registers deferred cleanup before the next setup step,
// including a container returned alongside a startup error.
// The Ent client closes its shared pool before container termination uses a
// fresh thirty-second context. Setup errors return one; cleanup errors are
// logged and turn a successful test runner result into failure.
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
		tcpostgres.WithDatabase("billing_contracts"),
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
