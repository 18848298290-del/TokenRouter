package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// Live analytics rebuilding can replace a rounded UTC hour while scanning its
// raw records only through the watermark. Cleanup repair includes the end
// instant and rescans that complete final hour, including an hour beginning
// exactly at end. SQL expectations distinguish the replacement range from the
// raw scan range and the UTC dates rebuilt from those hours.
//
// Dashboard expectations separately track the Calendar's day range and the
// hourly range feeding daily membership. Exact-midnight repair expands both
// affected date and hour scopes, while a live range excludes those starting at
// its exact end. State expectations verify manual target/cursor decoding and
// their positions in full singleton replacement, independently of concurrent
// field-merge scenarios.

// expectUsageAnalyticsRangeRebuild registers a transaction replacing hourly and
// daily analytics rows. Callers pass UTC rounded hourStart/hourEnd and the
// intended raw scanEnd, which may stop inside the last replaced hour. The
// helper computes dates from those rounded bounds.
//
// The instant before exclusive hourEnd determines the final affected date, so
// an exact midnight does not add an untouched next date. Ordered expectations
// require hourly deletion and insertion before daily replacement and commit,
// and the insertion pattern checks the model normalization expression.
func expectUsageAnalyticsRangeRebuild(mock sqlmock.Sqlmock, hourStart, hourEnd, scanEnd time.Time) {
	dayStart := time.Date(hourStart.Year(), hourStart.Month(), hourStart.Day(), 0, 0, 0, 0, time.UTC)
	// Use an instant inside the last replaced hour to derive daily expectations,
	// so an exclusive midnight does not select the following date.
	lastHour := hourEnd.Add(-time.Nanosecond)
	dayEnd := time.Date(lastHour.Year(), lastHour.Month(), lastHour.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM usage_analytics_hourly").
		WithArgs(hourStart, hourEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	modelExpr := regexp.QuoteMeta("COALESCE(NULLIF(TRIM(ul.model), ''), NULLIF(TRIM(ul.requested_model), ''), '')")
	mock.ExpectExec("(?s)INSERT INTO usage_analytics_hourly.*"+modelExpr).
		WithArgs(hourStart, scanEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM usage_analytics_daily").
		WithArgs(dayStart, dayEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_analytics_daily").
		WithArgs(dayStart, dayEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

// TestAggregateUsageAnalyticsRangeStopsAtLiveWatermark ends a live scan halfway
// through its final UTC hour. Hourly replacement covers that hour, but the raw
// insertion expectation stops at the supplied end.
func TestAggregateUsageAnalyticsRangeStopsAtLiveWatermark(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	start := time.Date(2026, 8, 4, 9, 15, 0, 0, time.UTC)
	end := time.Date(2026, 8, 4, 10, 30, 0, 0, time.UTC)

	// Replacement selects whole UTC hours, while the last argument keeps the
	// partial watermark so the insertion expectation catches scanning too far.
	expectUsageAnalyticsRangeRebuild(mock, start.Truncate(time.Hour), end.Truncate(time.Hour).Add(time.Hour), end)

	require.NoError(t, repo.AggregateUsageAnalyticsRange(context.Background(), start, end))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAggregateUsageAnalyticsHourlyRangeSkipsDailyRebuild refreshes hours with
// a partial final watermark. The expected transaction deletes and inserts
// hourly rows, then commits without any daily replacement statement.
func TestAggregateUsageAnalyticsHourlyRangeSkipsDailyRebuild(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	start := time.Date(2026, 8, 4, 9, 15, 0, 0, time.UTC)
	end := time.Date(2026, 8, 4, 10, 30, 0, 0, time.UTC)
	// Replace both touched hours while leaving the raw insert bound at end,
	// distinguishing hourly replacement from completion of the partial hour.
	hourStart := start.Truncate(time.Hour)
	hourEnd := end.Truncate(time.Hour).Add(time.Hour)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM usage_analytics_hourly").
		WithArgs(hourStart, hourEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	modelExpr := regexp.QuoteMeta("COALESCE(NULLIF(TRIM(ul.model), ''), NULLIF(TRIM(ul.requested_model), ''), '')")
	mock.ExpectExec("(?s)INSERT INTO usage_analytics_hourly.*"+modelExpr).
		WithArgs(hourStart, end).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.AggregateUsageAnalyticsHourlyRange(context.Background(), start, end))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestRebuildUsageAnalyticsDailyRangeUsesUTCDateBounds crosses UTC midnight
// with partial days on both sides. Daily replacement receives midnight bounds
// covering both dates and completes in one transaction.
func TestRebuildUsageAnalyticsDailyRangeUsesUTCDateBounds(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	start := time.Date(2026, 8, 3, 23, 30, 0, 0, time.UTC)
	end := time.Date(2026, 8, 4, 0, 30, 0, 0, time.UTC)
	dayStart := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	dayEnd := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM usage_analytics_daily").
		WithArgs(dayStart, dayEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_analytics_daily").
		WithArgs(dayStart, dayEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.RebuildUsageAnalyticsDailyRange(context.Background(), start, end))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestRecomputeUsageAnalyticsRangeRebuildsPartialEndBucket places cleanup end
// midway through a UTC hour. SQL expectations require replacement and raw
// insertion to reach the following hour, protecting the full final-bucket scan
// range.
func TestRecomputeUsageAnalyticsRangeRebuildsPartialEndBucket(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	start := time.Date(2026, 8, 4, 9, 15, 0, 0, time.UTC)
	end := time.Date(2026, 8, 4, 10, 30, 0, 0, time.UTC)
	// Floor the first affected hour and scan the complete final hour, keeping
	// survivors on both sides of the narrower cleanup range.
	hourEnd := end.Truncate(time.Hour).Add(time.Hour)

	expectUsageAnalyticsRangeRebuild(mock, start.Truncate(time.Hour), hourEnd, hourEnd)

	require.NoError(t, repo.RecomputeUsageAnalyticsRange(context.Background(), start, end))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestRecomputeUsageAnalyticsRangeIncludesExactEndBucket places cleanup end
// exactly on a UTC hour. Repair replacement and insertion include the hour
// beginning there, as required for an inclusive endpoint.
func TestRecomputeUsageAnalyticsRangeIncludesExactEndBucket(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	start := time.Date(2026, 8, 4, 9, 15, 0, 0, time.UTC)
	end := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	// Expect the start's UTC hour floor through the complete hour beginning at
	// cleanup end, catching omission of that newly starting bucket.
	hourEnd := end.Add(time.Hour)

	expectUsageAnalyticsRangeRebuild(mock, start.Truncate(time.Hour), hourEnd, hourEnd)

	require.NoError(t, repo.RecomputeUsageAnalyticsRange(context.Background(), start, end))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestUsageAnalyticsAggregationStatePersistsManualCursor supplies a mocked
// progress row containing automatic and manual cursors. Manual values decode
// into pointers and return in the full state replacement's expected SQL
// argument positions.
func TestUsageAnalyticsAggregationStatePersistsManualCursor(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	live := time.Date(2026, 8, 4, 10, 30, 0, 0, time.UTC)
	coverage := live.Add(-48 * time.Hour)
	automaticCursor := live.Add(-72 * time.Hour)
	oldest := live.Add(-30 * 24 * time.Hour)
	manualStart := live.Add(-7 * 24 * time.Hour)
	manualCursor := live.Add(-3 * time.Hour)
	lastRun := live.Add(-time.Minute)
	lastSuccess := live.Add(-2 * time.Minute)

	mock.ExpectQuery("SELECT live_watermark, coverage_start, backfill_cursor, source_oldest_at").
		WillReturnRows(sqlmock.NewRows([]string{
			"live_watermark", "coverage_start", "backfill_cursor", "source_oldest_at",
			"manual_backfill_start", "manual_backfill_cursor", "phase", "last_run_at",
			"last_success_at", "last_error_at", "last_error", "last_duration_ms",
		}).AddRow(
			live, coverage, automaticCursor, oldest, manualStart, manualCursor,
			"backfill", lastRun, lastSuccess, nil, "", int64(25),
		))

	state, err := repo.GetUsageAnalyticsAggregationState(context.Background())
	require.NoError(t, err)
	require.Equal(t, manualStart, *state.ManualBackfillStart)
	require.Equal(t, manualCursor, *state.ManualBackfillCursor)

	mock.ExpectExec("INSERT INTO usage_analytics_aggregation_state").
		WithArgs(
			live, coverage, automaticCursor, oldest, manualStart, manualCursor,
			"backfill", lastRun, lastSuccess, nil, "", int64(25),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.SaveUsageAnalyticsAggregationState(context.Background(), state))
	require.NoError(t, mock.ExpectationsWereMet())
}

// expectDashboardAggregateStatements registers membership writes followed by
// numeric dashboard writes using time.Local's timezone name. Callers
// independently supply hourly, daily-membership and day-total ranges, then
// register transaction begin, preceding deletions if needed, and commit
// themselves.
func expectDashboardAggregateStatements(
	mock sqlmock.Sqlmock,
	hourStart, hourEnd time.Time,
	dailyUserStart, dailyUserEnd time.Time,
	dayStart, dayEnd time.Time,
) {
	tzName := time.Local.String()

	mock.ExpectExec("INSERT INTO usage_dashboard_hourly_users").
		WithArgs(hourStart, hourEnd, tzName).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_daily_users").
		WithArgs(dailyUserStart, dailyUserEnd, tzName).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_hourly \\(").
		WithArgs(hourStart, hourEnd, tzName).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_daily \\(").
		WithArgs(dayStart, dayEnd, dayStart, dayEnd, tzName).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// TestDashboardAggregateRangeKeepsExactEndExclusive ends live dashboard
// aggregation at calendar midnight. The time.Local fixture assumes that
// midnight also falls on an absolute hour boundary; membership and totals use
// it as the exclusive SQL endpoint.
func TestDashboardAggregateRangeKeepsExactEndExclusive(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	loc := time.Local

	start := time.Date(2026, 8, 4, 23, 15, 0, 0, loc)
	end := time.Date(2026, 8, 5, 0, 0, 0, 0, loc)
	// Distinct hour and Calendar date floors check both scopes while preserving
	// the exact end as their shared exclusive endpoint.
	hourStart := start.Truncate(time.Hour)
	dayStart := truncateToDay(start, timezone.NewCalendar(time.Local))

	mock.ExpectBegin()
	expectDashboardAggregateStatements(mock, hourStart, end, hourStart, end, dayStart, end)
	mock.ExpectCommit()

	require.NoError(t, repo.AggregateRange(context.Background(), start, end))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestDashboardRecomputeRangeIncludesExactEndBuckets ends cleanup repair at
// calendar midnight. The time.Local fixture assumes that midnight also falls
// on an absolute hour boundary. Delete and rebuild expectations include the
// starting hour and date; daily membership receives complete-day bounds.
func TestDashboardRecomputeRangeIncludesExactEndBuckets(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	loc := time.Local

	start := time.Date(2026, 8, 4, 23, 15, 0, 0, loc)
	end := time.Date(2026, 8, 5, 0, 0, 0, 0, loc)
	// Expand the expected hour and date ranges independently because repair
	// rebuilds whole-date membership beyond the narrower deleted hourly scope.
	hourStart := start.Truncate(time.Hour)
	hourEnd := end.Add(time.Hour)
	dayStart := truncateToDay(start, timezone.NewCalendar(time.Local))
	dayEnd := end.AddDate(0, 0, 1)

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM usage_dashboard_hourly WHERE").
		WithArgs(hourStart, hourEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM usage_dashboard_hourly_users WHERE").
		WithArgs(hourStart, hourEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM usage_dashboard_daily WHERE").
		WithArgs(dayStart, dayEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM usage_dashboard_daily_users WHERE").
		WithArgs(dayStart, dayEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectDashboardAggregateStatements(mock, hourStart, hourEnd, dayStart, dayEnd, dayStart, dayEnd)
	mock.ExpectCommit()

	require.NoError(t, repo.RecomputeRange(context.Background(), start, end))
	require.NoError(t, mock.ExpectationsWereMet())
}
