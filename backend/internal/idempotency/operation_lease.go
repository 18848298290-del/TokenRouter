package idempotency

import (
	"context"
	"time"
)

// OperationClaim identifies a system maintenance operation and its lease owner.
type OperationClaim struct {
	Scope, KeyHash, OperationID, Ownership string
	Now, LockedUntil, ExpiresAt            time.Time
}

// OperationLeaseStore compares ownership when renewing or finishing a claim so stale owners cannot update
// a replacement.
type OperationLeaseStore interface {
	ClaimOperation(context.Context, OperationClaim) (*IdempotencyRecord, bool, error)
	RenewOperation(context.Context, int64, string, string, time.Time, time.Time) (bool, error)
	FinishOperation(context.Context, int64, string, string, bool, string, time.Time) (bool, error)
}
