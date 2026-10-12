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

// Suite shares a container within the test process and clones a migrated database for each New call.
// Call Run from TestMain to terminate the container after test cleanup finishes.
type Suite struct {
	mu        sync.Mutex
	container *tcpostgres.PostgresContainer
	admin     *sql.DB
	dsn       string
	next      uint64
	closed    bool
}

// Run executes tests and terminates the container, returning a failing exit code if cleanup fails.
func (s *Suite) Run(m *testing.M) int {
	code := m.Run()
	if err := s.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "清理测试数据库失败:", err)
		return 1
	}
	return code
}

// initialize starts the container on first use, so an empty test run needs no Docker instance.
func (s *Suite) initialize(ctx context.Context, migrationFS fs.FS) error {
	if s.closed {
		return errors.New("测试数据库已关闭")
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
	// Keep resources reachable by Close if initialization fails; New then marks the suite closed.
	s.admin = admin
	if _, err = admin.ExecContext(ctx, "CREATE DATABASE verification_template TEMPLATE template0"); err != nil {
		return err
	}
	template, err := sql.Open("postgres", s.databaseDSN("verification_template"))
	if err != nil {
		return err
	}
	migrationErr := postgres.ApplyMigrations(ctx, template, migrationFS)
	if err := errors.Join(migrationErr, template.Close()); err != nil {
		return err
	}
	_, err = admin.ExecContext(ctx, "ALTER DATABASE verification_template ALLOW_CONNECTIONS false")
	return err
}

// databaseDSN selects a test database while retaining the container connection options.
func (s *Suite) databaseDSN(name string) string {
	u, err := url.Parse(s.dsn)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}

// New creates an isolated database. Application cleanup registered afterward runs before database cleanup.
func (s *Suite) New(t *testing.T) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s.mu.Lock()
	if err := s.initialize(ctx, migrations.FS); err != nil {
		s.closed = true
		s.mu.Unlock()
		t.Fatalf("初始化测试数据库: %v", err)
	}
	s.next++
	name := fmt.Sprintf("verification_%d", s.next)
	_, err := s.admin.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(name)+" TEMPLATE verification_template")
	dsn := s.databaseDSN(name)
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("创建测试数据库: %v", err)
	}
	var db *sql.DB
	t.Cleanup(func() {
		if db != nil {
			if err := db.Close(); err != nil {
				t.Errorf("关闭测试数据库连接: %v", err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.drop(ctx, name); err != nil {
			t.Errorf("清理测试数据库: %v", err)
		}
	})
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("连接测试数据库: %v", err)
	}
	return db
}

// drop uses FORCE to terminate remaining connections before deleting the test database.
func (s *Suite) drop(ctx context.Context, name string) error {
	_, err := s.admin.ExecContext(ctx, "DROP DATABASE "+pq.QuoteIdentifier(name)+" WITH (FORCE)")
	return err
}

// Close releases the shared container and connections. Subsequent calls return nil.
func (s *Suite) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
