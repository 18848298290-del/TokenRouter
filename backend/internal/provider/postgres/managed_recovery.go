package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// ApplyManagedRecoveryStep conditionally updates one recovery step when identity, error, and step-specific state match the observation.
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
	result, err := r.sql.ExecContext(ctx, "UPDATE providers SET "+set+" WHERE id=$1 AND deleted_at IS NULL AND COALESCE(error_message,'')=$2 AND "+where, args...)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 0 {
		return false, err
	}
	// Outbox publication follows the state write; publication failure leaves the state change in place.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &v.ID, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue managed recovery failed: provider=%d step=%d err=%v", v.ID, step, err)
	}
	if step != provider.ManagedRecoveryQuotaScopes {
		r.afterChange(ctx, v.ID)
	}
	return true, nil
}
