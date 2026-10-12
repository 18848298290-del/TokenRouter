//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq"
	redisclient "github.com/redis/go-redis/v9"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	infra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
	"github.com/TokenFlux/TokenRouter/migrations"
)

const (
	redisImageTag    = "redis:8.4-alpine"
	postgresImageTag = "postgres:18.1-alpine3.23"
)

var (
	integrationDB        *sql.DB
	integrationEntClient *dbent.Client
	integrationRedis     *redisclient.Client
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Use UTC for test timestamps.
	time.Local = time.UTC

	if !dockerIsAvailable(ctx) {
		// CI and strict verification require Docker.
		if os.Getenv("CI") != "" || os.Getenv("TOKENROUTER_VERIFY_STRICT") == "1" {
			log.Printf("docker is not available (CI=true); failing integration tests")
			os.Exit(1)
		}
		log.Printf("docker is not available; skipping integration tests (start Docker to enable)")
		os.Exit(0)
	}

	postgresImage := selectDockerImage(ctx, postgresImageTag)
	pgContainer, err := tcpostgres.Run(
		ctx,
		postgresImage,
		tcpostgres.WithDatabase("tokenrouter_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		log.Printf("failed to start postgres container: %v", err)
		os.Exit(1)
	}
	defer func() { _ = pgContainer.Terminate(ctx) }()

	redisContainer, err := rediscontainer.Run(
		ctx,
		redisImageTag,
	)
	if err != nil {
		log.Printf("failed to start redis container: %v", err)
		os.Exit(1)
	}
	defer func() { _ = redisContainer.Terminate(ctx) }()

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		log.Printf("failed to get postgres dsn: %v", err)
		os.Exit(1)
	}

	integrationDB, err = openSQLWithRetry(ctx, dsn, 30*time.Second)
	if err != nil {
		log.Printf("failed to open sql db: %v", err)
		os.Exit(1)
	}
	if err := infra.ApplyMigrations(ctx, integrationDB, migrations.FS); err != nil {
		log.Printf("failed to apply db migrations: %v", err)
		os.Exit(1)
	}

	// Use the migrated database for Ent queries.
	drv := entsql.OpenDB(dialect.Postgres, integrationDB)
	integrationEntClient = dbent.NewClient(dbent.Driver(drv))

	redisHost, err := redisContainer.Host(ctx)
	if err != nil {
		log.Printf("failed to get redis host: %v", err)
		os.Exit(1)
	}
	redisPort, err := redisContainer.MappedPort(ctx, "6379/tcp")
	if err != nil {
		log.Printf("failed to get redis port: %v", err)
		os.Exit(1)
	}

	integrationRedis = redisclient.NewClient(&redisclient.Options{
		Addr: fmt.Sprintf("%s:%d", redisHost, redisPort.Int()),
		DB:   0,
	})
	if err := integrationRedis.Ping(ctx).Err(); err != nil {
		log.Printf("failed to ping redis: %v", err)
		os.Exit(1)
	}

	m.Run()

	_ = integrationEntClient.Close()
	_ = integrationRedis.Close()
	_ = integrationDB.Close()
}

func dockerIsAvailable(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "docker", "info")
	cmd.Env = os.Environ()
	return cmd.Run() == nil
}

// selectDockerImage resolves the container image for integration tests.
//
// TOKENROUTER_TEST_POSTGRES_IMAGE overrides the image to test supported older versions.
// PostgreSQL versions differ in SQL behavior: jsonpath .datetime() accepts ISO-8601
// "Z" suffixes starting in PostgreSQL 17. Running only PostgreSQL 18 would miss
// compatibility failures on versions 14 through 16.
//
//	TOKENROUTER_TEST_POSTGRES_IMAGE=postgres:15-alpine go test -tags integration ./internal/ops/postgres/
func selectDockerImage(ctx context.Context, preferred string) string {
	if override := strings.TrimSpace(os.Getenv("TOKENROUTER_TEST_POSTGRES_IMAGE")); override != "" &&
		strings.HasPrefix(preferred, "postgres:") {
		return override
	}
	if dockerImageExists(ctx, preferred) {
		return preferred
	}

	return preferred
}

func dockerImageExists(ctx context.Context, image string) bool {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", image)
	cmd.Env = os.Environ()
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

func openSQLWithRetry(ctx context.Context, dsn string, timeout time.Duration) (*sql.DB, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}

		if err := pingWithTimeout(ctx, db, 2*time.Second); err != nil {
			lastErr = err
			_ = db.Close()
			time.Sleep(250 * time.Millisecond)
			continue
		}

		return db, nil
	}

	return nil, fmt.Errorf("db not ready after %s: %w", timeout, lastErr)
}

func pingWithTimeout(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return db.PingContext(pingCtx)
}
