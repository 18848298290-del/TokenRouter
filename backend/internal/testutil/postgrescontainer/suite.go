//go:build integration

package postgrescontainer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// suite.go creates isolated PostgreSQL test databases from a shared migrated
// template.
// Parallel PostgreSQL tests need independent databases while the migration set
// can be applied once per process. Suite lazily starts one container, migrates
// a verification_template database, closes that template's pool, and disables
// template connections before cloning it for each test. Creation is serialized
// so every clone receives a unique name and a fully prepared template.
//
// Each test cleanup closes its pool and forcibly drops its clone after
// application cleanup callbacks registered later. The shared administrator pool
// and container remain available until Suite.Run finishes all tests and their
// cleanup. Initialization keeps partial resources reachable by Close, and a
// failed initialization in New marks the suite closed so callers cannot reuse
// an incompletely migrated template.

// Suite owns one lazily initialized PostgreSQL container and per-test migrated
// database clones. Use Run in TestMain or call Close after all tests and their
// cleanup have finished.
type Suite struct {
	// mu serializes container/template initialization, clone naming, and Close.
	// New and Close change container, admin, dsn, next, and closed under that
	// lock. Direct initialization fixtures remain serial, and drop operations require
	// the administrator pool to stay open until per-test cleanup has finished.
	mu        sync.Mutex                    // suite resource lifecycle lock
	container *tcpostgres.PostgresContainer // shared test database container
	admin     *sql.DB                       // shared database-management connection pool
	dsn       string                        // shared container connection URL
	next      uint64                        // next isolated database sequence
	closed    bool                          // subsequent initialization rejection flag
}

// Run executes m and closes suite resources after test cleanup completes. A
// cleanup error is printed and returns exit code one; successful cleanup
// retains the exit code returned by m.Run.
func (s *Suite) Run(m *testing.M) int {
	code := m.Run()
	if err := s.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "failed to clean up test database:", err)
		return 1
	}
	return code
}

// initialize prepares the shared container and migrated template on first use.
// Callers hold mu or use an isolated serial fixture; a closed suite fails
// immediately, and a nonnil admin pool identifies an already initialized path.
//
// Resources are stored on s as they are acquired so Close can release them
// after an error. Migration runs through a temporary template pool, whose close
// result is joined with the migration error. Successful migration and pool
// closure precede disabling connections to the template, allowing subsequent
// clones to use its completed schema. New closes the suite to further creation
// after an initialization failure.
func (s *Suite) initialize(ctx context.Context, migrationFS fs.FS) error {
	if s.closed {
		return errors.New("test database is closed")
	}
	if s.admin != nil {
		return nil
	}
	image := strings.TrimSpace(os.Getenv("TOKENROUTER_TEST_POSTGRES_IMAGE"))
	if image == "" {
		image = "postgres:18.1-alpine3.23"
	}
	container, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("postgres"), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	s.container = container
	if err != nil {
		return err
	}
	s.dsn, err = container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		return err
	}
	admin, err := sql.Open("postgres", s.dsn)
	if err != nil {
		return err
	}
	// Retain the administrator pool before creating or migrating the template.
	// Close can then release partial initialization resources when New rejects further creation.
	s.admin = admin
	if _, err = admin.ExecContext(ctx, "CREATE DATABASE verification_template TEMPLATE template0"); err != nil {
		return err
	}
	template, err := sql.Open("postgres", s.databaseDSN("verification_template"))
	if err != nil {
		return err
	}
	// Apply migrations and close the template pool before disabling its connections.
	// Joining both errors retains cleanup evidence when migration itself also failed.
	migrationErr := postgres.ApplyMigrations(ctx, template, migrationFS)
	if err := errors.Join(migrationErr, template.Close()); err != nil {
		return err
	}
	_, err = admin.ExecContext(ctx, "ALTER DATABASE verification_template ALLOW_CONNECTIONS false")
	return err
}

// databaseDSN replaces the path in the initialized container connection URL
// with name. It retains query options such as UTC, and invalid stored URL text
// panics because fixture initialization must supply a parseable DSN.
func (s *Suite) databaseDSN(name string) string {
	u, err := url.Parse(s.dsn)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}

// New returns a pool for a newly named database cloned from the migrated
// template. Its two-minute context covers initialization and clone creation,
// and setup failures fail the calling test.
//
// The suite lock protects initialization, name allocation, and CREATE DATABASE
// so concurrent tests receive distinct clones. After successful creation,
// cleanup is registered before pool construction; it can therefore drop the
// clone even if opening its pool fails. Application cleanup registered after
// New runs before pool closure and forced drop. Keep the shared suite open
// until every per-test cleanup has finished.
func (s *Suite) New(t *testing.T) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.mu.Lock()
	if err := s.initialize(ctx, migrations.FS); err != nil {
		s.closed = true
		s.mu.Unlock()
		t.Fatalf("initialize test database: %v", err)
	}
	// Allocate the clone name while initialization and creation share the suite lock.
	// Parallel New calls then clone the completed template into separate database identities.
	s.next++
	name := fmt.Sprintf("verification_%d", s.next)
	_, err := s.admin.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(name)+" TEMPLATE verification_template")
	dsn := s.databaseDSN(name)
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("create test database: %v", err)
	}
	var db *sql.DB
	// Register before opening the clone pool so a later construction failure still
	// drops its database. Later application cleanups run before this pool-close/drop sequence.
	t.Cleanup(func() {
		if db != nil {
			if err := db.Close(); err != nil {
				t.Errorf("close test database connection: %v", err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.drop(ctx, name); err != nil {
			t.Errorf("clean up test database: %v", err)
		}
	})
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	return db
}

// drop forcibly removes name through the shared administrator pool using ctx.
// Remaining connections to that database can be terminated, so callers finish
// application cleanup first and keep the suite open until deletion completes.
func (s *Suite) drop(ctx context.Context, name string) error {
	_, err := s.admin.ExecContext(ctx, "DROP DATABASE "+pq.QuoteIdentifier(name)+" WITH (FORCE)")
	return err
}

// Close marks the suite closed, closes the administrator pool, and terminates
// the container. Call it after test work and clone cleanup have ended, since
// those operations still need the shared pool.
//
// The suite lock excludes concurrent initialization and clone creation while
// resources are released. Container termination has a fresh thirty-second
// context, and its error is joined with any pool-close error. Resource
// references are cleared even after cleanup errors, so subsequent Close calls
// return nil and do not retry failed termination.
func (s *Suite) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Stop future initialization before closing shared resources. Clearing acquired
	// references after each release makes repeated Close calls finish without repeating those operations.
	s.closed = true
	var result error
	if s.admin != nil {
		result = s.admin.Close()
		s.admin = nil
	}
	if s.container != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result = errors.Join(result, s.container.Terminate(ctx))
		s.container = nil
	}
	return result
}
