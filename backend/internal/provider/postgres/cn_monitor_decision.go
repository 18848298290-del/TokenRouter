package postgres

import (
	"context"
	"time"
)

// CN quota decisions belong to the provider version that produced the
// observation. A compare against updated_at prevents a delayed balance result
// from replacing a newer configuration or health change; extending a pause
// also requires a later deadline, while clearing requires the stored reason
// to match.
//
// The health statement and scheduling notification are separate operations on
// the supplied executor. An outbox error is observed before snapshot
// synchronization is attempted. With an autocommit executor, the state is
// already committed; a supplied transaction retains its caller's completion
// responsibility and can be aborted by a database error. The applied boolean
// identifies the matched statement, so callers still need to distinguish
// durable commit and successful cache delivery from that result.

// SetCNUsageDecisionCAS applies a CN monitor pause or recovery to the row
// whose updated_at still equals expected. Extending accepts only a later
// until; clearing requires the stored reason to equal reason, and a missing
// or changed row returns false.
//
// The state update precedes a separate outbox attempt on the configured
// executor. That error is observed, then synchronization is attempted and true
// is returned. A caller-supplied transaction still needs completion and can be
// aborted by an outbox database error; true reports the matched statement,
// rather than a committed transaction or delivered snapshot.
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
	// The clear predicate also compares the cause, keeping a different health rule's
	// pause intact even when the monitor observes the current provider version.
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
	// Report the separate event failure while keeping synchronization reachable;
	// a transactional executor's owner still resolves any database error before
	// committing.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &id, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue CN monitor health failed: provider=%d err=%v", id, err)
	}
	r.afterChange(ctx, id)
	return true, nil
}
