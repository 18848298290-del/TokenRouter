package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/requestlog"
)

// TestSaveClassifiesInvalidRecords distinguishes discardable data errors from retryable connection and constraint failures.
func TestSaveClassifiesInvalidRecords(t *testing.T) {
	for _, test := range []struct {
		code    string
		invalid bool
	}{
		{"22P05", true},
		{"22001", true},
		{"08006", false},
		{"23514", false},
		{"40001", false},
	} {
		t.Run(test.code, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			failure := fmt.Errorf("write: %w", &pq.Error{Code: pq.ErrorCode(test.code)})
			mock.ExpectBegin()
			mock.ExpectPrepare("INSERT INTO request_records").ExpectExec().WillReturnError(failure)
			mock.ExpectRollback()
			err = NewStore(db).Save(t.Context(), []telemetry.RequestRecord{{RequestID: "id"}})
			require.ErrorIs(t, err, failure)
			if test.invalid {
				require.ErrorIs(t, err, requestlog.ErrInvalidRecord)
			} else {
				require.NotErrorIs(t, err, requestlog.ErrInvalidRecord)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestSaveRejectsUnencodableRecord marks timestamps outside JSON's range as invalid snapshots.
func TestSaveRejectsUnencodableRecord(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectPrepare("INSERT INTO request_records")
	mock.ExpectRollback()
	err = NewStore(db).Save(t.Context(), []telemetry.RequestRecord{{RequestID: "id", StartedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}})
	require.ErrorIs(t, err, requestlog.ErrInvalidRecord)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestFindResolvesIDsOnce checks shared candidate IDs and source-specific authorization parameters.
func TestFindResolvesIDsOnce(t *testing.T) {
	for _, test := range []struct {
		name  string
		admin bool
		exact bool
	}{
		{"user external", false, false},
		{"admin external", true, false},
		{"user exact", false, true},
		{"admin exact", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			ids := pq.Array([]string{"local-request", "client:legacy"})
			mock.ExpectQuery(`SELECT ARRAY\(SELECT id FROM request_lookup_ids\(\$1\)\)`).
				WithArgs("external-id").WillReturnRows(sqlmock.NewRows([]string{"ids", "exact"}).AddRow(`{"local-request","client:legacy"}`, test.exact))
			mock.ExpectQuery(`SELECT record FROM request_records`).WithArgs(ids, int64(42), test.admin).
				WillReturnRows(sqlmock.NewRows([]string{"record"}))
			mock.ExpectQuery(`SELECT .* FROM usage_logs`).WithArgs(ids, int64(42), test.admin, "external-id", test.exact).
				WillReturnRows(sqlmock.NewRows([]string{"id"}))
			mock.ExpectQuery(`SELECT .* FROM ops_error_logs`).WithArgs(ids, int64(42), test.admin, test.exact).
				WillReturnRows(sqlmock.NewRows([]string{"id"}))
			if test.admin {
				mock.ExpectQuery(`SELECT .* FROM audit_logs`).WithArgs(ids).
					WillReturnRows(sqlmock.NewRows([]string{"id"}))
			}
			items, err := NewStore(db).Find(t.Context(), "external-id", 42, test.admin)
			require.NoError(t, err)
			require.Empty(t, items)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestFindStopsWhenLookupIsCanceled checks that canceled ID resolution prevents further source queries.
func TestFindStopsWhenLookupIsCanceled(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(`SELECT ARRAY`).WithArgs("id").WillReturnError(context.Canceled)
	_, err = NewStore(db).Find(t.Context(), "id", 42, false)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, mock.ExpectationsWereMet())
}
