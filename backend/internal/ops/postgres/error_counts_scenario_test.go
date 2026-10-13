package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// TestErrorCountsReadFailure injects the same SQL failure into collector and
// dashboard category readers. It checks error identity is retained and every
// returned category count remains zero for that failed query.
func TestErrorCountsReadFailure(t *testing.T) {
	for _, source := range []string{"collector", "dashboard"} {
		t.Run(source, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			// Close each subtest's SQL fixture in cleanup so query-failure assertions
			// can end early while the opened handle is still released.
			t.Cleanup(func() { _ = db.Close() })
			wantErr := errors.New("query failed")
			start := time.Date(2038, 3, 10, 12, 0, 0, 0, time.UTC)
			end := start.Add(time.Hour)
			mock.ExpectQuery("FROM ops_error_logs").WithArgs(start, end).WillReturnError(wantErr)
			var a, b, c, d, e, f int64
			if source == "collector" {
				a, b, c, d, e, f, err = (&MetricsQueries{db: db}).QueryErrorCounts(context.Background(), start, end, nil)
			} else {
				a, b, c, d, e, f, err = NewOpsRepository(db).queryErrorCounts(context.Background(), &ops.OpsDashboardFilter{}, start, end)
			}
			require.ErrorIs(t, err, wantErr)
			require.Equal(t, [6]int64{}, [6]int64{a, b, c, d, e, f})
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
