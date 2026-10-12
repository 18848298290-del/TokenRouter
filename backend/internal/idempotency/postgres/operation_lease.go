package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	pg "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
)

// A maintenance lease can outlive the worker that acquired it, so takeover needs
// to replace both the task identity and acquisition token. PostgreSQL stores the
// operation ID in request_fingerprint and its token JSON in response_body while
// the row is processing. Availability depends on locked_until, allowing legacy
// rows with no token metadata to be acquired once their locks expire.
//
// Renewal and completion compare the row ID, operation ID, token text, and
// processing state in the same UPDATE. A replaced or finished acquisition updates
// no rows, letting the maintenance worker report ownership loss. Renewal can
// extend an expired deadline when its identity still matches; a successful takeover
// replaces that identity before an earlier worker can update the replacement.

// ClaimOperation acquires an absent or unlocked maintenance lease and returns
// its record with true ownership. A current unexpired lock rejects acquisition;
// a separate lookup supplies the busy record, which can be nil if the row disappears.
//
// The INSERT's conflict update compares locked_until with the caller's Now,
// then replaces the operation ID and token together. A false acquisition is
// followed by a separate record read, so the busy diagnostic can reflect a
// subsequent lease change rather than the exact conflicting row instant.
func (r *idempotencyRepository) ClaimOperation(ctx context.Context, c idempotency.OperationClaim) (*idempotency.IdempotencyRecord, bool, error) {
	var id int64
	// Replace task identity, token, deadlines, and processing state in one statement;
	// the lock predicate lets one acquisition win without exposing partly replaced ownership metadata.
	err := pg.ScanSingleRow(ctx, r.sql, `INSERT INTO idempotency_records(scope,idempotency_key_hash,request_fingerprint,status,response_body,locked_until,expires_at)
 VALUES($1,$2,$3,'processing',$4,$5,$6)
 ON CONFLICT(scope,idempotency_key_hash) DO UPDATE SET request_fingerprint=EXCLUDED.request_fingerprint,status='processing',response_body=EXCLUDED.response_body,response_status=NULL,error_reason=NULL,locked_until=EXCLUDED.locked_until,expires_at=EXCLUDED.expires_at,updated_at=NOW()
 WHERE idempotency_records.locked_until IS NULL OR idempotency_records.locked_until <= $7 RETURNING id`, []any{c.Scope, c.KeyHash, c.OperationID, c.Ownership, c.LockedUntil, c.ExpiresAt, c.Now}, &id)
	if errors.Is(err, sql.ErrNoRows) {
		// A conflict with a live lock yields no returned row. Load its current
		// record for maintenance busy diagnostics instead of treating that conflict as a store outage.
		record, e := r.GetByScopeAndKeyHash(ctx, c.Scope, c.KeyHash)
		return record, false, e
	}
	if err != nil {
		return nil, false, err
	}
	return &idempotency.IdempotencyRecord{
		ID:                 id,
		Scope:              c.Scope,
		IdempotencyKeyHash: c.KeyHash,
		RequestFingerprint: c.OperationID,
		Status:             idempotency.IdempotencyStatusProcessing,
		ResponseBody:       &c.Ownership,
		LockedUntil:        &c.LockedUntil,
		ExpiresAt:          c.ExpiresAt,
	}, true, nil
}

// RenewOperation updates the deadlines of the matching processing acquisition
// and reports whether exactly one row changed. The row ID, operation ID, and
// token text identify the caller's claim.
//
// This comparison allows renewal after a deadline has passed when ownership
// metadata still matches. A takeover that completes first changes that metadata,
// so the earlier caller's update affects no rows. The maintenance renewal
// loop interprets false as ownership loss and cancels the operation's worker context.
func (r *idempotencyRepository) RenewOperation(ctx context.Context, id int64, operationID, ownership string, until, expires time.Time) (bool, error) {
	// Compare ownership and processing state with the deadline update itself, so
	// a worker cannot extend the lease of a different acquisition after takeover.
	result, err := r.sql.ExecContext(ctx, `UPDATE idempotency_records SET locked_until=$4,expires_at=$5,updated_at=NOW() WHERE id=$1 AND request_fingerprint=$2 AND response_body=$3 AND status='processing'`, id, operationID, ownership, until, expires)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// FinishOperation stores a successful release or retryable failure for the
// supplied processing acquisition and reports whether exactly one row changed.
// Success stores a release response and clears the lock; failure stores the
// reason and sets the lock deadline to the current local time.
//
// The final UPDATE compares ownership metadata before replacing response_body,
// keeping an earlier worker from finishing a replacement's operation. A false
// result tells Release that ownership has been lost. Expiration is supplied by
// the caller separately from the immediate failure-unlock or successful release.
func (r *idempotencyRepository) FinishOperation(ctx context.Context, id int64, operationID, ownership string, success bool, reason string, expires time.Time) (bool, error) {
	status := idempotency.IdempotencyStatusFailedRetryable
	var code any
	var body any
	var locked any = time.Now()
	if success {
		status = idempotency.IdempotencyStatusSucceeded
		code = 200
		// The payload contains only a string and boolean, both supported by encoding/json;
		// encoding this release marker has no recoverable value-conversion failure to report.
		encoded, _ := json.Marshal(map[string]any{"operation_id": operationID, "released": true})
		body = string(encoded)
		locked = nil
		reason = ""
	}
	// Replace result metadata only while the processing token still matches. Finishing
	// removes that token from response_body, so a later renewal of this acquisition fails.
	result, err := r.sql.ExecContext(ctx, `UPDATE idempotency_records SET status=$4,response_status=$5,response_body=$6,error_reason=NULLIF($7,''),locked_until=$8,expires_at=$9,updated_at=NOW() WHERE id=$1 AND request_fingerprint=$2 AND response_body=$3 AND status='processing'`, id, operationID, ownership, status, code, body, reason, locked, expires)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// NewOperationLeaseStore binds maintenance lease operations to the supplied SQL executor.
func NewOperationLeaseStore(executor Executor) idempotency.OperationLeaseStore {
	return &idempotencyRepository{sql: executor}
}
