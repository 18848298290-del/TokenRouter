package postgres

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// Maintenance lock tests model admission and release through SQL expectations
// on a reserved session. Busy locks return no cleanup function; an acquired
// lock produces an unlock statement carrying the same hashed maintenance
// identity before the fixture database is closed.

// TestDatabaseHeavyMaintenanceLockRejectsConcurrentTask supplies a busy
// maintenance advisory lock through SQLMock. It requires false admission and
// no release function, then checks the expected lock query was used.
func TestDatabaseHeavyMaintenanceLockRejectsConcurrentTask(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT pg_try_advisory_lock").
		WithArgs(HashAdvisoryLockID("maintenance:database-heavy")).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(false))

	release, acquired, err := TryAcquireDBAdvisoryLockWithError(context.Background(), db, HashAdvisoryLockID("maintenance:database-heavy"))
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if acquired || release != nil {
		t.Fatalf("acquired = %v release = %v, want busy", acquired, release != nil)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// TestDatabaseHeavyMaintenanceLockReleasesSession models successful
// maintenance lock admission followed by release. It requires a release
// function and checks the unlock statement uses the same hashed identifier as
// acquisition.
func TestDatabaseHeavyMaintenanceLockReleasesSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT pg_try_advisory_lock").
		WithArgs(HashAdvisoryLockID("maintenance:database-heavy")).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true))
	mock.ExpectExec("SELECT pg_advisory_unlock").
		WithArgs(HashAdvisoryLockID("maintenance:database-heavy")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	release, acquired, err := TryAcquireDBAdvisoryLockWithError(context.Background(), db, HashAdvisoryLockID("maintenance:database-heavy"))
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if !acquired || release == nil {
		t.Fatalf("acquired = %v release = %v, want acquired", acquired, release != nil)
	}
	release()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
