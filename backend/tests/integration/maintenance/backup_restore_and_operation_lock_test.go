//go:build integration

package maintenance_test

import (
	"context"
	"database/sql"
	"io"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/TokenFlux/TokenRouter/internal/backup"
	bp "github.com/TokenFlux/TokenRouter/internal/backup/provider"
	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	idempotencypg "github.com/TokenFlux/TokenRouter/internal/idempotency/postgres"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/ops/maintenance"
	"github.com/TokenFlux/TokenRouter/migrations"
)

// A reclaimed maintenance lease has a new owner, so renewal and completion
// compare the current acquisition identity. Restore scenarios check selected
// rows after SQL errors, cancellation and reader failure, then verify a local
// archive round trip against isolated PostgreSQL containers. Local psql and
// pg_dump binaries execute the SQL; archive storage and settings are fixtures.

// brokenSQLReader injects a later input failure after emitting a SQL fragment.
type brokenSQLReader struct {
	// sent selects the injected failure after Read has attempted its one
	// SQL fragment. The marker remains true for every later call on this
	// reader.
	sent bool // first read already attempted
}

// backupSettings stores backup metadata settings in a caller-owned in-memory
// map.
type backupSettings struct {
	// values is initialized by the restore test and shared with backup
	// metadata readers and writers during its sequential service
	// operations.
	values map[string]string // stored backup metadata settings
}

// TestSystemLockOwnership expires a PostgreSQL maintenance lease before
// acquiring its replacement. One scenario renews the new owner directly; the
// other requires the old release to fail while the replacement row remains
// processing.
func TestSystemLockOwnership(t *testing.T) {
	ctx := context.Background()
	integrationDB := maintenanceDatabase(t)
	repo := idempotencypg.NewIdempotencyRepository(integrationDB)
	operationStore := idempotencypg.NewOperationLeaseStore(integrationDB)
	for _, scenario := range []string{"reclaim-renew", "late-release"} {
		t.Run(scenario, func(t *testing.T) {
			// Clear the prior scenario's global lease row before
			// acquisition so the two cases observe replacement
			// ownership independently in the same migrated
			// database.
			_, err := integrationDB.ExecContext(ctx, "DELETE FROM idempotency_records WHERE scope=$1", "admin.system.operations.global_lock")
			if err != nil {
				t.Fatal(err)
			}
			// An hour-long lease puts ordinary renewal ticks
			// outside this short scenario; the renewal assertion
			// below invokes the ownership-checked store directly.
			svc := maintenance.NewSystemOperationLockService(operationStore, maintenance.Options{Log: logging.LegacyPrintf, ProcessingTimeout: time.Hour, SystemOperationTTL: 2 * time.Hour})
			first, err := svc.Acquire(ctx, "test-first")
			if err != nil {
				t.Fatal(err)
			}
			// Release stops and joins the first owner's renewal
			// loop on every exit; its later ownership error is
			// discarded during fixture cleanup.
			defer func() { _ = svc.Release(ctx, first, false, "cleanup") }()
			// Expire availability in the database so the second
			// acquisition can take over immediately, without
			// waiting for the configured hour-long processing
			// lease.
			_, err = integrationDB.ExecContext(ctx, "UPDATE idempotency_records SET locked_until=NOW()-INTERVAL '1 second' WHERE scope=$1", "admin.system.operations.global_lock")
			if err != nil {
				t.Fatal(err)
			}
			second, err := svc.Acquire(ctx, "test-second")
			if err != nil {
				t.Fatal(err)
			}
			// The replacement owner also releases its renewal loop
			// before database cleanup; this deferred release
			// discards errors outside the scenario's ownership
			// assertions.
			defer func() { _ = svc.Release(ctx, second, true, "") }()
			hash := idempotency.HashIdempotencyKey("global-system-operation-lock")
			current, err := repo.GetByScopeAndKeyHash(ctx, "admin.system.operations.global_lock", hash)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "reclaim-renew" {
				// Supply the replacement operation ID and
				// stored owner text together so a successful
				// renewal checks the persisted acquisition
				// identity after takeover.
				ok, err := operationStore.RenewOperation(ctx, current.ID, second.OperationID(), *current.ResponseBody, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					t.Errorf("新持有者不能续租；数据库指纹仍为 %s", current.RequestFingerprint)
				}
			} else {
				// Attempt completion with the replaced owner,
				// then reread the row to detect any stale
				// release that overwrote the new processing
				// acquisition.
				if err := svc.Release(ctx, first, true, ""); err == nil {
					t.Fatal("旧持有者释放必须报告丢失所有权")
				}
				current, err = repo.GetByScopeAndKeyHash(ctx, "admin.system.operations.global_lock", hash)
				if err != nil {
					t.Fatal(err)
				}
				if current.Status != idempotency.IdempotencyStatusProcessing {
					t.Errorf("旧持有者释放覆盖新锁: %s", current.Status)
				}
			}
		})
	}
}

