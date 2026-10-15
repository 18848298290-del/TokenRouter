package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/creative"
)

// outbox.go persists creative recovery actions and fences worker updates with
// claim tokens.
// Creative recovery actions remain in PostgreSQL while queue and funds
// operations can fail independently. A row is unique by run and operation, and
// a pending action becomes leased when a worker claims it. Completed or
// cancelled rows remain terminal when Ensure is called again, while an
// existing live lease keeps its token and availability.
//
// Claim selects due pending rows or expired leases using one SQL update with
// skipped locked candidates. The returned token fences completion, retry, and
// cancellation through a matching leased-row predicate. A worker whose token
// no longer matches receives an ownership error instead of overwriting the
// current claim. Lease expiration makes abandoned work eligible for a later
// claim, separately from image bytes that live in transient storage.

// The compile-time assertion checks that this SQL store supplies every
// creative outbox recovery operation required by the domain interface.
var _ creative.CreativeRunOutboxRepository = (*creativeRunOutboxRepository)(nil)

// creativeRunOutboxRepository stores recoverable creative actions using PostgreSQL SQL.
type creativeRunOutboxRepository struct {
	// db is borrowed from application wiring and remains open while outbox
	// calls execute; the repository leaves connection-pool shutdown to its owner.
	db *sql.DB // borrowed outbox SQL connection pool
}

// NewCreativeRunOutboxRepository retains the supplied SQL pool for creative
// recovery actions. Callers own its lifetime; a nil pool is accepted here and
// repository operations return a configuration error.
func NewCreativeRunOutboxRepository(db *sql.DB) creative.CreativeRunOutboxRepository {
	return &creativeRunOutboxRepository{db: db}
}

