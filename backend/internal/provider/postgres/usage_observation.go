package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// usageObservationPredicate builds SQL conditions for the observed identity and, when health is true,
// its limit windows.
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

func (r *ProviderStore) UpdateUsageExtraIfUnchanged(ctx context.Context, v provider.UsageObservationVersion, updates map[string]any) (bool, error) {
	err := r.updateExtra(ctx, v.ID, provider.CloneValues(updates), &v)
	if errors.Is(err, provider.ErrUsageObservationChanged) {
		return false, nil
	}
	return err == nil, err
}

func (r *ProviderStore) SetUsageRateLimitIfUnchanged(ctx context.Context, v provider.UsageObservationVersion, reset time.Time) (bool, error) {
	return r.updateUsageRateLimit(ctx, v, &reset)
}

func (r *ProviderStore) ClearUsageRateLimitIfUnchanged(ctx context.Context, v provider.UsageObservationVersion) (bool, error) {
	return r.updateUsageRateLimit(ctx, v, nil)
}

// updateUsageRateLimit commits health before attempting an outbox write. Clearing also requires
// unchanged limit windows.
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
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &v.ID, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue usage rate limit failed: provider=%d err=%v", v.ID, err)
	}
	r.afterChange(ctx, v.ID)
	return true, nil
}

// UpdatePrivacyModeIfUnchanged checks the observed identity in the Extra/outbox transaction before
// updating privacy mode.
func (r *ProviderStore) UpdatePrivacyModeIfUnchanged(ctx context.Context, v provider.UsageObservationVersion, mode string) (bool, error) {
	return r.UpdateUsageExtraIfUnchanged(ctx, v, map[string]any{"privacy_mode": mode})
}