// TestRestoreSQLFailure exercises SQL error, deadline cancellation and reader
// failure through psql, then a local backup round trip. It checks reported
// errors and retention of the selected fixture row, plus successful committed
// and archived restore contents.
func TestRestoreSQLFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	// Release the test-wide deadline timer on every exit; database and
	// process calls below share this bounded context while fixture cleanup
	// uses separate contexts.
	defer cancel()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("test_restore"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	// Run may return a container alongside a startup error. Register cleanup
	// before checking the error, and give termination its own deadline so test
	// cancellation still allows Docker to remove the partially started instance.
	if pg != nil {
		t.Cleanup(func() {
			cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancelCleanup()
			if err := pg.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate PostgreSQL container: %v", err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	host, err := pg.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := pg.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Close the SQL pool before container termination so connections are
	// released against the running database; this defer discards pool-close
	// errors during cleanup.
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(ctx, "CREATE TABLE fixture(id INT PRIMARY KEY); INSERT INTO fixture VALUES(1)")
	if err != nil {
		t.Fatal(err)
	}
	dumper := bp.NewPgDumper(bp.DatabaseOptions{Host: host, Port: port.Int(), User: "postgres", Password: "postgres", DBName: "test_restore", SSLMode: "disable"})
	err = dumper.Restore(ctx, strings.NewReader("DELETE FROM fixture; INSERT INTO definitely_missing_table VALUES(1); INSERT INTO fixture VALUES(2);"))
	var count int
	if e := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture WHERE id=1").Scan(&count); e != nil {
		t.Fatal(e)
	}
	t.Logf("原行保留=%d，Restore error=%v", count, err)
	if err == nil {
		t.Error("SQL 失败却报告恢复成功")
	}
	if count != 1 {
		t.Error("失败恢复未保持原数据库")
	}
	// Install row 2 with a successful restore before cancellation and
	// reader-failure cases; their later reads use this committed row as the
	// retained-state observation.
	err = dumper.Restore(ctx, strings.NewReader("DELETE FROM fixture; INSERT INTO fixture VALUES(2);"))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture WHERE id=2").Scan(&count); err != nil || count != 1 {
		t.Fatalf("成功恢复未提交: count=%d error=%v", count, err)
	}
	// The five-second SQL sleep outlasts this short child deadline when
	// reached. The assertion checks the returned error and retained row
	// without a process-start barrier.
	cancelled, cancelRestore := context.WithTimeout(ctx, 80*time.Millisecond)
	err = dumper.Restore(cancelled, strings.NewReader("DELETE FROM fixture; SELECT pg_sleep(5); INSERT INTO fixture VALUES(3);"))
	// Release the child deadline timer after Restore returns so later
	// archive operations retain the still-live parent context and its
	// separate test budget.
	cancelRestore()
	if err == nil {
		t.Fatal("取消恢复不能报告成功")
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture WHERE id=2").Scan(&count); err != nil || count != 1 {
		t.Fatalf("取消恢复未回滚: count=%d error=%v", count, err)
	}
	// The reader emits SQL before failing, exercising Restore's
	// cancellation request before stdin closure. Verify the error and
	// retained row for this input-failure scenario.
	err = dumper.Restore(ctx, &brokenSQLReader{})
	if err == nil {
		t.Fatal("损坏输入不能报告成功")
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture WHERE id=2").Scan(&count); err != nil || count != 1 {
		t.Fatalf("输入失败未回滚: count=%d error=%v", count, err)
	}

	// Archive row 2 through pg_dump, gzip and local storage, then mutate
	// the live row; recovery must read the archive to restore the selected
	// content.
	path := t.TempDir()
	settings := &backupSettings{values: map[string]string{}}
	core := backup.New(settings, backup.Options{LocalPath: path, Now: time.Now}, nil, nil, bp.NewLocalBackupStore(path), bp.NewArchive(dumper, 0), func(ctx context.Context) (func(), bool, error) {
		return postgresinfra.TryAcquireDBAdvisoryLockWithError(ctx, db, postgresinfra.HashAdvisoryLockID("maintenance:database-heavy"))
	})
	// Stop the backup service before closing its SQL pool so any service
	// shutdown work still has the database and local archive fixture
	// available.
	defer core.Stop()
	record, err := core.CreateBackup(ctx, "manual", 1)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "completed" {
		t.Fatalf("备份未完成: %s", record.Status)
	}
	if _, err = db.ExecContext(ctx, "UPDATE fixture SET id=9"); err != nil {
		t.Fatal(err)
	}
	if err = core.RestoreBackup(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture WHERE id=2").Scan(&count); err != nil || count != 1 {
		t.Fatalf("归档恢复内容错误: count=%d error=%v", count, err)
	}
}

// Read copies one SQL fragment on its first call and returns ErrUnexpectedEOF
// afterward. A short destination keeps only that copied prefix because the
// fixture records the first call as sent.
func (r *brokenSQLReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	// Set the marker so any subsequent buffered read injects the intended
	// input failure. This keeps repeated SQL emission out of the
	// reader-failure fixture.
	r.sent = true
	return copy(p, "DELETE FROM fixture; INSERT INTO fixture VALUES(4);"), nil
}

// GetValue reads the fixture settings map and returns an empty string for a
// missing key. Absence succeeds here, letting backup metadata readers apply
// their empty-value behavior.
func (s *backupSettings) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

// Set stores backup metadata in the fixture map and reports success. The caller
// initializes the map before backup operations; access follows this test's
// sequential service calls.
func (s *backupSettings) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

// maintenanceDatabase starts an isolated PostgreSQL container and applies the
// published migrations to its database. Test cleanup closes the returned pool
// before terminating the container, reporting both cleanup failures.
func maintenanceDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("test_maintenance"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	// Run may return a container alongside a startup error. Register cleanup
	// before checking the error, and give termination its own deadline so test
	// cancellation still allows Docker to remove the partially started instance.
	if pg != nil {
		t.Cleanup(func() {
			cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancelCleanup()
			if err := pg.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate PostgreSQL container: %v", err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The later cleanup registration runs first, closing
		// connections before container termination; pool-close failures
		// become test errors instead of disappearing during teardown.
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := postgresinfra.ApplyMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return db
}
