package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// Pending-event queries distinguish a missing row from a database failure,
// and cleanup combines a watermark with an age-limited batch. SQL mocks check
// those inputs and lease statements, while the nil-payload case checks the
// bytes that participate in persisted deduplication.

// TestSchedulerOutboxRepositoryFirstCreatedAtAfter supplies one timestamp
// above the watermark. It checks the query arguments, found result and
// returned time used by pending-event lag checks.
func TestSchedulerOutboxRepositoryFirstCreatedAtAfter(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Close this isolated mock database after its expectations are checked;
	// teardown errors do not replace the assertions made by the test.
	defer func() { _ = db.Close() }()

	repo := &schedulerOutboxRepository{db: db}
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	const expectedSQL = `
		SELECT created_at
		FROM scheduler_outbox
		WHERE id > $1
		ORDER BY id ASC
		LIMIT 1
	`
	mock.ExpectQuery(regexp.QuoteMeta(expectedSQL)).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(createdAt))

	got, ok, err := repo.FirstCreatedAtAfter(context.Background(), 42)

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, createdAt, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSchedulerOutboxRepositoryFirstCreatedAtAfterReturnsNotFound supplies an
// empty pending-event query. It checks found=false with a zero time and no
// error, so absence is a valid lag observation.
func TestSchedulerOutboxRepositoryFirstCreatedAtAfterReturnsNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Close this isolated mock database after its expectations are checked;
	// teardown errors do not replace the assertions made by the test.
	defer func() { _ = db.Close() }()

	repo := &schedulerOutboxRepository{db: db}
	const expectedSQL = `
		SELECT created_at
		FROM scheduler_outbox
		WHERE id > $1
		ORDER BY id ASC
		LIMIT 1
	`
	mock.ExpectQuery(regexp.QuoteMeta(expectedSQL)).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}))

	got, ok, err := repo.FirstCreatedAtAfter(context.Background(), 42)

	require.NoError(t, err)
	require.False(t, ok)
	require.True(t, got.IsZero())
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSchedulerOutboxRepositoryDeleteConsumedUpToUsesBoundedCTE expects the
// age-limited deletion statement with watermark and batch arguments. It
// checks that the repository returns the database's affected-row count.
func TestSchedulerOutboxRepositoryDeleteConsumedUpToUsesBoundedCTE(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Close this isolated mock database after its expectations are checked;
	// teardown errors do not replace the assertions made by the test.
	defer func() { _ = db.Close() }()

	repo := &schedulerOutboxRepository{db: db}
	const expectedSQL = `
		WITH doomed AS (
			SELECT id
			FROM scheduler_outbox
			WHERE id <= $1
				AND created_at < NOW() - INTERVAL '10 seconds'
			ORDER BY id ASC
			LIMIT $2
		)
		DELETE FROM scheduler_outbox o
		USING doomed d
		WHERE o.id = d.id
	`
	mock.ExpectExec(regexp.QuoteMeta(expectedSQL)).
		WithArgs(int64(42), 5000).
		WillReturnResult(sqlmock.NewResult(0, 17))

	deleted, err := repo.DeleteConsumedUpTo(context.Background(), 42, 5000)

	require.NoError(t, err)
	require.EqualValues(t, 17, deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSchedulerOutboxRepositoryDeleteConsumedUpToSkipsNonPositiveWatermark
// calls cleanup before any positive watermark exists. It checks zero
// deletions and expects no database statement for that input.
func TestSchedulerOutboxRepositoryDeleteConsumedUpToSkipsNonPositiveWatermark(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Close this isolated mock database after its expectations are checked;
	// teardown errors do not replace the assertions made by the test.
	defer func() { _ = db.Close() }()

	repo := &schedulerOutboxRepository{db: db}

	deleted, err := repo.DeleteConsumedUpTo(context.Background(), 0, 5000)

	require.NoError(t, err)
	require.EqualValues(t, 0, deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSchedulerOutboxRepositoryTryAcquireCleanupLock makes the advisory-lock
// query succeed. It checks that an acquired lease is returned and that
// releasing it issues the matching unlock statement.
func TestSchedulerOutboxRepositoryTryAcquireCleanupLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Close this isolated mock database after its expectations are checked;
	// teardown errors do not replace the assertions made by the test.
	defer func() { _ = db.Close() }()

	repo := &schedulerOutboxRepository{db: db}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock(hashtext('scheduler_outbox_cleanup'))")).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock(hashtext('scheduler_outbox_cleanup'))")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	lease, acquired, err := repo.TryAcquireCleanupLock(context.Background())
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, lease)

	lease.Release()

	require.NoError(t, mock.ExpectationsWereMet())
}

// TestSchedulerOutboxRepositoryTryAcquireCleanupLockUnavailable simulates
// advisory-lock contention. It checks that acquired=false and a nil lease
// return successfully after the database reports an existing lock holder.
func TestSchedulerOutboxRepositoryTryAcquireCleanupLockUnavailable(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	// Close this isolated mock database after its expectations are checked;
	// teardown errors do not replace the assertions made by the test.
	defer func() { _ = db.Close() }()

	repo := &schedulerOutboxRepository{db: db}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock(hashtext('scheduler_outbox_cleanup'))")).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(false))

	lease, acquired, err := repo.TryAcquireCleanupLock(context.Background())
	require.NoError(t, err)
	require.False(t, acquired)
	require.Nil(t, lease)

	require.NoError(t, mock.ExpectationsWereMet())
}

// TestEnqueueSchedulerOutbox_UngroupedProviderDedupesWithLiteralNilPayload
// compares GroupPayload's empty-group result with a literal nil payload. It
// checks equal deduplication hashes, protecting the interface-nil
// representation used by ungrouped provider events.
func TestEnqueueSchedulerOutbox_UngroupedProviderDedupesWithLiteralNilPayload(t *testing.T) {
	providerID := int64(42)

	// Status and error updates pass a literal nil payload.
	keyLiteralNil := schedulerOutboxDedupKey("provider_changed", &providerID, nil, nil)

	// An ungrouped provider produces the same nil payload through GroupPayload.
	emptyGroupsPayload := scheduler.GroupPayload(nil)
	require.Nil(t, emptyGroupsPayload,
		"scheduler.GroupPayload(empty) must return untyped-nil any to avoid typed-nil marshal")

	// Exercise the same interface-nil check as enqueueSchedulerOutbox; a typed
	// nil would pass that check, marshal as null and change the persisted hash.
	var payloadJSON []byte
	if emptyGroupsPayload != nil {
		t.Fatalf("typed-nil regression: scheduler.GroupPayload(empty) interface should be nil")
	}
	keyEmptyGroups := schedulerOutboxDedupKey("provider_changed", &providerID, nil, payloadJSON)

	require.Equal(t, keyLiteralNil, keyEmptyGroups,
		"ungrouped-provider provider_changed must share dedup_key with other nil-payload variants")
}
