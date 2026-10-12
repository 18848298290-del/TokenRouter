package idempotency

import (
	"context"
	"time"
)

// Maintenance operations share a database lease that can be acquired again after
// its lock expires. Acquisition callers supply an operation ID and fresh ownership token,
// allowing renewal and completion to distinguish the current owner from an
// earlier worker that still holds an expired claim.

// OperationClaim identifies a system maintenance operation and its lease owner.
type OperationClaim struct {
	// Scope and KeyHash select the row shared by competing maintenance tasks.
	// ClaimOperation replaces OperationID and Ownership together when that row
	// becomes available for acquisition.
	//
	// Ownership is compared byte-for-byte. Callers supply fresh owner metadata for
	// every acquisition and retain its exact text for renewal and completion.
	// Scope: shared maintenance namespace; KeyHash: hashed lock identifier;
	// OperationID: current maintenance task; Ownership: acquisition owner metadata.
	Scope, KeyHash, OperationID, Ownership string
	// Now decides whether an existing lock has expired. LockedUntil bounds this
	// acquisition's lease, while ExpiresAt controls retention of its database record.
	// Now: acquisition comparison time; LockedUntil: current lease deadline;
	// ExpiresAt: database retention deadline.
	Now, LockedUntil, ExpiresAt time.Time
}

// OperationLeaseStore checks processing state and acquisition identity before
// lease updates.
// Callers use fresh ownership metadata for every claim so later updates distinguish
// a replacement even when its row ID and operation ID match an earlier acquisition.
type OperationLeaseStore interface {
	// ClaimOperation creates or replaces an available maintenance lease and reports
	// ownership. A false result returns a separately read record for busy diagnostics,
	// which may be nil if the row disappeared. Existing rows become available when
	// their lock is absent or expired at Now.
	//
	// The acquisition replaces task identity and owner metadata together. Callers
	// supply fresh Ownership text and retain it for renewal and completion, because
	// a later acquisition of the same shared row belongs to a different worker even
	// when its operation ID names the same maintenance action.
	ClaimOperation(context.Context, OperationClaim) (*IdempotencyRecord, bool, error)
	// RenewOperation extends lease and retention deadlines for a processing record
	// whose ID, operation ID, and ownership token still match. False tells the renewal
	// worker that this acquisition has lost ownership or is already finished.
	//
	// Deadline expiry alone does not prevent renewal while these fields still
	// match. When replacement happens first, its changed owner metadata prevents the
	// earlier worker from extending that replacement's lease. Callers respond to
	// false by cancelling their maintenance work rather than continuing with an
	// obsolete claim.
	RenewOperation(context.Context, int64, string, string, time.Time, time.Time) (bool, error)
	// FinishOperation stores success or retryable failure for the matching processing
	// owner and reports whether it updated the row. Success releases the lease;
	// failure records its reason and makes the lock immediately reclaimable.
	//
	// Callers stop their renewal loop before finishing to avoid renewing a released
	// claim. The operation ID and token distinguish the acquisition from any
	// replacement sharing the row ID, and false tells the caller that its completion
	// was rejected because the matching processing owner was no longer present.
	FinishOperation(context.Context, int64, string, string, bool, string, time.Time) (bool, error)
}
