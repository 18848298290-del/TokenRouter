package postgres

import (
	"context"
	"time"
)

// SetCNUsageDecisionCAS updates quota-related scheduling state when the observed provider version still matches.
// The health update is one SQL statement; a separate best-effort outbox write follows it.
func (r *ProviderStore) SetCNUsageDecisionCAS(ctx context.Context, id int64, expected time.Time, until time.Time, reason string, clear bool) (bool, error) {
	query := `UPDATE providers SET temp_unschedulable_until=$1,temp_unschedulable_reason=$2,updated_at=NOW()
 WHERE id=$3 AND deleted_at IS NULL AND updated_at=$4
 AND (temp_unschedulable_until IS NULL OR temp_unschedulable_until<$1)`
	args := []any{until, reason, id, expected}
	if clear {
		query = `UPDATE providers SET temp_unschedulable_until=NULL,temp_unschedulable_reason=NULL,updated_at=NOW()
 WHERE id=$1 AND deleted_at IS NULL AND updated_at=$2 AND temp_unschedulable_reason=$3`
		args = []any{id, expected, reason}
	}
	result, err := r.sql.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue CN monitor health failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return true, nil
}
