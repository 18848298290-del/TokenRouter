package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	keycore "github.com/TokenFlux/TokenRouter/internal/apikey"
)

// invalidation_outbox.go claims durable cache-invalidation records and
// persists their retry and delivery progress. Claim uses one PostgreSQL update
// with SKIP LOCKED candidates and an elapsed-lease predicate so workers can
// distribute available records while retaining recovery for abandoned claims.
// Candidate order favors earlier IDs, but the returned update rows carry no
// final ordering guarantee.
//
// Every completion transition checks its row ID and claim owner before
// changing or deleting it. A second-pass transition also requires the initial
// stage, while retry increments its durable attempt count and releases the
// claim. Health queries include all retained records, including delayed and
// claimed events, and derive their oldest creation and most recently scheduled
// stored error observations.

// AuthCacheInvalidationOutboxRepository persists leased delivery progress using
// a shared PostgreSQL handle.
type AuthCacheInvalidationOutboxRepository struct {
	// db is shared with assembly and retained by this repository. Its caller
	// closes the handle after claim and transition users have stopped.
	db *sql.DB
}

// NewAuthCacheInvalidationOutboxRepository retains db as the invalidation claim
// storage dependency. Its caller owns closing the shared database after workers
// stop; Claim checks a missing handle, while other methods require a usable
// handle.
func NewAuthCacheInvalidationOutboxRepository(db *sql.DB) keycore.AuthCacheInvalidationOutboxRepository {
	return &AuthCacheInvalidationOutboxRepository{db: db}
}

// Claim leases available records to workerID using one owner-updating
// PostgreSQL statement. Nonpositive limit selects 100, and a lease below one
// whole second selects thirty seconds.
//
// Candidate rows exclude unexpired claims, favor earlier IDs and use SKIP
// LOCKED so another worker can claim available rows concurrently. The update
// returns each row's current stage and retry state, with trimmed cache-key
// text. Iteration failures reject the returned batch after the statement may
// already have assigned its claims; lease expiry supplies later recovery.
func (r *AuthCacheInvalidationOutboxRepository) Claim(ctx context.Context, workerID string, limit int, lease time.Duration) ([]keycore.AuthCacheInvalidationEvent, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("nil auth cache invalidation outbox database")
	}
	if limit <= 0 {
		limit = 100
	}
	// PostgreSQL expresses the lease in whole seconds for abandoned-claim recovery.
	leaseSeconds := int64(lease / time.Second)
	if leaseSeconds < 1 {
		leaseSeconds = 30
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT id
			FROM auth_cache_invalidation_outbox
			WHERE available_at <= NOW()
			  AND (claimed_at IS NULL OR claimed_at < NOW() - ($3 * INTERVAL '1 second'))
			ORDER BY id ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE auth_cache_invalidation_outbox AS o
		SET claimed_at = NOW(), claimed_by = $1
		FROM candidates AS c
		WHERE o.id = c.id
		RETURNING o.id, o.cache_key, o.attempts, o.delivery_stage, o.created_at
	`, workerID, limit, leaseSeconds)
	if err != nil {
		return nil, err
	}
	// Close the returned update rows after scanning so the shared database
	// connection becomes available to later claim, retry and acknowledgement operations.
	// Deferred Close errors are discarded; the scan and iteration result remains
	// the returned outcome for this claim attempt.
	defer func() { _ = rows.Close() }()

	events := make([]keycore.AuthCacheInvalidationEvent, 0, limit)
	for rows.Next() {
		var event keycore.AuthCacheInvalidationEvent
		if err := rows.Scan(&event.ID, &event.CacheKey, &event.Attempts, &event.Stage, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.CacheKey = strings.TrimSpace(event.CacheKey)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// ScheduleSecondPass changes an initial-stage row owned by workerID to a
// delayed safety pass. It clears error and claim fields after setting
// availableAt; anything other than one affected row reports a lost or
// ineligible claim.
func (r *AuthCacheInvalidationOutboxRepository) ScheduleSecondPass(ctx context.Context, id int64, workerID string, availableAt time.Time) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE auth_cache_invalidation_outbox
		SET delivery_stage = 1,
			available_at = $3,
			last_error = NULL,
			claimed_at = NULL,
			claimed_by = NULL
		WHERE id = $1 AND claimed_by = $2 AND delivery_stage = 0
	`, id, workerID, availableAt)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("auth cache invalidation claim %d cannot schedule second pass", id)
	}
	return nil
}

// DeleteClaimed deletes the row still owned by workerID after completed
// delivery. A missing or replaced claim returns an error when the affected-row
// count differs from one, and SQL failures retain their error identity.
func (r *AuthCacheInvalidationOutboxRepository) DeleteClaimed(ctx context.Context, id int64, workerID string) error {
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM auth_cache_invalidation_outbox
		WHERE id = $1 AND claimed_by = $2
	`, id, workerID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("auth cache invalidation claim %d is no longer owned by %s", id, workerID)
	}
	return nil
}

// RetryClaimed increments attempts, saves failure text and releases a
// worker-owned row for availableAt. It requires exactly one affected record; a
// lost claim reports an error and leaves its current owner's row unchanged.
func (r *AuthCacheInvalidationOutboxRepository) RetryClaimed(ctx context.Context, id int64, workerID string, availableAt time.Time, lastError string) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE auth_cache_invalidation_outbox
		SET attempts = attempts + 1,
			available_at = $3,
			last_error = $4,
			claimed_at = NULL,
			claimed_by = NULL
		WHERE id = $1 AND claimed_by = $2
	`, id, workerID, availableAt, lastError)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("auth cache invalidation claim %d is no longer owned by %s", id, workerID)
	}
	return nil
}

// Stats queries all retained rows for count, oldest creation time, maximum
// attempts and the latest available-time-ordered stored error. Empty queues
// return zero counts and no oldest timestamp; nullable database observations
// are converted to ordinary result fields.
func (r *AuthCacheInvalidationOutboxRepository) Stats(ctx context.Context) (keycore.AuthCacheInvalidationOutboxStats, error) {
	var (
		stats     keycore.AuthCacheInvalidationOutboxStats
		oldest    sql.NullTime
		lastError sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*), MIN(created_at), COALESCE(MAX(attempts), 0),
			(SELECT last_error
			 FROM auth_cache_invalidation_outbox
			 WHERE last_error IS NOT NULL
			 ORDER BY available_at DESC, id DESC
			 LIMIT 1)
		FROM auth_cache_invalidation_outbox
	`).Scan(&stats.Pending, &oldest, &stats.MaxAttempts, &lastError)
	if err != nil {
		return stats, err
	}
	if oldest.Valid {
		value := oldest.Time
		stats.OldestCreatedAt = &value
	}
	if lastError.Valid {
		stats.LastError = lastError.String
	}
	return stats, nil
}
