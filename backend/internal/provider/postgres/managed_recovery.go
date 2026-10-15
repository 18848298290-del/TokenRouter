package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// managed_recovery.go applies conditional managed-recovery steps and publishes
// their scheduling changes.
// Managed refresh recovery clears five pieces of health state through
// separately invoked conditional writes. Each step compares the observed
// credential identity and error message; the rate-limit, Extra-key, and
// temporary-pause steps add their own observed-state comparison so a later
// administrator or request update can stop recovery.
//
// The provider core advances its expected version after each applied step and
// stops when a write fails or no longer matches. With the application's
// autocommit executor, committed steps remain durable if a later step fails.
// A supplied transaction instead retains its caller's completion and
// database-error behavior. Scheduling events follow each state write;
// quota-scope cleanup defers immediate snapshot synchronization while the
// other steps attempt to publish the latest record.

// ApplyManagedRecoveryStep applies one health cleanup while the observed
// identity and error message still match. Each step additionally compares the
// state it clears, and an error-recovery step requires the observed status to
// be error; unknown steps return an error.
//
// A successful state statement is followed by a best-effort scheduling event
// on the configured executor. Event failure is logged after the state update;
// a database error can still abort a supplied transaction. Quota-scope cleanup
// skips immediate snapshot synchronization, while other steps attempt it
// before returning true. The core advances its expected version between
// separate calls. Earlier cleanup remains durable when its statement has
// already committed through the application's autocommit executor.
func (r *ProviderStore) ApplyManagedRecoveryStep(ctx context.Context, step provider.ManagedRecoveryStep, v provider.ManagedRecoveryVersion) (bool, error) {
	args := []any{v.ID, v.ErrorMessage}
	where, values, err := usageObservationPredicate(v.UsageObservationVersion, 3, step == provider.ManagedRecoveryRateLimit)
	if err != nil {
		return false, err
	}
	args = append(args, values...)
	set := ""
	switch step {
	case provider.ManagedRecoveryError:
		if v.Status != provider.StatusError {
			return false, nil
		}
		args = append(args, provider.StatusActive, r.options.Now())
		set = fmt.Sprintf("status=$%d,error_message='',updated_at=$%d", len(args)-1, len(args))
	case provider.ManagedRecoveryRateLimit:
		set = "rate_limited_at=NULL,rate_limit_reset_at=NULL,overload_until=NULL,updated_at=NOW()"
	case provider.ManagedRecoveryQuotaScopes, provider.ManagedRecoveryModelLimits:
		key := "antigravity_quota_scopes"
		if step == provider.ManagedRecoveryModelLimits {
			key = "model_rate_limits"
		}
		var payload any
		if value, present := v.Extra[key]; present {
			encoded, encodeErr := json.Marshal(value)
			if encodeErr != nil {
				return false, encodeErr
			}
			payload = string(encoded)
		}
		args = append(args, payload)
		where += fmt.Sprintf(" AND extra -> '%s' IS NOT DISTINCT FROM $%d::jsonb", key, len(args))
		set = fmt.Sprintf("extra=COALESCE(extra,'{}'::jsonb)-'%s',updated_at=NOW()", key)
	case provider.ManagedRecoveryTemporary:
		args = append(args, v.Until, v.Reason)
		where += fmt.Sprintf(" AND temp_unschedulable_until IS NOT DISTINCT FROM $%d AND COALESCE(temp_unschedulable_reason,'')=$%d", len(args)-1, len(args))
		set = "temp_unschedulable_until=NULL,temp_unschedulable_reason=NULL,updated_at=NOW()"
	default:
		return false, fmt.Errorf("unknown managed recovery step: %d", step)
	}
	// Each statement compares the identity and this step's observed state; the core
	// advances that observation only after this independently applied cleanup succeeds.
	result, err := r.sql.ExecContext(ctx, "UPDATE providers SET "+set+" WHERE id=$1 AND deleted_at IS NULL AND COALESCE(error_message,'')=$2 AND "+where, args...)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 0 {
		return false, err
	}
	// Observe event errors without returning them as step errors; transaction owners
	// still account for database failures before accepting these updates as committed.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &v.ID, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue managed recovery failed: provider=%d step=%d err=%v", v.ID, step, err)
	}
	// Quota-scope cleanup relies on the event path, matching the dedicated scope
	// writer; other cleanup steps also synchronize the current provider snapshot here.
	if step != provider.ManagedRecoveryQuotaScopes {
		r.afterChange(ctx, v.ID)
	}
	return true, nil
}
