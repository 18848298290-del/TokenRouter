package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	billingpg "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// SQL-mock repository tests choose regular-expression matching so query
// fragments and bound values can be asserted separately. The dashboard
// aggregation helper supplies the same executor to both rollup reads and
// billing-dedup archival, making transaction choice visible in each fixture.

// newDashboardAggregationRepositoryWithSQL constructs a fixture aggregation
// store for q and local Calendar rules. Its archival callback uses that same
// executor, leaving any transaction commit or rollback to the calling test.
func newDashboardAggregationRepositoryWithSQL(q sqlExecutor) *AggregationStore {
	return NewAggregationStoreWithSQL(q, timezone.NewCalendar(time.Local), func(ctx context.Context, t time.Time) error { return billingpg.ArchiveUsageDedup(ctx, q, t) })
}

// newSQLMock returns a regex-matching database mock and registers database-close
// cleanup. Creation failures stop the test; cleanup ignores close errors, while
// each test remains responsible for checking its expected queries.
func newSQLMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, m, e := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, e)
	// Register cleanup before returning the mock so an assertion failure cannot
	// retain its database handle; cleanup ignores any later Close error.
	t.Cleanup(func() { _ = db.Close() })
	return db, m
}
