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

// Repository integration tests share migrated PostgreSQL and Redis containers
// rather than constructing a database per test. TestMain fixes process-local
// dates to UTC, initializes the shared SQL/Ent resources, and requires a working
// Docker daemon in CI or strict verification mode.
//
// Local runs can exit successfully when Docker is absent. Resource setup
// failures exit immediately, while a completed test run closes shared clients
// before deferred container teardown. Connection startup retries readiness with
// a per-ping deadline and a separate elapsed-time budget. The PostgreSQL image
// can be overridden for compatibility checks; the image-selection helper
// otherwise returns its preferred tag whether or not it is already cached.

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

// TestMain initializes shared migrated PostgreSQL/Redis resources for
// integration scenarios. It selects UTC dates, skips local runs when Docker is
// absent, and fails setup under CI or strict mode.
//
// Ent and SQL share the migrated database handle, while Redis is checked before
// m.Run. Normal return closes clients and runs deferred container termination;
// setup os.Exit calls bypass deferred cleanup. Go's generated test wrapper then
// exits with the result stored by m.Run, so this function can finish normal
// teardown before returning.
func TestMain(m *testing.M) {
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
	// Normal harness return terminates the PostgreSQL fixture. Setup paths using
	// os.Exit bypass this deferred teardown, leaving this local cleanup unexecuted.
	defer func() { _ = pgContainer.Terminate(ctx) }()

	redisContainer, err := rediscontainer.Run(
		ctx,
		redisImageTag,
	)
	if err != nil {
		log.Printf("failed to start redis container: %v", err)
		os.Exit(1)
	}
	// Normal return also terminates Redis after client cleanup; termination errors
	// are ignored by the harness rather than changing the recorded test outcome.
	defer func() { _ = redisContainer.Terminate(ctx) }()

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		log.Printf("failed to get postgres dsn: %v", err)
		os.Exit(1)
	}

	// Limit readiness polling before migration setup, so an unavailable fixture
	// database ends shared harness startup after the retry budget is exhausted.
	integrationDB, err = openSQLWithRetry(ctx, dsn, 30*time.Second)
	if err != nil {
		log.Printf("failed to open sql db: %v", err)
		os.Exit(1)
	}
	if err := infra.ApplyMigrations(ctx, integrationDB, migrations.FS); err != nil {
		log.Printf("failed to apply db migrations: %v", err)
		os.Exit(1)
	}

	// Apply handwritten migrations before sharing SQL with Ent, so repository
	// scenarios exercise the deployed schema and its triggers rather than
	// auto-migration.
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
		Addr: fmt.Sprintf("%s:%d", redisHost, redisPort.Int()), // mapped fixture Redis endpoint
		DB:   0,                                                // fixture Redis database number
	})
	if err := integrationRedis.Ping(ctx).Err(); err != nil {
		log.Printf("failed to ping redis: %v", err)
		os.Exit(1)
	}

	m.Run()

	// Close shared clients on normal return before container teardown; the Ent
	// driver and SQL pool share ownership, and these cleanup errors are ignored.
	_ = integrationEntClient.Close()
	_ = integrationRedis.Close()
	_ = integrationDB.Close()
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
