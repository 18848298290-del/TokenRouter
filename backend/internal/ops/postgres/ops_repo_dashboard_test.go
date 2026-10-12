package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

func TestIsQueryTimeoutErr(t *testing.T) {
	if !isQueryTimeoutErr(context.DeadlineExceeded) {
		t.Fatalf("context.DeadlineExceeded should be treated as query timeout")
	}
	if !isQueryTimeoutErr(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)) {
		t.Fatalf("wrapped context.DeadlineExceeded should be treated as query timeout")
	}
	if isQueryTimeoutErr(context.Canceled) {
		t.Fatalf("context.Canceled should not be treated as query timeout")
	}
	if isQueryTimeoutErr(fmt.Errorf("wrapped: %w", context.Canceled)) {
		t.Fatalf("wrapped context.Canceled should not be treated as query timeout")
	}
}

func TestAggregateHourlyRowsWeightsTTFTBySampleCount(t *testing.T) {
	rows := []opsHourlyMetricsRow{
		{
			bucketStart:     time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			successCount:    100,
			ttftSampleCount: 1,
			durationP50:     sql.NullInt64{Int64: 100, Valid: true},
			durationP90:     sql.NullInt64{Int64: 200, Valid: true},
			durationAvg:     sql.NullFloat64{Float64: 150, Valid: true},
			durationP95:     sql.NullInt64{Int64: 220, Valid: true},
			durationP99:     sql.NullInt64{Int64: 250, Valid: true},
			durationMax:     sql.NullInt64{Int64: 300, Valid: true},
			ttftP50:         sql.NullInt64{Int64: 1000, Valid: true},
			ttftP90:         sql.NullInt64{Int64: 1200, Valid: true},
			ttftAvg:         sql.NullFloat64{Float64: 1100, Valid: true},
			ttftP95:         sql.NullInt64{Int64: 1300, Valid: true},
			ttftP99:         sql.NullInt64{Int64: 1400, Valid: true},
			ttftMax:         sql.NullInt64{Int64: 1500, Valid: true},
		},
		{
			bucketStart:     time.Date(2026, 6, 1, 1, 0, 0, 0, time.UTC),
			successCount:    1,
			ttftSampleCount: 99,
			durationP50:     sql.NullInt64{Int64: 1000, Valid: true},
			durationP90:     sql.NullInt64{Int64: 2000, Valid: true},
			durationAvg:     sql.NullFloat64{Float64: 1500, Valid: true},
			durationP95:     sql.NullInt64{Int64: 2200, Valid: true},
			durationP99:     sql.NullInt64{Int64: 2500, Valid: true},
			durationMax:     sql.NullInt64{Int64: 3000, Valid: true},
			ttftP50:         sql.NullInt64{Int64: 10, Valid: true},
			ttftP90:         sql.NullInt64{Int64: 20, Valid: true},
			ttftAvg:         sql.NullFloat64{Float64: 15, Valid: true},
			ttftP95:         sql.NullInt64{Int64: 25, Valid: true},
			ttftP99:         sql.NullInt64{Int64: 30, Valid: true},
			ttftMax:         sql.NullInt64{Int64: 35, Valid: true},
		},
	}

	got := aggregateHourlyRows(rows)

	require.Equal(t, int64(101), got.successCount)
	require.Equal(t, int64(100), got.ttftSampleCount)

	// Request duration is weighted by success_count.
	require.NotNil(t, got.duration.P50)
	require.Equal(t, 109, *got.duration.P50)

	// Weight first-token latency by ttft_sample_count so non-streaming successes do not dilute it.
	require.NotNil(t, got.ttft.P50)
	require.Equal(t, 20, *got.ttft.P50)
	require.NotNil(t, got.ttft.P90)
	require.Equal(t, 32, *got.ttft.P90)
	require.NotNil(t, got.ttft.Avg)
	require.Equal(t, 26, *got.ttft.Avg)
	require.NotNil(t, got.ttft.P95)
	require.Equal(t, 1300, *got.ttft.P95)
}

func TestCombineApproxPercentilesUsesProvidedWeightsForTTFT(t *testing.T) {
	got := combineApproxPercentiles([]opsPercentileSegment{
		{
			weight: 1,
			p: ops.OpsPercentiles{
				P50: intValuePtr(1000),
				P90: intValuePtr(1200),
				Avg: intValuePtr(1100),
				P95: intValuePtr(1300),
			},
		},
		{
			weight: 99,
			p: ops.OpsPercentiles{
				P50: intValuePtr(10),
				P90: intValuePtr(20),
				Avg: intValuePtr(15),
				P95: intValuePtr(25),
			},
		},
	})

	require.NotNil(t, got.P50)
	require.Equal(t, 20, *got.P50)
	require.NotNil(t, got.P90)
	require.Equal(t, 32, *got.P90)
	require.NotNil(t, got.Avg)
	require.Equal(t, 26, *got.Avg)
	require.NotNil(t, got.P95)
	require.Equal(t, 1300, *got.P95)
}

func intValuePtr(v int) *int {
	return &v
}

// TestOpsHourlyMetricsCoverageRequiresEveryBucket rejects coverage with missing buckets.
func TestOpsHourlyMetricsCoverageRequiresEveryBucket(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &Store{db: db}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	query := regexp.QuoteMeta(`
		SELECT COUNT(*)
		FROM ops_metrics_hourly
		WHERE bucket_start >= $1 AND bucket_start < $2
		  AND platform IS NULL AND group_id IS NULL
	`)

	mock.ExpectQuery(query).WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(23)))
	covered, err := repo.hasCompleteHourlyMetricsCoverage(context.Background(), start, end)
	require.NoError(t, err)
	require.False(t, covered)

	mock.ExpectQuery(query).WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(24)))
	covered, err = repo.hasCompleteHourlyMetricsCoverage(context.Background(), start, end)
	require.NoError(t, err)
	require.True(t, covered)
	require.NoError(t, mock.ExpectationsWereMet())
}
