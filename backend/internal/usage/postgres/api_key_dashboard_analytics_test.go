package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
)

// TestAPIKeyDashboardAnalyticsFallback checks full raw totals and daily statistics after state lookup fails.
func TestAPIKeyDashboardAnalyticsFallback(t *testing.T) {
	db, mock := newSQLMock(t)
	r := &Store{sql: db, calendar: timezone.NewCalendar(time.UTC), preAggregation: preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{Usage: preaggregation.UsageOptions{Enabled: true}})}
	mock.ExpectQuery("SELECT MIN\\(created_at\\)").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"oldest"}).AddRow(time.Now().Add(-48 * time.Hour)))
	mock.ExpectQuery("SELECT live_watermark, coverage_start").WillReturnError(errors.New("aggregation unavailable"))
	mock.ExpectQuery("(?s)SELECT COUNT\\(\\*\\).*FROM usage_logs WHERE api_key_id").WillReturnRows(sqlmock.NewRows([]string{"requests", "input", "output", "creation", "read", "cost", "actual", "average", "today_requests", "today_input", "today_output", "today_creation", "today_read", "today_cost", "today_actual"}).AddRow(3, 10, 20, 30, 40, 1, 2, 150, 1, 2, 3, 4, 5, 6, 7))
	mock.ExpectQuery("(?s)SELECT.*FROM usage_logs.*created_at >=").WillReturnRows(sqlmock.NewRows([]string{"requests", "tokens"}).AddRow(10, 100))
	got, err := r.GetAPIKeyDashboardStats(context.Background(), 1)
	require.NoError(t, err)
	require.EqualValues(t, 3, got.TotalRequests)
	require.EqualValues(t, 100, got.TotalTokens)
	require.EqualValues(t, 14, got.TodayTokens)
	require.EqualValues(t, 150, got.AverageDurationMs)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAPIKeyDashboardWithoutRecords ignores stale rollups after all raw records are gone.
func TestAPIKeyDashboardWithoutRecords(t *testing.T) {
	db, mock := newSQLMock(t)
	r := &Store{sql: db, calendar: timezone.NewCalendar(time.UTC), preAggregation: preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{Usage: preaggregation.UsageOptions{Enabled: true}})}
	mock.ExpectQuery("SELECT MIN\\(created_at\\)").WillReturnRows(sqlmock.NewRows([]string{"oldest"}).AddRow(nil))
	mock.ExpectQuery("(?s)SELECT.*FROM usage_logs.*created_at >=").WillReturnRows(sqlmock.NewRows([]string{"requests", "tokens"}).AddRow(0, 0))
	got, err := r.GetAPIKeyDashboardStats(context.Background(), 1)
	require.NoError(t, err)
	require.Zero(t, got.TotalRequests)
	require.Zero(t, got.TodayRequests)
	require.NoError(t, mock.ExpectationsWereMet())
}
