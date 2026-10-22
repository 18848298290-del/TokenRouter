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

// main_integration_test.go supplies migrated PostgreSQL and Redis fixtures for
// Usage repository scenarios. Tests share SQL, Ent, and Redis clients for the
// process lifetime and use UTC for Go and SQL dates. The PostgreSQL image
// override supports database compatibility checks, while the Docker availability
// policy permits local skips and requires a runtime for CI or strict acceptance.
//
// The runner returns its status after deferred cleanup, including on partial
// setup failures. Ent takes responsibility for closing the shared SQL pool when
// constructed. Client cleanup precedes container removal, and each container has
// an independent teardown deadline. Cleanup failures are logged and turn a
// successful test status into failure.

// Integration image defaults pin PostgreSQL and Redis fixture versions; the
// PostgreSQL preferred tag can be overridden by the compatibility-test
// environment.
const (
	redisImageTag    = "redis:8.4-alpine"         // shared Redis fixture image
	postgresImageTag = "postgres:18.1-alpine3.23" // preferred migrated PostgreSQL fixture
)

// Shared integration clients are published by TestMain after setup and closed
// after m.Run; tests own fixture isolation before using these process-wide
// resources.
var (
	integrationDB        *sql.DB             // migrated shared SQL pool
	integrationEntClient *dbent.Client       // Ent client sharing SQL resources
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

	// Set UTC before stores construct local Calendars so suite dates and SQL
	// session dates share the same fixture baseline across test methods.
	time.Local = time.UTC

	if !dockerIsAvailable(ctx) {
		// Container-backed assertions cannot run without Docker. Strict environments
		// fail immediately; ordinary local runs use the documented successful skip
		// path.
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

	// Limit readiness polling before migration setup, so an unavailable fixture
	// database ends shared harness startup after the retry budget is exhausted.
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

	// Apply handwritten migrations before sharing SQL with Ent so repository
	// scenarios exercise the deployed schema and its triggers during queries.
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
		Addr: fmt.Sprintf("%s:%d", redisHost, redisPort.Int()), // mapped fixture Redis endpoint
		DB:   0,                                                // fixture Redis database number
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

// dockerIsAvailable reports whether docker info succeeds under the supplied
// context. It inherits the process environment; command failures return false so
// TestMain can apply its local-versus-strict startup policy.
func dockerIsAvailable(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "docker", "info")
	cmd.Env = os.Environ()
	return cmd.Run() == nil
}

// selectDockerImage returns the PostgreSQL override for a preferred postgres tag
// when configured. Otherwise it probes the preferred image and returns that same
// tag for both cache outcomes; callers perform actual container startup.
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

// dockerImageExists reports whether Docker can inspect the requested image under
// ctx. It inherits the process environment and discards command output,
// returning false for any command failure.
func dockerImageExists(ctx context.Context, image string) bool {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", image)
	cmd.Env = os.Environ()
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// openSQLWithRetry returns a PostgreSQL pool after a successful bounded
// readiness ping. It retries until the elapsed timeout deadline; failed pool
// creation or ping records the last error.
//
// Ping failure closes the just-opened pool before pausing, so the next attempt
// does not retain another unusable handle. Each ping has a two-second child
// deadline, while retry pauses use fixed sleeps and do not select context
// cancellation. The elapsed deadline is tested between attempts, so a final
// attempt and its pause can extend beyond it.
func openSQLWithRetry(ctx context.Context, dsn string, timeout time.Duration) (*sql.DB, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			lastErr = err
			// Pause readiness retries to let container startup advance; this
			// fixed sleep does not observe context cancellation until the next
			// command or ping.
			time.Sleep(250 * time.Millisecond)
			continue
		}

		if err := pingWithTimeout(ctx, db, 2*time.Second); err != nil {
			lastErr = err
			// The readiness ping failed after pool creation. Close this handle
			// before the next attempt so unsuccessful startup cannot accumulate
			// pool resources.
			_ = db.Close()
			// Pause readiness retries to let container startup advance; this
			// fixed sleep does not observe context cancellation until the next
			// command or ping.
			time.Sleep(250 * time.Millisecond)
			continue
		}

		return db, nil
	}

	return nil, fmt.Errorf("db not ready after %s: %w", timeout, lastErr)
}

// pingWithTimeout tests pool readiness under a child context with the supplied
// deadline. The child is canceled on return, and cancellation or database errors
// propagate to the startup retry loop.
func pingWithTimeout(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return db.PingContext(pingCtx)
}
