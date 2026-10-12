package postgres

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// ClearRefreshCooldownIfUnchanged clears one matching identity and cooldown window, then attempts outbox publication.
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
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &v.ID, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue clear temp unschedulable failed: provider=%d err=%v", v.ID, err)
	}
	r.afterChange(ctx, v.ID)
	return true, nil
}
