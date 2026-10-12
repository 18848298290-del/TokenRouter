package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
	"github.com/TokenFlux/TokenRouter/internal/team"
)

// TestTeamReportAggregationFailureUsesRaw checks a full raw-range query after aggregation state fails.
func TestTeamReportAggregationFailureUsesRaw(t *testing.T) {
	db, mock := newSQLMock(t)
	r := &Store{sql: db, preAggregation: preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{Usage: preaggregation.UsageOptions{Enabled: true}})}
	mock.ExpectQuery("SELECT MIN\\(ul.created_at\\)").WillReturnRows(sqlmock.NewRows([]string{"oldest"}).AddRow(time.Now().Add(-30 * time.Minute)))
	mock.ExpectQuery("SELECT live_watermark, coverage_start").WillReturnError(errors.New("state unavailable"))
	mock.ExpectQuery("(?s)WITH scoped.*FROM usage_logs.*GROUP BY GROUPING SETS").WillReturnRows(sqlmock.NewRows([]string{"day", "cost", "requests", "input", "output"}).AddRow(nil, 12, 3, 4, 5))
	result, err := r.GetTeamUsageSummary(context.Background(), 7, team.TeamUsageQuery{From: time.Now().Add(-time.Hour), To: time.Now()})
	require.NoError(t, err)
	require.EqualValues(t, 12, result.ActualCost)
	require.EqualValues(t, 3, result.RequestCount)
	require.NoError(t, mock.ExpectationsWereMet())
}
