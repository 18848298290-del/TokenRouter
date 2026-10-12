package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// Upstream usage and privacy results are tied to the identity observed before
// the remote request. Their SQL predicates compare platform, provider type,
// status, credentials, proxy, shadow parent, and quota dimension; clearing
// health additionally compares all recorded rate-limit and overload windows.
//
// Extra changes delegate to the JSONB patch transaction and turn a version
// conflict into an unapplied result. Rate-limit changes write health through
// the supplied SQL executor before attempting their scheduling event. Event
// errors are observed after the write; a supplied transaction still retains
// caller-owned completion and can be aborted by a database error. Snapshot
// synchronization follows the event attempt, and the applied result describes
// the state match independently of those delivery and completion outcomes.

// usageObservationPredicate returns SQL identity conditions and their
// positional values beginning at offset. Nil credentials encode as an empty
// JSON object; health adds nullable rate-limit and overload comparisons, and
// the caller supplies ID and deletion predicates around the returned
// expression.
func usageObservationPredicate(v provider.UsageObservationVersion, offset int, health bool) (string, []any, error) {
	credentials := v.Credentials
	if credentials == nil {
		credentials = map[string]any{}
	}
	payload, err := json.Marshal(credentials)
	if err != nil {
		return "", nil, err
	}
	values := []any{v.Platform, v.Type, v.Status, string(payload), v.ProxyID, v.ParentProviderID, v.QuotaDimension}
	condition := fmt.Sprintf("platform=$%d AND type=$%d AND status=$%d AND credentials=$%d::jsonb AND proxy_id IS NOT DISTINCT FROM $%d AND parent_provider_id IS NOT DISTINCT FROM $%d AND quota_dimension=$%d", offset, offset+1, offset+2, offset+3, offset+4, offset+5, offset+6)
	if health {
		condition += fmt.Sprintf(" AND rate_limited_at IS NOT DISTINCT FROM $%d AND rate_limit_reset_at IS NOT DISTINCT FROM $%d AND overload_until IS NOT DISTINCT FROM $%d", offset+7, offset+8, offset+9)
		values = append(values, v.RateLimitedAt, v.RateLimitResetAt, v.OverloadUntil)
	}
	return condition, values, nil
}

// UpdateUsageExtraIfUnchanged copies supported containers in updates before
// delegating the identity-checked Extra patch. A version conflict returns
// false with no error; an empty effective patch succeeds before any identity
// check, while other persistence errors are returned.
func (r *ProviderStore) UpdateUsageExtraIfUnchanged(ctx context.Context, v provider.UsageObservationVersion, updates map[string]any) (bool, error) {
	err := r.updateExtra(ctx, v.ID, provider.CloneValues(updates), &v)
	if errors.Is(err, provider.ErrUsageObservationChanged) {
		return false, nil
	}
	return err == nil, err
}

// SetUsageRateLimitIfUnchanged sets the observation time and reset while the
// recorded provider identity still matches. It can replace an existing reset
// with the supplied value; false reports no matching row, and outbox failures
// are logged after the state write.
func (r *ProviderStore) SetUsageRateLimitIfUnchanged(ctx context.Context, v provider.UsageObservationVersion, reset time.Time) (bool, error) {
	return r.updateUsageRateLimit(ctx, v, &reset)
}

// ClearUsageRateLimitIfUnchanged clears rate-limit and overload columns only
// while the identity and all observed limit windows match. False reports a
// changed or missing row, allowing callers to retain a newer window;
// scheduling-event failures are logged after matching writes.
func (r *ProviderStore) ClearUsageRateLimitIfUnchanged(ctx context.Context, v provider.UsageObservationVersion) (bool, error) {
	return r.updateUsageRateLimit(ctx, v, nil)
}

// updateUsageRateLimit changes health for the complete usage identity and
// reports whether the row matched. A nil reset clears rate-limit and overload
// columns while comparing their observed values; a supplied reset overwrites
// rate-limit columns and leaves overload intact.
//
// The health statement and outbox attempt are separate operations on r.sql.
// Event errors are sent to the observer, then immediate snapshot
// synchronization is attempted. The method leaves caller-supplied
// transactions uncommitted, and a database error can abort them. The returned
// boolean describes the state match independently of durable commit or the
// callback's delivery outcome.
func (r *ProviderStore) updateUsageRateLimit(ctx context.Context, v provider.UsageObservationVersion, reset *time.Time) (bool, error) {
	now := r.options.Now()
	set := "rate_limited_at=NULL,rate_limit_reset_at=NULL,overload_until=NULL,updated_at=$2"
	args := []any{v.ID, now}
	if reset != nil {
		set = "rate_limited_at=$2,rate_limit_reset_at=$3,updated_at=$2"
		args = append(args, *reset)
	}
	where, values, err := usageObservationPredicate(v, len(args)+1, reset == nil)
	if err != nil {
		return false, err
	}
	args = append(args, values...)
	// Identity predicates keep a remote result attached to the provider that produced
	// it; recovery additionally compares limit windows so a newer cooldown can win.
	result, err := r.sql.ExecContext(ctx, "UPDATE providers SET "+set+" WHERE id=$1 AND deleted_at IS NULL AND "+where, args...)
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
	// Observe this event failure separately from the matched health statement;
	// the return value describes that match while an outer owner resolves
	// transaction completion.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &v.ID, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue usage rate limit failed: provider=%d err=%v", v.ID, err)
	}
	r.afterChange(ctx, v.ID)
	return true, nil
}

// UpdatePrivacyModeIfUnchanged writes privacy_mode through the
// identity-checked Extra patch. A superseded observation returns false with
// no error; scheduler-relevant patch handling owns the transaction and event
// write, while outer context transactions retain their caller's commit
// responsibility.
func (r *ProviderStore) UpdatePrivacyModeIfUnchanged(ctx context.Context, v provider.UsageObservationVersion, mode string) (bool, error) {
	return r.UpdateUsageExtraIfUnchanged(ctx, v, map[string]any{"privacy_mode": mode})
}
