package postgres

import (
	"context"
	"fmt"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
)

// lifecycle_participant.go binds Key deletion and group migration to the Ent
// transaction supplied by a lifecycle operation. The participant retains that
// transaction and its client, supplies a tombstone for deletion, and leaves
// final commit or rollback to its caller.

// LifecycleParticipant performs Key deletion and binding changes in its
// caller's transaction.
type LifecycleParticipant struct {
	store *KeyStore // transaction-bound Key mutation store
	// tx is retained from the lifecycle caller. The caller completes the
	// transaction after every participant finishes, including rollback after a failed write.
	tx *dbent.Tx
}

// LifecycleInTx binds a Key participant to the supplied active Ent transaction.
// The caller owns commit and rollback and keeps the transaction usable until
// participant calls finish; the usage-total callback remains shared with the
// source store.
func (r *KeyStore) LifecycleInTx(tx *dbent.Tx) *LifecycleParticipant {
	return &LifecycleParticipant{store: &KeyStore{client: tx.Client(), sql: tx.Client(), usageTotals: r.usageTotals}, tx: tx}
}

// DeleteWithAudit tombstones and soft-deletes id using the retained transaction
// client. Repeated deletion follows KeyDeleteWithTombstone behavior; the
// participant returns write failures and leaves final transaction completion to
// its lifecycle caller.
func (p *LifecycleParticipant) DeleteWithAudit(ctx context.Context, id int64) error {
	return p.store.KeyDeleteWithTombstone(ctx, p.tx.Client(), id, fmt.Sprintf("__deleted__%d__%d", id, time.Now().UnixNano()))
}

// UpdateGroupIDByUserAndGroup migrates the creator's ordinary and composite
// bindings within the retained transaction. It returns affected binding count
// and failure, while the caller owns rollback of any preceding writes on error.
func (p *LifecycleParticipant) UpdateGroupIDByUserAndGroup(ctx context.Context, userID, oldID, newID int64) (int64, error) {
	return p.store.UpdateGroupIDByUserAndGroup(dbent.NewTxContext(ctx, p.tx), userID, oldID, newID)
}
