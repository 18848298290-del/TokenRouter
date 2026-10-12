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

var (
	// integrationDB connects to the test container; runPostgresTests closes it before terminating the container.
	integrationDB *sql.DB

	integrationEntClient *dbent.Client
)

func TestMain(m *testing.M) { os.Exit(runPostgresTests(m)) }

func runPostgresTests(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	image := strings.TrimSpace(os.Getenv("TOKENROUTER_TEST_POSTGRES_IMAGE"))
	if image == "" {
		image = "postgres:18.1-alpine3.23"
	}
	container, err := tcpostgres.Run(ctx, image, tcpostgres.WithDatabase("billing_contracts"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
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
	if err := postgresinfra.ApplyMigrations(ctx, integrationDB, migrations.FS); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}
