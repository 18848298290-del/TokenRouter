package postgres

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// Store is checked against the complete OpsRepository interface so timing-query
// fixtures retain the repository methods used by the service.
var _ ops.OpsRepository = (*Store)(nil)

// TestOpsRepositoryListRequestTimings returns one access-log timing object and
// one row lacking recognized fields from a SQL fixture. It checks only the
// recognized row remains and verifies the latest-row query shape.
func TestOpsRepositoryListRequestTimings(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{db: db}

	mock.ExpectQuery(`(?s)SELECT DISTINCT ON \(COALESCE\(NULLIF\(l\.client_request_id,''\), l\.request_id\)\).*l\.request_id = ANY\(\$1\).*ORDER BY COALESCE\(NULLIF\(l\.client_request_id,''\), l\.request_id\), l\.created_at DESC, l\.id DESC`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"client_request_id", "extra"}).
			AddRow("req-internal-1", `{"upstream_first_response_byte_ms":120000,"upstream_attempt_count":2}`).
			AddRow("req-no-timing", `{}`))

	got, err := repo.ListRequestTimings(context.Background(), []string{"req-internal-1", "req-internal-1", "req-no-timing"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got["req-internal-1"])
	require.Equal(t, int64(120000), *got["req-internal-1"].UpstreamFirstResponseByteMs)
	require.Equal(t, int64(2), *got["req-internal-1"].UpstreamAttemptCount)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestOpsRepositoryListRequestTimingsEmptyInput supplies empty and
// whitespace-only identifiers to a configured repository. It checks an empty
// successful map is returned without any SQL query expectation.
func TestOpsRepositoryListRequestTimingsEmptyInput(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{db: db}

	got, err := repo.ListRequestTimings(context.Background(), []string{"", "  "})
	require.NoError(t, err)
	require.Empty(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}
