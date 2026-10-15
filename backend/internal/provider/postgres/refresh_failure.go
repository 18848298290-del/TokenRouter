package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// refresh_failure.go applies background refresh failures to the matching
// authentication state and clears Antigravity refresh requests. Background
// failure handling compares the complete attempted authentication state before
// changing persistent health. One SQL statement locks the candidate and
// chooses permanent rejection or a longer temporary pause, returning
// identity-match and actual-change results separately so an already longer
// pause still counts as a handled failure.
//
// Only an actual health change triggers outbox and snapshot notifications, and
// an outbox error is logged after that write. Clearing Antigravity's one-time
// request uses a different path: CredentialRefreshStore keeps its identity
// lock while UpdateExtra merges the clearing fields in the same transaction.
// An owned successful commit precedes snapshot synchronization in that path.

// ApplyOAuthRefreshFailure records permanent rejection or extends a cooldown
// when the attempted identity and schedulable value still match. Nil expected
// credentials compare as an empty object, and an unknown action returns an
// error.
//
// The SQL result distinguishes a matching candidate from a changed row. A
// matching equal or longer cooldown returns true without shortening the pause or
// publishing another update. Actual changes attempt an outbox event and
// invoke configured snapshot synchronization; enqueue failure leaves the
// matching result true. RefreshAttempts can therefore classify the failure
// without installing state for a comparison miss.
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
	// Lock and update the candidate in one statement so an administrator change
	// cannot occur between identity comparison and health mutation. Keep a longer
	// existing cooldown while still reporting that the attempted identity
	// matched.
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
	// Finish reading and close the result before sending notifications; the
	// match and change flags must be available before outbox work begins.
	closeErr := rows.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	// A retained longer cooldown needs no fresh notification. When health changed,
	// attempt the outbox write and snapshot sync even if enqueueing reports
	// failure.
	if changed {
		if err := r.enqueue(ctx, r.sql, ProviderChanged, &version.ID, nil, nil); err != nil {
			r.observe("[SchedulerOutbox] enqueue OAuth refresh failure failed: provider=%d err=%v", version.ID, err)
		}
		r.afterChange(ctx, version.ID)
	}
	return matched, nil
}

// ClearAntigravityRefreshRequest clears the flag, reason and timestamp for a
// matching Antigravity OAuth identity. Unsupported platform or type returns
// false before creating any transaction.
//
// CredentialRefreshStore locks the expected identity while UpdateExtra merges
// the clearing fields, so an administrator's new authorization cannot be
// overwritten between comparison and the merge. The same transaction carries
// applicable outbox work, and an owned successful commit triggers afterChange.
// Borrowed transactions leave their final commit and publication to the outer
// owner.
func (r *ProviderStore) ClearAntigravityRefreshRequest(ctx context.Context, version provider.CredentialVersion) (bool, error) {
	if version.Platform != provider.PlatformAntigravity || version.Type != provider.ProviderTypeOAuth {
		return false, nil
	}
	applied, committed, err := (CredentialRefreshStore{Client: r.client, Persist: func(ctx context.Context, id int64, _ map[string]any) error {
		return r.UpdateExtra(ctx, id, provider.ClearedAntigravityRefreshRequest())
	}}).Apply(ctx, version, nil)
	// The Extra update remains protected by the identity transaction through its
	// commit; publish the current provider snapshot only after an owned commit.
	if err == nil && committed {
		r.afterChange(ctx, version.ID)
	}
	return applied, err
}
