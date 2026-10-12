package postgres

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Refresh recovery clears a temporary window only when its deadline, reason and
// observed authentication identity still match. The conditional SQL update
// leaves a newer pause intact, then a successful clear attempts an outbox event
// and invokes configured snapshot synchronization.
//
// The predicate includes parent selection and quota dimension, because recovery
// must refer to the same provider role as the exchange. Failure to enqueue the
// event is logged after the health update rather than returned as a failed
// clear. Post actions use the true result to clear their local pause before
// later scheduler publication.

// ClearRefreshCooldownIfUnchanged clears the deadline and reason for the exact
// observed identity and window. A comparison miss returns false and leaves a
// newer pause untouched; serialization and SQL failures return errors.
//
// After a matched update, outbox enqueueing is attempted and errors are logged,
// then afterChange invokes configured snapshot synchronization. The health write
// and this notification are separate operations, so an enqueue failure still
// returns true. Callers use that result to update their local health fields
// before publishing any exchanged record.
func (r *ProviderStore) ClearRefreshCooldownIfUnchanged(ctx context.Context, v provider.RefreshCooldownVersion) (bool, error) {
	where, args, err := usageObservationPredicate(provider.UsageObservationVersion{CredentialVersion: v.CredentialVersion, ParentProviderID: v.ParentProviderID, QuotaDimension: v.QuotaDimension}, 4, false)
	if err != nil {
		return false, err
	}
	values := []any{v.ID, v.Until, v.Reason}
	values = append(values, args...)
	result, err := r.sql.ExecContext(ctx, `UPDATE providers SET temp_unschedulable_until=NULL,temp_unschedulable_reason=NULL,updated_at=NOW()
 WHERE id=$1 AND deleted_at IS NULL AND temp_unschedulable_until IS NOT DISTINCT FROM $2 AND COALESCE(temp_unschedulable_reason,'')=$3 AND `+where, values...)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows == 0 {
		return false, nil
	}
	// The matching health update already succeeded. Attempt its outbox notice
	// and synchronize the current snapshot even when enqueueing fails.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &v.ID, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue clear temp unschedulable failed: provider=%d err=%v", v.ID, err)
	}
	r.afterChange(ctx, v.ID)
	return true, nil
}
