package postgres

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// TestOpsRepositoryInsertSystemMetricsPreservesZeroDBPoolCounts supplies zero
// active connections, ten idle, and an absent waiting observation. It checks
// those parameter slots retain zero and ten while waiting is SQL NULL.
func TestOpsRepositoryInsertSystemMetricsPreservesZeroDBPoolCounts(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{db: db}
	createdAt := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	active := 0
	idle := 10

	args := make([]driver.Value, 43)
	for i := range args {
		args[i] = sqlmock.AnyArg()
	}
	// Inspect zero-based slots thirty-eight through forty for active, idle, and
	// waiting: nil waiting stays distinct from a valid zero active count.
	args[38] = int64(0)
	args[39] = int64(10)
	args[40] = nil

	mock.ExpectExec("INSERT INTO ops_system_metrics").
		WithArgs(args...).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err := repo.InsertSystemMetrics(context.Background(), &ops.OpsInsertSystemMetricsInput{
		CreatedAt:     createdAt,
		WindowMinutes: 1,
		DBConnActive:  &active,
		DBConnIdle:    &idle,
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestInsertSystemMetricsNullableIntegerMetrics compares explicit zero and
// absent active-connection observations through INSERT expectations. It checks
// the native pointer encoder preserves zero and the default window is one for
// these fixtures.
func TestInsertSystemMetricsNullableIntegerMetrics(t *testing.T) {
	createdAt := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	zero := 0

	tests := []struct {
		name string // subtest scenario display label
		// dbConnActive selects a present zero or an unavailable observation. The SQL
		// expectation below uses this distinction after pointer encoding.
		dbConnActive *int         // supplied active connection observation
		wantDBActive driver.Value // expected active SQL argument
	}{
		{name: "explicit zero is preserved", dbConnActive: &zero, wantDBActive: int64(0)},
		{name: "unavailable metric remains null", dbConnActive: nil, wantDBActive: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			// Release each subtest's opened mock handle in cleanup, allowing fatal
			// argument assertions to end the case without skipping its handle close.
			t.Cleanup(func() { _ = db.Close() })

			args := make([]driver.Value, 43)
			args[0] = createdAt
			args[1] = int64(1)
			// Populate the contiguous integer count slots with valid zeros; other unset
			// optional slots remain nil in this exact INSERT fixture.
			for i := 4; i <= 12; i++ {
				args[i] = int64(0)
			}
			args[38] = tt.wantDBActive

			mock.ExpectExec("INSERT INTO ops_system_metrics").
				WithArgs(args...).
				WillReturnResult(sqlmock.NewResult(1, 1))

			repo := &Store{db: db}
			err = repo.InsertSystemMetrics(context.Background(), &ops.OpsInsertSystemMetricsInput{
				CreatedAt:    createdAt,
				DBConnActive: tt.dbConnActive,
			})
			if err != nil {
				t.Fatalf("InsertSystemMetrics: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}
