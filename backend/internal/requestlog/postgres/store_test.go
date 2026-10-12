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

// SQL-mock cases check permanent snapshot classification and the arguments
// shared by sequential detail queries. They isolate transaction rollback,
// unencodable timestamps, administrator versus ordinary lookup, and exact-local
// flags without executing PostgreSQL's lookup function or planner.

// TestSaveClassifiesInvalidRecords injects wrapped PostgreSQL SQLSTATEs through
// mocked upsert execution. Class-22 failures retain their cause and add
// ErrInvalidRecord; other classes remain retryable, with rollback expectations
// satisfied.
func TestSaveClassifiesInvalidRecords(t *testing.T) {
	for _, test := range []struct {
		// code selects the wrapped SQLSTATE and invalid selects whether Save must add
		// ErrInvalidRecord. The cases distinguish data exceptions from connection,
		// constraint, and serialization outcomes.
		code    string // injected PostgreSQL error code
		invalid bool   // expected permanent-content classification
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
			// Close the fixture database after checking expected SQL operations; the
			// deferred close result is suppressed and does not change asserted
			// transaction or lookup outcomes.
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

// TestSaveRejectsUnencodableRecord passes a timestamp beyond JSON's supported
// year range into a mocked transaction. Save reports ErrInvalidRecord and rolls
// back before an upsert execution is expected.
func TestSaveRejectsUnencodableRecord(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Close the mock pool after the scenario finishes, suppressing its deferred
	// error while explicit SQL expectations and returned error assertions
	// determine the result.
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectPrepare("INSERT INTO request_records")
	mock.ExpectRollback()
	err = NewStore(db).Save(t.Context(), []telemetry.RequestRecord{{RequestID: "id", StartedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}})
	require.ErrorIs(t, err, requestlog.ErrInvalidRecord)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestFindResolvesIDsOnce returns a fixed candidate array for
// administrator/user and exact/external cases. Every subsequent mocked source
// receives the same IDs and its expected flags, with audits queried for
// administrators.
func TestFindResolvesIDsOnce(t *testing.T) {
	for _, test := range []struct {
		// name labels the combination of admin and exact used by each lookup
		// expectation. Admin selects audit attachment, and exact disables
		// source-specific external-ID compatibility matching.
		name  string // lookup policy scenario label
		admin bool   // trusted administrator access flag
		exact bool   // resolved local-match lookup flag
	}{
		{"user external", false, false},
		{"admin external", true, false},
		{"user exact", false, true},
		{"admin exact", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			// Close the fixture database after checking expected SQL operations; the
			// deferred close result is suppressed and does not change asserted
			// transaction or lookup outcomes.
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

// TestFindStopsWhenLookupIsCanceled makes the initial candidate-resolution
// query return context cancellation. Find returns that cause and satisfies the
// mock with no subsequent source query expected.
func TestFindStopsWhenLookupIsCanceled(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Close the mock pool after the scenario finishes, suppressing its deferred
	// error while explicit SQL expectations and returned error assertions
	// determine the result.
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(`SELECT ARRAY`).WithArgs("id").WillReturnError(context.Canceled)
	_, err = NewStore(db).Find(t.Context(), "id", 42, false)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, mock.ExpectationsWereMet())
}
