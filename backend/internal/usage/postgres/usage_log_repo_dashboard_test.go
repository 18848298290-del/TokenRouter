package postgres

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// TestRunDashboardQueriesSerializesNonPoolExecutor checks that single-connection executors are used sequentially.
func TestRunDashboardQueriesSerializesNonPoolExecutor(t *testing.T) {
	repo := &Store{}
	var running atomic.Int32
	var overlapped atomic.Bool
	query := func(context.Context) error {
		if running.Add(1) != 1 {
			overlapped.Store(true)
		}
		time.Sleep(time.Millisecond)
		running.Add(-1)
		return nil
	}

	require.NoError(t, repo.runDashboardQueries(context.Background(), query, query, query))
	require.False(t, overlapped.Load())
}

// TestRunDashboardQueriesParallelizesPoolExecutor checks concurrent execution of independent pool queries.
func TestRunDashboardQueriesParallelizesPoolExecutor(t *testing.T) {
	repo := &Store{db: new(sql.DB)}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 1)
	query := func(context.Context) error {
		started <- struct{}{}
		<-release
		return nil
	}

	go func() {
		done <- repo.runDashboardQueries(context.Background(), query, query)
	}()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for range 2 {
		select {
		case <-started:
		case <-timer.C:
			close(release)
			<-done
			t.Fatal("连接池查询未并行启动")
		}
	}
	close(release)
	require.NoError(t, <-done)
}

// TestGetUserDashboardStatsUsesIndexableOwnedTeamScope checks team resolution followed by separate indexed scans.
func TestGetUserDashboardStatsUsesIndexableOwnedTeamScope(t *testing.T) {
	db, mock := newSQLMock(t)
	// Allow independent key, usage, and rate queries to execute in any order after team resolution.
	mock.MatchExpectationsInOrder(false)
	repo := &Store{sql: db}

	mock.ExpectQuery("(?s)SELECT \\(.*FROM team_memberships.*tm.user_id = \\$1.*tm.role = 'owner'.*tm.left_at IS NULL").
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(42)))
	mock.ExpectQuery("(?s)WITH scoped AS.*FROM api_keys.*user_id = \\$1.*UNION ALL.*team_id = \\$2 AND user_id <> \\$1.*COUNT\\(\\*\\) FILTER").
		WithArgs(int64(7), int64(42), billing.StatusActive).
		WillReturnRows(sqlmock.NewRows([]string{"total", "active"}).AddRow(int64(3), int64(2)))
	mock.ExpectQuery("(?s)WITH scoped AS.*FROM usage_logs WHERE user_id = \\$1.*UNION ALL.*team_id = \\$2 AND user_id <> \\$1.*COUNT\\(\\*\\) FILTER").
		WithArgs(int64(7), int64(42), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens", "total_cache_creation_tokens",
			"total_cache_read_tokens", "total_cost", "total_actual_cost", "total_duration_ms", "duration_count",
			"today_requests", "today_input_tokens", "today_output_tokens", "today_cache_creation_tokens",
			"today_cache_read_tokens", "today_cost", "today_actual_cost",
		}).AddRow(
			int64(3), int64(10), int64(20), int64(2), int64(4), 0.4, 0.3, int64(300), int64(3),
			int64(1), int64(5), int64(6), int64(1), int64(2), 0.2, 0.15,
		))
	mock.ExpectQuery("(?s)WITH scoped AS.*FROM usage_logs.*user_id = \\$1 AND created_at >= \\$3.*UNION ALL.*team_id = \\$2 AND user_id <> \\$1").
		WithArgs(int64(7), int64(42), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"request_count", "token_count"}).AddRow(int64(10), int64(100)))

	stats, err := repo.GetUserDashboardStats(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(3), stats.TotalAPIKeys)
	require.Equal(t, int64(3), stats.TotalRequests)
	require.Equal(t, int64(36), stats.TotalTokens)
	require.Equal(t, int64(2), stats.Rpm)
	require.Equal(t, int64(20), stats.Tpm)
	require.NoError(t, mock.ExpectationsWereMet())
}
