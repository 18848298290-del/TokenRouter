package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestAuthCacheInvalidationOutboxRepository_ClaimUsesLeaseAndSkipLocked
// supplies one returned leased record through SQL expectations. It checks the
// elapsed-claim predicate, SKIP LOCKED statement shape, supplied parameters and
// decoded delivery-stage fields.
func TestAuthCacheInvalidationOutboxRepository_ClaimUsesLeaseAndSkipLocked(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Release the test-owned SQL handle after its claim or transition
	// assertions so the mock connection does not outlive the adapter scenario.
	defer func() { _ = db.Close() }()

	created := time.Now().UTC()
	mock.ExpectQuery("(?s)claimed_at < NOW\\(\\) - .*FOR UPDATE SKIP LOCKED.*RETURNING").
		WithArgs("worker-a", 100, int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "cache_key", "attempts", "delivery_stage", "created_at"}).
			AddRow(int64(4), strings.Repeat("a", 64), 2, 1, created))

	repo := NewAuthCacheInvalidationOutboxRepository(db)
	events, err := repo.Claim(context.Background(), "worker-a", 100, 30*time.Second)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, int64(4), events[0].ID)
	require.Equal(t, 1, events[0].Stage)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAuthCacheInvalidationOutboxRepository_ClaimIsBoundedByDefault passes
// omitted batch and lease values to the SQL adapter. It checks defaults become
// a bounded claim limit and thirty-second lease in the executed statement.
func TestAuthCacheInvalidationOutboxRepository_ClaimIsBoundedByDefault(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Release the test-owned SQL handle after its claim or transition
	// assertions so the mock connection does not outlive the adapter scenario.
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("(?s)FROM auth_cache_invalidation_outbox.*LIMIT \\$2.*SKIP LOCKED").
		WithArgs("worker", 100, int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "cache_key", "attempts", "delivery_stage", "created_at"}))
	repo := NewAuthCacheInvalidationOutboxRepository(db)
	_, err = repo.Claim(context.Background(), "worker", 0, 0)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAuthCacheInvalidationOutboxRepository_ClaimOwnershipTransitions accepts
// one affected row for safety scheduling, retry and deletion. It checks row and
// worker identities plus retry timestamps and text are sent to each
// owner-dependent SQL operation.
func TestAuthCacheInvalidationOutboxRepository_ClaimOwnershipTransitions(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Release the test-owned SQL handle after its claim or transition
	// assertions so the mock connection does not outlive the adapter scenario.
	defer func() { _ = db.Close() }()
	repo := NewAuthCacheInvalidationOutboxRepository(db)

	next := time.Now().UTC().Add(time.Minute)
	mock.ExpectExec("UPDATE auth_cache_invalidation_outbox").
		WithArgs(int64(1), "worker", next).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.ScheduleSecondPass(context.Background(), 1, "worker", next))

	retryAt := next.Add(time.Minute)
	mock.ExpectExec("UPDATE auth_cache_invalidation_outbox").
		WithArgs(int64(2), "worker", retryAt, "publish failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.RetryClaimed(context.Background(), 2, "worker", retryAt, "publish failed"))

	mock.ExpectExec("DELETE FROM auth_cache_invalidation_outbox").
		WithArgs(int64(3), "worker").
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.DeleteClaimed(context.Background(), 3, "worker"))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAuthCacheInvalidationOutboxRepository_RejectsLostClaim returns zero
// affected rows for an old worker's acknowledgement attempt. It checks the
// adapter reports lost claim ownership instead of accepting the absent
// deletion.
func TestAuthCacheInvalidationOutboxRepository_RejectsLostClaim(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Release the test-owned SQL handle after its claim or transition
	// assertions so the mock connection does not outlive the adapter scenario.
	defer func() { _ = db.Close() }()
	mock.ExpectExec("DELETE FROM auth_cache_invalidation_outbox").
		WithArgs(int64(3), "old-worker").
		WillReturnResult(sqlmock.NewResult(0, 0))
	repo := NewAuthCacheInvalidationOutboxRepository(db)
	err = repo.DeleteClaimed(context.Background(), 3, "old-worker")
	require.ErrorContains(t, err, "no longer owned")
}

// TestAuthCacheInvalidationOutboxRepository_StatsExposeDurableLagAndFailures
// supplies retained count, creation time, retries and stored error through SQL
// rows. It checks these observations are decoded for worker health reporting.
func TestAuthCacheInvalidationOutboxRepository_StatsExposeDurableLagAndFailures(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Release the test-owned SQL handle after its claim or transition
	// assertions so the mock connection does not outlive the adapter scenario.
	defer func() { _ = db.Close() }()
	oldest := time.Now().UTC().Add(-time.Minute)
	mock.ExpectQuery("(?s)SELECT COUNT\\(\\*\\), MIN\\(created_at\\), COALESCE\\(MAX\\(attempts\\), 0\\)").
		WillReturnRows(sqlmock.NewRows([]string{"count", "min", "max", "last_error"}).AddRow(5, oldest, 7, "redis down"))
	repo := NewAuthCacheInvalidationOutboxRepository(db)
	stats, err := repo.Stats(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(5), stats.Pending)
	require.Equal(t, 7, stats.MaxAttempts)
	require.Equal(t, "redis down", stats.LastError)
	require.NotNil(t, stats.OldestCreatedAt)
}
