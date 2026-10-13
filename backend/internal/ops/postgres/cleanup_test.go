package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// fakeErr supplies custom error text for the missing-relation classifier's
// wording fixtures.
type fakeErr string

// TestIsMissingRelationError covers nil, canonical missing-relation wording,
// changed case, and an unrelated connection failure. It checks the helper's
// substring classification for each supplied fixture.
func TestIsMissingRelationError(t *testing.T) {
	cases := []struct {
		name string // subtest case display label
		err  error  // supplied classifier error value
		want bool   // expected relation classification result
	}{
		{name: "nil is not missing", err: nil, want: false},
		{name: "match relation does not exist", err: fakeErr(`pq: relation "ops_error_logs" does not exist`), want: true},
		{name: "match case-insensitive", err: fakeErr(`ERROR: Relation "x" Does Not Exist`), want: true},
		{name: "non-matching error", err: fakeErr("connection refused"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingRelationError(tc.err); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDeleteOldRowsByCTIDBatchesAndThrottles supplies a full two-row SQL batch
// followed by a short one-row batch. It checks three deletions, two batches,
// and positive measured throttle time with the expected ordered query shape.
func TestDeleteOldRowsByCTIDBatchesAndThrottles(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	// Close this SQL mock when the test function exits, including fatal assertion
	// paths, while keeping its expectations focused on cleanup statements.
	defer func() { _ = db.Close() }()

	cutoff := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	query := `(?s)WITH batch AS MATERIALIZED.*ORDER BY created_at ASC, id ASC.*DELETE FROM ops_system_logs AS target`
	mock.ExpectExec(query).WithArgs(cutoff, 2).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(query).WithArgs(cutoff, 2).WillReturnResult(sqlmock.NewResult(0, 1))

	// Use a two-row limit and one-millisecond pause so the first full fixture
	// batch throttles once before the short batch ends iteration.
	result, err := deleteOldRowsByCTID(context.Background(), db, "ops_system_logs", "created_at", cutoff, 2, time.Millisecond, false)
	if err != nil {
		t.Fatalf("delete rows: %v", err)
	}
	if result.Deleted != 3 || result.Batches != 2 {
		t.Fatalf("result = %#v, want deleted=3 batches=2", result)
	}
	if result.Throttled <= 0 {
		t.Fatalf("throttled = %v, want positive duration", result.Throttled)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// TestTruncateOpsTableUsesStatisticsInsteadOfFullCount supplies a pg_class
// estimate followed by a successful TRUNCATE expectation. It checks the
// returned accounting value uses that estimate rather than an exact row-count
// query.
func TestTruncateOpsTableUsesStatisticsInsteadOfFullCount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	// Close this SQL mock when the test function exits, including fatal assertion
	// paths, while keeping its expectations focused on cleanup statements.
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`(?s)SELECT COALESCE.*FROM pg_class.*to_regclass`).
		WithArgs("ops_error_logs").
		WillReturnRows(sqlmock.NewRows([]string{"estimated_rows"}).AddRow(int64(4200)))
	mock.ExpectExec(`TRUNCATE TABLE ops_error_logs`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	estimatedRows, err := truncateOpsTable(context.Background(), db, "ops_error_logs")
	if err != nil {
		t.Fatalf("truncate table: %v", err)
	}
	if estimatedRows != 4200 {
		t.Fatalf("estimated rows = %d, want 4200", estimatedRows)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// TestSleepOpsCleanupWithContextHonorsCancellation cancels the parent before
// requesting a one-second throttle delay. It checks the helper returns the
// cancellation error rather than successful timer completion.
func TestSleepOpsCleanupWithContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepOpsCleanupWithContext(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

// Error returns the fixture string as its custom error description.
func (e fakeErr) Error() string { return string(e) }
