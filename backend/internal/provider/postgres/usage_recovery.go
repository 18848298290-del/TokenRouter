package postgres

import (
	"context"
	"encoding/json"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// usage_recovery.go restores active status after a successful usage query when
// the failed identity and error message still match. The single update
// compares complete credentials and proxy identity, allowing a concurrent
// reauthorization or administrator edit to retain its newer state.
//
// Recovery sets active status and clears the message but retains the
// scheduling switch and other health windows. A separate scheduling event is
// attempted after the state update, followed by immediate snapshot
// synchronization. A caller-supplied transaction keeps its completion
// responsibility and can fail after an outbox database error. Missing,
// deleted, or changed rows return an unapplied result so the caller can leave
// its old observation unused.

// ClearUsageErrorIfUnchanged restores active status and an empty message when
// the observed error identity still matches. Observed status must be error;
// complete credentials, proxy, platform, type, and error text are compared,
// and a missing or changed row returns false.
//
// The update retains the scheduling switch and other health windows. Its
// outbox attempt uses the configured executor after the state statement, and
// errors from that attempt are observed after the write. Matching recovery
// attempts snapshot synchronization before returning true. A supplied SQL
// transaction still needs its caller's completion and can be aborted by a
// database error; other health windows keep their own eligibility rules.
func (r *ProviderStore) ClearUsageErrorIfUnchanged(ctx context.Context, v provider.UsageRecoveryVersion) (bool, error) {
	if v.Status != provider.StatusError {
		return false, nil
	}
	credentials := v.Credentials
	if credentials == nil {
		credentials = map[string]any{}
	}
	payload, err := json.Marshal(credentials)
	if err != nil {
		return false, err
	}
	// Match the original error text along with credentials so a successful usage
	// result cannot erase a different failure installed while that query was running.
	result, err := r.sql.ExecContext(ctx, `UPDATE providers SET status=$8,error_message='',updated_at=$9
 WHERE id=$1 AND deleted_at IS NULL AND platform=$2 AND type=$3 AND status=$4
 AND credentials=$5::jsonb AND proxy_id IS NOT DISTINCT FROM $6 AND error_message=$7`, v.ID, v.Platform, v.Type, v.Status, string(payload), v.ProxyID, v.ErrorMessage, provider.StatusActive, r.options.Now())
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
	// Attempt snapshot reload even when notification fails; callers using an outer
	// SQL transaction still account for its state before completing that transaction.
	if err := r.enqueue(ctx, r.sql, ProviderChanged, &v.ID, nil, nil); err != nil {
		r.observe("[SchedulerOutbox] enqueue clear error failed: provider=%d err=%v", v.ID, err)
	}
	r.afterChange(ctx, v.ID)
	return true, nil
}
