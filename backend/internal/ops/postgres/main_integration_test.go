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

// main_integration_test.go manages the shared PostgreSQL and Redis fixtures for
// Ops repository tests. The runner applies embedded migrations before exposing
// SQL and Ent clients, selects UTC for date-sensitive queries, and permits local
// runs to skip when Docker is unavailable. CI and strict verification require
// Docker before fixture creation begins.
//
// Resource cleanup is registered as setup progresses, including containers
// returned with startup errors. The SQL pool has one close path through Ent
// after wrapping, and clients close before their containers. Independent
// container deadlines allow teardown after failed setup. Cleanup errors are
// logged and make an otherwise successful run fail.

// These image tags select default PostgreSQL and Redis fixture containers;
// selectDockerImage can override the PostgreSQL tag for version-compatibility
// runs.
const (
	redisImageTag    = "redis:8.4-alpine"         // default Redis fixture image
	postgresImageTag = "postgres:18.1-alpine3.23" // default PostgreSQL fixture image
)

// These package-level clients share migrated integration storage. TestMain
// publishes them before m.Run and closes the wrappers after tests return.
var (
	integrationDB        *sql.DB             // shared migrated SQL pool
	integrationEntClient *dbent.Client       // ORM wrapper around shared pool
	integrationRedis     *redisclient.Client // shared Redis fixture client
)

// TestMain runs the shared PostgreSQL and Redis integration fixtures in UTC.
// The runner completes deferred cleanup before its status becomes the process
// exit code, including when fixture initialization fails.
func TestMain(m *testing.M) {
	os.Exit(runIntegrationTests(m))
}

// runIntegrationTests initializes migrated PostgreSQL and Redis fixtures, runs
// package tests, and returns the process status. Missing Docker skips ordinary
// local runs and fails CI or strict verification before resources are created.
//
// Acquired resources register cleanup before later setup can fail. Database
// closure belongs to the SQL pool until Ent wraps it, then the Ent client closes
// that shared pool. Clients close before containers, and each container receives
// its own background cleanup deadline. Cleanup failures turn a successful test
// status into one while retaining an existing failure status.
func runIntegrationTests(m *testing.M) (exitCode int) {
	ctx := context.Background()

	// Set the process-local timezone before fixture startup so date-sensitive
	// integration code and explicit UTC SQL connection options use the same test
	// convention.
	time.Local = time.UTC

	if !dockerIsAvailable(ctx) {
		// A missing Docker runtime is fatal for CI or strict acceptance, while
		// ordinary local runs return successfully before creating any fixtures.
		if os.Getenv("CI") != "" || os.Getenv("TOKENROUTER_VERIFY_STRICT") == "1" {
			log.Printf("docker is not available (CI=true); failing integration tests")
			return 1
		}
		log.Printf("docker is not available; skipping integration tests (start Docker to enable)")
		return 0
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
	// Startup can return a container together with a readiness error. Register
	// termination before checking that error so partial startup releases Docker
	// resources with a cleanup deadline independent of fixture setup.
	if pgContainer != nil {
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := pgContainer.Terminate(cleanupCtx); err != nil {
				log.Printf("failed to terminate postgres container: %v", err)
				if exitCode == 0 {
					exitCode = 1
				}
			}
		}()
	}
	if err != nil {
		log.Printf("failed to start postgres container: %v", err)
		return 1
	}

	redisContainer, err := rediscontainer.Run(
		ctx,
		redisImageTag,
	)
	// Redis can also return an allocated container on startup failure. Its
	// later defer runs before PostgreSQL teardown and receives a separate budget.
	if redisContainer != nil {
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := redisContainer.Terminate(cleanupCtx); err != nil {
				log.Printf("failed to terminate redis container: %v", err)
				if exitCode == 0 {
					exitCode = 1
				}
			}
		}()
	}
	if err != nil {
		log.Printf("failed to start redis container: %v", err)
		return 1
	}

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		log.Printf("failed to get postgres dsn: %v", err)
		return 1
	}

	integrationDB, err = openSQLWithRetry(ctx, dsn, 30*time.Second)
	if err != nil {
		log.Printf("failed to open sql db: %v", err)
		return 1
	}
	// Migration failure still closes the newly acquired pool. Once Ent wraps
	// that pool, its Close owns the same resource and replaces direct SQL closure.
	defer func() {
		var closeErr error
		if integrationEntClient != nil {
			closeErr = integrationEntClient.Close()
		} else {
			closeErr = integrationDB.Close()
		}
		if closeErr != nil {
			log.Printf("failed to close postgres client: %v", closeErr)
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}()
	if err := infra.ApplyMigrations(ctx, integrationDB, migrations.FS); err != nil {
		log.Printf("failed to apply db migrations: %v", err)
		return 1
	}

	// Wrap the migrated SQL pool for Ent access so raw and ORM fixture queries use
	// the same schema and stored test rows.
	drv := entsql.OpenDB(dialect.Postgres, integrationDB)
	integrationEntClient = dbent.NewClient(dbent.Driver(drv))

	redisHost, err := redisContainer.Host(ctx)
	if err != nil {
		log.Printf("failed to get redis host: %v", err)
		return 1
	}
	redisPort, err := redisContainer.MappedPort(ctx, "6379/tcp")
	if err != nil {
		log.Printf("failed to get redis port: %v", err)
		return 1
	}

	integrationRedis = redisclient.NewClient(&redisclient.Options{
		Addr: fmt.Sprintf("%s:%d", redisHost, redisPort.Int()),
		DB:   0,
	})
	// Register client closure before its readiness ping so a failed connection
	// attempt still releases Redis resources before either container is terminated.
	defer func() {
		if err := integrationRedis.Close(); err != nil {
			log.Printf("failed to close redis client: %v", err)
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}()
	if err := integrationRedis.Ping(ctx).Err(); err != nil {
		log.Printf("failed to ping redis: %v", err)
		return 1
	}

	return m.Run()
}

// dockerIsAvailable runs docker info with the supplied context and process
// environment. A successful exit returns true; command lookup, cancellation, or
// daemon failures return false for TestMain's skip/fail policy.
func dockerIsAvailable(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "docker", "info")
	cmd.Env = os.Environ()
	return cmd.Run() == nil
}

