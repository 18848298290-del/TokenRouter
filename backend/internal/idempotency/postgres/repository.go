package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// repository.go stores idempotent command records and conditionally reclaims
// eligible execution slots.
// Command records use a unique scope and key hash so retries encounter one
// stored request fingerprint and outcome. Creating a claim returns the generated
// row ID to the coordinator; conflicting inserts leave the existing record for
// a separate fingerprint and status decision. Nullable response and lock fields
// retain their absence when decoded from database rows.
//
// Reclaim compares the expected state and lock deadline with its update, while
// processing extensions also compare the request fingerprint. Ordinary success
// and failure writes update by row ID and leave their caller responsible for
// identifying the intended execution. Expiry cleanup deletes one limited set at
// a time, independently of processing state, using the executor supplied as a
// pool or caller-owned transaction.

// idempotencyRepository applies command state updates through a caller-owned SQL executor.
type idempotencyRepository struct {
	// sql is retained from the caller. Tests may supply a transaction; its owner
	// commits or rolls it back and closes any underlying pool outside this repository.
	sql Executor
}

// Executor accepts either a database connection pool or an explicit SQL transaction.
type Executor interface {
	// QueryContext executes a query using the supplied arguments and operation context.
	// The caller closes returned rows after scanning; a transaction executor keeps
	// query visibility within that caller's transaction.
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	// ExecContext executes a statement and returns its result under the supplied context.
	// The repository uses affected row counts for claim decisions, while the executor's
	// owner controls transaction completion and pool lifetime.
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// NewIdempotencyRepository binds command records to the supplied SQL executor.
// The caller retains ownership of the pool or transaction; this constructor
// starts no workers and performs no connection or transaction lifecycle operations.
func NewIdempotencyRepository(sqlDB Executor) idempotency.IdempotencyRepository {
	return &idempotencyRepository{sql: sqlDB}
}

// CreateProcessing inserts the supplied record when its scope and key hash are
// absent, filling its ID and database timestamps on success. A nil record or
// conflicting key returns false; the caller supplies the intended processing state.
//
// The unique scope/key constraint resolves competing insertions before the
// coordinator invokes business work. Ownership is reported from RETURNING;
// the coordinator reads an existing record only after insertion loses, so retries
// inspect that row without replacing its fingerprint or outcome.
func (r *idempotencyRepository) CreateProcessing(ctx context.Context, record *idempotency.IdempotencyRecord) (bool, error) {
	if record == nil {
		return false, nil
	}
	// The unique scope/key constraint arbitrates initial ownership in one insert;
	// a duplicate yields no returned row and leaves the existing fingerprint for coordinator inspection.
	query := `
		INSERT INTO idempotency_records (
			scope, idempotency_key_hash, request_fingerprint, status, locked_until, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (scope, idempotency_key_hash) DO NOTHING
		RETURNING id, created_at, updated_at
	`
	var createdAt time.Time
	var updatedAt time.Time
	err := postgresinfra.ScanSingleRow(ctx, r.sql, query, []any{
		record.Scope,
		record.IdempotencyKeyHash,
		record.RequestFingerprint,
		record.Status,
		record.LockedUntil,
		record.ExpiresAt,
	}, &record.ID, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	record.CreatedAt = createdAt
	record.UpdatedAt = updatedAt
	return true, nil
}

// GetByScopeAndKeyHash returns the matching command record with independently
// allocated nullable field values. Missing rows return nil without an error;
// callers distinguish that absence from database failure before deciding whether to execute.
func (r *idempotencyRepository) GetByScopeAndKeyHash(ctx context.Context, scope, keyHash string) (*idempotency.IdempotencyRecord, error) {
	query := `
		SELECT
			id, scope, idempotency_key_hash, request_fingerprint, status, response_status,
			response_body, error_reason, locked_until, expires_at, created_at, updated_at
		FROM idempotency_records
		WHERE scope = $1 AND idempotency_key_hash = $2
	`
	record := &idempotency.IdempotencyRecord{}
	var responseStatus sql.NullInt64
	var responseBody sql.NullString
	var errorReason sql.NullString
	var lockedUntil sql.NullTime
	err := postgresinfra.ScanSingleRow(ctx, r.sql, query, []any{scope, keyHash},
		&record.ID,
		&record.Scope,
		&record.IdempotencyKeyHash,
		&record.RequestFingerprint,
		&record.Status,
		&responseStatus,
		&responseBody,
		&errorReason,
		&lockedUntil,
		&record.ExpiresAt,
		&record.CreatedAt,
		&record.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		// Absence is distinct from a query failure: the coordinator treats a missing
		// record after a rejected insert as unavailable storage rather than executing another callback.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if responseStatus.Valid {
		v := int(responseStatus.Int64)
		record.ResponseStatus = &v
	}
	if responseBody.Valid {
		v := responseBody.String
		record.ResponseBody = &v
	}
	if errorReason.Valid {
		v := errorReason.String
		record.ErrorReason = &v
	}
	if lockedUntil.Valid {
		v := lockedUntil.Time
		record.LockedUntil = &v
	}
	return record, nil
}

// TryReclaim changes a record from the caller's observed status to processing
// when its lock is absent or expired, and reports whether a row changed.
// It installs new lock and retention deadlines and clears the recorded failure reason.
//
// Status and deadline comparison share the UPDATE so another claim can make
// this attempt return false instead of overwriting its active lock. ExpiresAt
// is updated but does not decide availability here; the coordinator checks record
// expiration before selecting this path, while retryable failures use their backoff lock.
func (r *idempotencyRepository) TryReclaim(
	ctx context.Context,
	id int64,
	fromStatus string,
	now, newLockedUntil, newExpiresAt time.Time,
) (bool, error) {
	// Compare the observed status and unlocked window with the state change itself,
	// preventing a stale reclaim read from replacing another caller's unexpired processing lock.
	query := `
		UPDATE idempotency_records
		SET status = $2,
			locked_until = $3,
			error_reason = NULL,
			updated_at = NOW(),
			expires_at = $4
		WHERE id = $1
			AND status = $5
			AND (locked_until IS NULL OR locked_until <= $6)
	`
	res, err := r.sql.ExecContext(ctx, query,
		id,
		idempotency.IdempotencyStatusProcessing,
		newLockedUntil,
		newExpiresAt,
		fromStatus,
		now,
	)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// ExtendProcessingLock extends deadlines for a processing record whose request
// fingerprint matches the caller's execution. False means no matching processing
// row changed; existing deadline expiry is not part of this update's predicate.
//
// The fingerprint identifies the request payload and actor, not an individual
// acquisition token. Repeated acquisitions of that same request can share it,
// so this method cannot distinguish those owners. Maintenance lease renewal uses
// the separate RenewOperation check when acquisition-specific ownership is required.
func (r *idempotencyRepository) ExtendProcessingLock(
	ctx context.Context,
	id int64,
	requestFingerprint string,
	newLockedUntil,
	newExpiresAt time.Time,
) (bool, error) {
	// Match the processing fingerprint while replacing both deadlines, so a finished
	// record or a different request identity cannot be renewed through this command path.
	query := `
		UPDATE idempotency_records
		SET locked_until = $2,
			expires_at = $3,
			updated_at = NOW()
		WHERE id = $1
			AND status = $4
			AND request_fingerprint = $5
	`
	res, err := r.sql.ExecContext(
		ctx,
		query,
		id,
		newLockedUntil,
		newExpiresAt,
		idempotency.IdempotencyStatusProcessing,
		requestFingerprint,
	)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// MarkSucceeded stores the callback's response and retention deadline by row ID,
// clearing its failure reason and lock. The statement returns its database error;
// it does not check the prior status, request fingerprint, or affected row count.
func (r *idempotencyRepository) MarkSucceeded(ctx context.Context, id int64, responseStatus int, responseBody string, expiresAt time.Time) error {
	query := `
		UPDATE idempotency_records
		SET status = $2,
			response_status = $3,
			response_body = $4,
			error_reason = NULL,
			locked_until = NULL,
			expires_at = $5,
			updated_at = NOW()
		WHERE id = $1
	`
	_, err := r.sql.ExecContext(ctx, query,
		id,
		idempotency.IdempotencyStatusSucceeded,
		responseStatus,
		responseBody,
		expiresAt,
	)
	return err
}

// MarkFailedRetryable stores the callback failure and next retry deadline by row
// ID. Existing response fields remain as stored; the update reports database
// errors and does not check the prior status, fingerprint, or affected row count.
func (r *idempotencyRepository) MarkFailedRetryable(ctx context.Context, id int64, errorReason string, lockedUntil, expiresAt time.Time) error {
	query := `
		UPDATE idempotency_records
		SET status = $2,
			error_reason = $3,
			locked_until = $4,
			expires_at = $5,
			updated_at = NOW()
		WHERE id = $1
	`
	_, err := r.sql.ExecContext(ctx, query,
		id,
		idempotency.IdempotencyStatusFailedRetryable,
		errorReason,
		lockedUntil,
		expiresAt,
	)
	return err
}

// DeleteExpired removes up to limit records whose retention deadline is at or
// before now and returns the affected count. Nonpositive limits choose five
// hundred, and processing state does not exempt records from this expiry selection.
func (r *idempotencyRepository) DeleteExpired(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 500
	}
	query := `
		WITH victims AS (
			SELECT id
			FROM idempotency_records
			WHERE expires_at <= $1
			ORDER BY expires_at ASC
			LIMIT $2
		)
		DELETE FROM idempotency_records
		WHERE id IN (SELECT id FROM victims)
	`
	res, err := r.sql.ExecContext(ctx, query, now, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