// Ensure creates a run/operation recovery row or updates its pending
// availability. Blank run or operation is rejected and zero availability uses
// the current time.
//
// The conflict update preserves done, cancelled, and leased status and
// availability, including the active lease token and expiry. Only a pending
// record is refreshed for another claim. This keeps repeated task provisioning
// from reactivating completed work or displacing an independently claimed
// action while its worker performs recovery.
func (r *creativeRunOutboxRepository) Ensure(ctx context.Context, runID string, operation creative.CreativeRunOutboxOperation, availableAt time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("creative run outbox database is not configured")
	}
	if strings.TrimSpace(runID) == "" || operation == "" {
		return errors.New("creative run outbox run and operation are required")
	}
	if availableAt.IsZero() {
		availableAt = time.Now()
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO creative_run_outbox (run_id, operation, status, available_at)
		VALUES ($1, $2, 'pending', $3)
		ON CONFLICT (run_id, operation) DO UPDATE
		SET status = CASE WHEN creative_run_outbox.status IN ('done', 'cancelled', 'leased') THEN creative_run_outbox.status ELSE 'pending' END,
			available_at = CASE WHEN creative_run_outbox.status IN ('done', 'cancelled', 'leased') THEN creative_run_outbox.available_at ELSE EXCLUDED.available_at END,
			lease_token = CASE WHEN creative_run_outbox.status = 'leased' THEN creative_run_outbox.lease_token ELSE NULL END,
			lease_until = CASE WHEN creative_run_outbox.status = 'leased' THEN creative_run_outbox.lease_until ELSE NULL END,
			updated_at = NOW()
	`, runID, string(operation), availableAt)
	return err
}

// Claim returns due pending actions and expired leases under one new batch
// token. Nonpositive limits use fifty and nonpositive leases use two minutes;
// positive durations are reduced to whole seconds with a one-second floor.
//
// Candidates are ordered by row ID and selected with FOR UPDATE SKIP LOCKED
// inside the update statement. Every claimed row increments its attempt count
// and receives the same token and lease expiry. Later writes require that
// row/token pair, and abandoned leased actions become eligible again after
// their SQL expiry passes.
func (r *creativeRunOutboxRepository) Claim(ctx context.Context, workerID string, limit int, lease time.Duration) ([]creative.CreativeRunOutbox, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("creative run outbox database is not configured")
	}
	if limit <= 0 {
		limit = 50
	}
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	// SQL expiry uses whole seconds; retain a one-second minimum so a positive
	// subsecond caller lease still has a future expiration for the claimed rows.
	leaseSeconds := max(int64(lease/time.Second), 1)
	token, err := newCreativeOutboxLeaseToken(workerID)
	if err != nil {
		return nil, err
	}
	// Candidate locking and lease publication share one statement, so workers
	// skip rows another claim is changing and publish ownership with the selected
	// batch instead of separating selection from its persistent lease update.
	rows, err := r.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT id
			FROM creative_run_outbox
			WHERE available_at <= NOW()
			  AND (status = 'pending' OR (status = 'leased' AND lease_until < NOW()))
			ORDER BY id ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE creative_run_outbox AS o
		SET status = 'leased',
			lease_token = $1,
			lease_until = NOW() + ($3 * INTERVAL '1 second'),
			attempt_count = o.attempt_count + 1,
			updated_at = NOW()
		FROM candidates AS c
		WHERE o.id = c.id
		RETURNING o.id, o.run_id, o.operation, o.status, o.available_at,
			o.lease_token, o.lease_until, o.attempt_count, COALESCE(o.last_error, ''),
			o.created_at, o.updated_at
	`, token, limit, leaseSeconds)
	if err != nil {
		return nil, err
	}
	// Close the returned rows after decoding, including scan failures. The claim
	// update has its own SQL lease lifetime, so discarded decoding results can
	// become recoverable after that expiry rather than requiring a live iterator.
	defer func() { _ = rows.Close() }()
	out := make([]creative.CreativeRunOutbox, 0, limit)
	for rows.Next() {
		var event creative.CreativeRunOutbox
		var operation, status string
		if err := rows.Scan(
			&event.ID, &event.RunID, &operation, &status, &event.AvailableAt,
			&event.LeaseToken, &event.LeaseUntil, &event.AttemptCount, &event.LastError,
			&event.CreatedAt, &event.UpdatedAt,
		); err != nil {
			return nil, err
		}
		event.Operation = creative.CreativeRunOutboxOperation(operation)
		event.Status = creative.CreativeRunOutboxStatus(status)
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Complete marks the matching leased action done and clears its lease and last
// error. An absent or replaced claim returns an ownership error from
// updateClaimed instead of silently reporting completion.
func (r *creativeRunOutboxRepository) Complete(ctx context.Context, id int64, leaseToken string) error {
	return r.updateClaimed(ctx, id, leaseToken, `
		SET status = 'done', lease_token = NULL, lease_until = NULL, last_error = NULL, updated_at = NOW()
	`)
}

// Retry returns the matching leased action to pending with supplied
// availability and trimmed error text. Zero availability uses one minute from
// now, and a missing or replaced claim returns an ownership error.
func (r *creativeRunOutboxRepository) Retry(ctx context.Context, id int64, leaseToken string, availableAt time.Time, lastError string) error {
	if availableAt.IsZero() {
		availableAt = time.Now().Add(time.Minute)
	}
	return r.updateClaimed(ctx, id, leaseToken, `
		SET status = 'pending', available_at = $3, lease_token = NULL, lease_until = NULL,
			last_error = $4, updated_at = NOW()
	`, availableAt, strings.TrimSpace(lastError))
}

// Cancel marks the matching leased action cancelled and clears its lease
// fields. A replaced or absent claim is reported as an ownership error,
// keeping another worker current action unchanged.
func (r *creativeRunOutboxRepository) Cancel(ctx context.Context, id int64, leaseToken string) error {
	return r.updateClaimed(ctx, id, leaseToken, `
		SET status = 'cancelled', lease_token = NULL, lease_until = NULL, updated_at = NOW()
	`)
}

// updateClaimed executes an internal update clause for the supplied positive ID
// and nonblank lease token. The clause and extra placeholders come from
// repository methods; the token is compared exactly as supplied.
//
// The statement matches ID, token, and leased status together, fencing
// completion, retry, and cancellation against a replacement claim. Expiry
// itself is not compared here, so the existing token remains eligible until its
// status or token changes. Exactly one affected row establishes success; other
// counts report lost ownership, while execution and result-count errors remain
// errors.
func (r *creativeRunOutboxRepository) updateClaimed(ctx context.Context, id int64, leaseToken, setClause string, extra ...any) error {
	if r == nil || r.db == nil {
		return errors.New("creative run outbox database is not configured")
	}
	if id <= 0 || strings.TrimSpace(leaseToken) == "" {
		return errors.New("creative run outbox claim is invalid")
	}
	args := []any{id, leaseToken}
	args = append(args, extra...)
	query := "UPDATE creative_run_outbox " + setClause + " WHERE id = $1 AND lease_token = $2 AND status = 'leased'"
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("creative run outbox claim %d is no longer owned", id)
	}
	return nil
}

// newCreativeOutboxLeaseToken returns the trimmed worker label, a colon, and
// sixteen random bytes in lowercase hex. A blank label uses creative, and a
// returned token has nil error. crypto/rand.Read terminates the process when
// io.ReadFull reports an error.
func newCreativeOutboxLeaseToken(workerID string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		workerID = "creative"
	}
	return workerID + ":" + hex.EncodeToString(raw[:]), nil
}
