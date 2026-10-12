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

// brokenSQLReader returns a read error after valid SQL to simulate a corrupt gzip trailer.
type brokenSQLReader struct{ sent bool }

// Test settings isolate archive metadata; archive contents come from PostgreSQL.
type backupSettings struct{ values map[string]string }

// TestSystemLockOwnership checks lock ownership in an isolated test database.
func TestSystemLockOwnership(t *testing.T) {
	ctx := context.Background()
	integrationDB := maintenanceDatabase(t)
	repo := idempotencypg.NewIdempotencyRepository(integrationDB)
	operationStore := idempotencypg.NewOperationLeaseStore(integrationDB)
	for _, scenario := range []string{"reclaim-renew", "late-release"} {
		t.Run(scenario, func(t *testing.T) {
			_, err := integrationDB.ExecContext(ctx, "DELETE FROM idempotency_records WHERE scope=$1", "admin.system.operations.global_lock")
			if err != nil {
				t.Fatal(err)
			}
			svc := maintenance.NewSystemOperationLockService(operationStore, maintenance.Options{Log: logging.LegacyPrintf, ProcessingTimeout: time.Hour, SystemOperationTTL: 2 * time.Hour})
			first, err := svc.Acquire(ctx, "test-first")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = svc.Release(ctx, first, false, "cleanup") }()
			_, err = integrationDB.ExecContext(ctx, "UPDATE idempotency_records SET locked_until=NOW()-INTERVAL '1 second' WHERE scope=$1", "admin.system.operations.global_lock")
			if err != nil {
				t.Fatal(err)
			}
			second, err := svc.Acquire(ctx, "test-second")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = svc.Release(ctx, second, true, "") }()
			hash := idempotency.HashIdempotencyKey("global-system-operation-lock")
			current, err := repo.GetByScopeAndKeyHash(ctx, "admin.system.operations.global_lock", hash)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "reclaim-renew" {
				ok, err := operationStore.RenewOperation(ctx, current.ID, second.OperationID(), *current.ResponseBody, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					t.Errorf("新持有者不能续租；数据库指纹仍为 %s", current.RequestFingerprint)
				}
			} else {
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

// TestRestoreSQLFailure checks real psql exit statuses against isolated PostgreSQL.
func TestRestoreSQLFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("test_restore"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pg.Terminate(context.Background()) }()
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
	// A successful restore commits; canceling a later restore must retain that committed state.
	err = dumper.Restore(ctx, strings.NewReader("DELETE FROM fixture; INSERT INTO fixture VALUES(2);"))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture WHERE id=2").Scan(&count); err != nil || count != 1 {
		t.Fatalf("成功恢复未提交: count=%d error=%v", count, err)
	}
	cancelled, cancelRestore := context.WithTimeout(ctx, 80*time.Millisecond)
	err = dumper.Restore(cancelled, strings.NewReader("DELETE FROM fixture; SELECT pg_sleep(5); INSERT INTO fixture VALUES(3);"))
	cancelRestore()
	if err == nil {
		t.Fatal("取消恢复不能报告成功")
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture WHERE id=2").Scan(&count); err != nil || count != 1 {
		t.Fatalf("取消恢复未回滚: count=%d error=%v", count, err)
	}
	// A read failure cancels psql before closing stdin so incomplete input is not accepted as EOF.
	err = dumper.Restore(ctx, &brokenSQLReader{})
	if err == nil {
		t.Fatal("损坏输入不能报告成功")
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixture WHERE id=2").Scan(&count); err != nil || count != 1 {
		t.Fatalf("输入失败未回滚: count=%d error=%v", count, err)
	}

	// Exercise backup and restore with pg_dump, gzip, a local archive, and psql.
	path := t.TempDir()
	settings := &backupSettings{values: map[string]string{}}
	core := backup.New(settings, backup.Options{DatabaseName: "test_restore", LocalPath: path, Now: time.Now}, nil, nil, bp.NewLocalBackupStore(path), bp.NewArchive(dumper, 0), func(ctx context.Context) (func(), bool, error) {
		return postgresinfra.TryAcquireDBAdvisoryLockWithError(ctx, db, postgresinfra.HashAdvisoryLockID("maintenance:database-heavy"))
	})
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

func (r *brokenSQLReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	r.sent = true
	return copy(p, "DELETE FROM fixture; INSERT INTO fixture VALUES(4);"), nil
}

func (s *backupSettings) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *backupSettings) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

// maintenanceDatabase creates an isolated database with published migrations for data and maintenance locks.
func maintenanceDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pg, err := tcpostgres.Run(ctx, "postgres:18.1-alpine3.23", tcpostgres.WithDatabase("test_maintenance"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := pg.Terminate(cleanup); err != nil {
			t.Error(err)
		}
	})
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := postgresinfra.ApplyMigrations(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return db
}
