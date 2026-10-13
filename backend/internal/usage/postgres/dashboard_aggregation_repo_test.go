package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// A New York calendar day spans twenty-three or twenty-five elapsed hours
// during the selected DST transitions. Dashboard aggregation needs the injected
// timezone after it constructs a transaction-derived store. SQL argument
// expectations check local midnight bounds and the timezone name through all
// membership and aggregate writes.

// TestAggregationCalendarSurvivesTransaction covers the selected spring and
// autumn New York DST dates. Database transaction expectations retain local day
// bounds and timezone names, with elapsed day lengths checked as twenty-three
// and twenty-five hours.
func TestAggregationCalendarSurvivesTransaction(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	for _, tc := range []struct {
		name string // DST transition scenario name
		// month, day, and hours select the 2026 New York transition date and its
		// elapsed day length, which differs from a fixed twenty-four-hour duration.
		month time.Month // transition calendar month value
		day   int        // transition calendar day number
		hours int        // expected elapsed day hours
	}{
		{name: "spring", month: time.March, day: 8, hours: 23},
		{name: "autumn", month: time.November, day: 1, hours: 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newSQLMock(t)
			calendar := timezone.NewCalendar(loc)
			repo := NewAggregationStoreWithSQL(db, calendar)
			start := time.Date(2026, tc.month, tc.day, 0, 0, 0, 0, loc)
			// Advance by one local calendar date so the expected endpoint follows DST
			// rather than assuming every day spans twenty-four elapsed hours.
			end := start.AddDate(0, 0, 1)
			require.Equal(t, time.Duration(tc.hours)*time.Hour, end.Sub(start))

			mock.ExpectBegin()
			mock.ExpectExec("INSERT INTO usage_dashboard_hourly_users").
				WithArgs(start, end, "America/New_York").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec("INSERT INTO usage_dashboard_daily_users").
				WithArgs(start, end, "America/New_York").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec("WITH hourly AS").
				WithArgs(start, end, "America/New_York").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec("WITH daily AS").
				WithArgs(start, end, start, end, "America/New_York").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectCommit()

			require.NoError(t, repo.AggregateRange(context.Background(), start.UTC(), end.UTC()))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
