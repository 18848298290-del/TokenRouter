package postgres

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Anthropic usage queries can discover a new session-window end while another
// request updates the same provider. The conditional write compares both
// credential identity and the previously observed end, so it applies only to
// the version used for that usage result.
//
// The matching SQL update precedes a separate best-effort scheduling event.
// Event failure is logged after the state change, and this path relies on
// outbox propagation rather than immediate snapshot synchronization. An
// unapplied result reports that the row disappeared or its identity or window
// changed before persistence.

// UpdateUsageSessionWindowEndIfUnchanged writes end while both the usage
// identity and observed session-window end still match. Nullable comparisons
// distinguish an unset window from a previously recorded end, and a missing
// or superseded row returns false.
//
// The matching state statement precedes a separate scheduler event through
// r.sql. An event error is observed after the window update and does not fail
// the method. A caller-supplied transaction remains uncommitted here and can
// be aborted by an outbox database error. This path relies on outbox
// propagation; callers use the applied result to distinguish the matched
// statement from an obsolete observation and still resolve transaction completion.
func (r *ProviderStore) UpdateUsageSessionWindowEndIfUnchanged(ctx context.Context, v provider.UsageObservationVersion, observed *time.Time, end time.Time) (bool, error) {
	where, values, err := usageObservationPredicate(v, 5, false)
	if err != nil {
		return false, err
	}
	args := []any{v.ID, end, observed, r.options.Now()}
	args = append(args, values...)
	// Match the previously observed nullable end as well as identity, allowing a
	// concurrent session-window writer to retain its newer observation
	// before this update.
	result, err := r.sql.ExecContext(ctx, "UPDATE providers SET session_window_end=$2,updated_at=$4 WHERE id=$1 AND deleted_at IS NULL AND session_window_end IS NOT DISTINCT FROM $3 AND "+where, args...)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 0 {
		return false, err
	}
	// Enqueue a full provider change after the window write so the worker reloads
	// scheduling state instead of receiving an obsolete timestamp-only patch.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &v.ID, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue session window end update failed: provider=%d err=%v", v.ID, err)
	}
	return true, nil
}
