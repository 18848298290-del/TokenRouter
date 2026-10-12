package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// ApplyOAuthRefreshFailure compares the credential identity before updating health and attempts an
// outbox write after commit.
// It reports an identity match even when an existing cooldown is already long enough, so callers can
// count that failure.
func (r *ProviderStore) ApplyOAuthRefreshFailure(ctx context.Context, version provider.RefreshFailureVersion, failure provider.RefreshFailure) (bool, error) {
	payload, err := json.Marshal(version.Credentials)
	if err != nil {
		return false, err
	}
	if version.Credentials == nil {
		payload = []byte(`{}`)
	}
	args := []any{version.ID, version.Platform, version.Type, version.Status, string(payload), version.ProxyID, version.Schedulable, failure.Message}
	var update string
	switch failure.Kind {
	case provider.RefreshFailurePermanent:
		update = `UPDATE providers SET status='error',error_message=$8,schedulable=false,updated_at=$9 WHERE id IN (SELECT id FROM candidate) RETURNING id`
		args = append(args, r.options.Now())
	case provider.RefreshFailureCooldown:
		update = `UPDATE providers SET temp_unschedulable_until=$9,temp_unschedulable_reason=$8,updated_at=NOW() WHERE id IN (SELECT id FROM candidate) AND (temp_unschedulable_until IS NULL OR temp_unschedulable_until<$9) RETURNING id`
		args = append(args, failure.Until)
	default:
		return false, errors.New("unknown OAuth refresh failure action")
	}
	rows, err := r.sql.QueryContext(ctx, `WITH candidate AS (
 SELECT id FROM providers WHERE id=$1 AND deleted_at IS NULL AND platform=$2 AND type=$3 AND status=$4
 AND credentials=$5::jsonb AND proxy_id IS NOT DISTINCT FROM $6 AND schedulable=$7 FOR UPDATE
 ), updated AS (`+update+`) SELECT EXISTS(SELECT 1 FROM candidate),EXISTS(SELECT 1 FROM updated)`, args...)
	if err != nil {
		return false, err
	}
	var matched, changed bool
	if rows.Next() {
		err = rows.Scan(&matched, &changed)
	} else {
		err = rows.Err()
		if err == nil {
			err = errors.New("missing OAuth refresh failure outcome")
		}
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if changed {
		if err := r.enqueue(ctx, r.sql, ProviderChanged, &version.ID, nil, nil); err != nil {
			r.observe("[SchedulerOutbox] enqueue OAuth refresh failure failed: provider=%d err=%v", version.ID, err)
		}
		r.afterChange(ctx, version.ID)
	}
	return matched, nil
}

// ClearAntigravityRefreshRequest locks and rechecks the identity within the Extra/outbox transaction
// so cleanup cannot overwrite a new authorization.
func (r *ProviderStore) ClearAntigravityRefreshRequest(ctx context.Context, version provider.CredentialVersion) (bool, error) {
	if version.Platform != provider.PlatformAntigravity || version.Type != provider.ProviderTypeOAuth {
		return false, nil
	}
	applied, committed, err := (CredentialRefreshStore{Client: r.client, Persist: func(ctx context.Context, id int64, _ map[string]any) error {
		return r.UpdateExtra(ctx, id, provider.ClearedAntigravityRefreshRequest())
	}}).Apply(ctx, version, nil)
	if err == nil && committed {
		r.afterChange(ctx, version.ID)
	}
	return applied, err
}