// selectDockerImage returns the supplied image or a trimmed PostgreSQL override.
// The local-image check returns the supplied name for either result; it does
// not select a replacement image when that name is unavailable locally.
//
// TOKENROUTER_TEST_POSTGRES_IMAGE selects another PostgreSQL image for the same
// migration and query fixtures. TestMain applies migrations.FS to that container
// before publishing its SQL pool, so the suite exercises the selected database
// implementation.
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

// dockerImageExists runs docker image inspect with inherited environment and
// supplied context. A nonzero or failed command returns false; stdout and
// stderr are discarded and no image is downloaded here.
func dockerImageExists(ctx context.Context, image string) bool {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", image)
	cmd.Env = os.Environ()
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// openSQLWithRetry returns an opened SQL pool after a successful bounded ping
// or the last readiness error when its elapsed-time deadline ends. Callers close
// the returned pool.
//
// Failed opens and pings retry after an unconditional quarter-second sleep;
// failed-ping pools are closed before retrying. The outer loop checks elapsed
// timeout, while individual pings derive a two-second context from ctx. Caller
// cancellation can fail pings but does not stop those sleeps or the outer loop
// immediately. A nonpositive timeout skips all attempts and returns the wrapped
// nil last-error value in its formatting path.
func openSQLWithRetry(ctx context.Context, dsn string, timeout time.Duration) (*sql.DB, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	// Retry against the outer elapsed-time deadline; individual parent-context
	// failure does not end the loop or its fixed sleeps.
	for time.Now().Before(deadline) {
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}

		// A successful Open alone does not establish database readiness; probe before
		// returning and close this attempt's pool when that probe fails.
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

// pingWithTimeout derives a deadline context from the supplied parent and pings
// an existing SQL pool. It cancels the child on return and leaves pool
// ownership with its caller.
func pingWithTimeout(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return db.PingContext(pingCtx)
}
