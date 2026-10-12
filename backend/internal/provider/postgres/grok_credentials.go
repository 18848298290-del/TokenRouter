package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// Grok credential failures and refresh results are conditional on the
// identity that produced the request. Complete JSONB credentials protect refreshed
// tokens; refresh and request-containment paths also compare nullable proxy IDs.
// Missing-token containment uses credentials without a proxy comparison, while
// request containment additionally requires current scheduling eligibility.
//
// Each modifying CTE inserts its scheduling event from the rows actually
// updated, so a failure in that statement rejects both effects. The
// affected-row result distinguishes a matching mutation from a superseded
// observation. Successful writes then synchronize the current snapshot with a
// short context detached from request cancellation, allowing local
// containment to finish after the originating request ends.

// SetGrokCredentialErrorIfMatch marks the matching active Grok OAuth identity
// as errored and disables scheduling. The row must still be schedulable and
// outside pause, rate-limit, overload, and enabled-expiry exclusions; false
// means that eligibility or credential/proxy identity changed.
//
// When errorMsg equals proxyInvalidReason, a proxy ID must still be selected
// and absent from nondeleted proxy rows. The modifying CTE inserts its event from
// the matching update so statement failure rejects both effects. Applied
// containment synchronizes the current snapshot through the detached bounded
// context after the SQL operation succeeds.
func (r *ProviderStore) SetGrokCredentialErrorIfMatch(
	ctx context.Context,
	id int64,
	snapshot acctcore.CredentialMutationSnapshot,
	errorMsg string,
	proxyInvalidReason string,
) (bool, error) {
	// Contain only the eligible request identity, including proxy existence when
	// that specific failure reason is supplied, and derive its event from the
	// matched row.
	result, err := r.sql.ExecContext(ctx, `
		WITH updated AS (
		UPDATE providers AS a
		SET status = $1,
			error_message = $2,
			schedulable = false,
			updated_at = NOW()
		WHERE a.id = $3
			AND a.deleted_at IS NULL
			AND a.status = $4
			AND a.platform = $5
			AND a.type = $6
			AND a.schedulable IS TRUE
			AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= NOW())
			AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= NOW())
			AND (a.overload_until IS NULL OR a.overload_until <= NOW())
			AND (a.auto_pause_on_expired IS NOT TRUE OR a.expires_at IS NULL OR a.expires_at > NOW())
			AND a.credentials = $7::jsonb
			AND a.proxy_id IS NOT DISTINCT FROM $8
			AND ($2 <> $9 OR (
				a.proxy_id IS NOT NULL AND NOT EXISTS (
					SELECT 1 FROM proxies p WHERE p.id = a.proxy_id AND p.deleted_at IS NULL
				)
			))
		RETURNING a.id
		)
		INSERT INTO scheduler_outbox (event_type, provider_id, group_id, payload)
		SELECT $10, updated.id, NULL, NULL FROM updated
	`, acctcore.StatusError, errorMsg, id, acctcore.StatusActive, acctcore.PlatformGrok, acctcore.ProviderTypeOAuth,
		snapshot.CredentialsJSON, snapshot.ProxyID, proxyInvalidReason,
		r.eventName(ProviderChanged))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	r.afterChangeDetached(ctx, id)
	return true, nil
}

// SetGrokOAuthErrorIfCredentialsUnchanged marks an active Grok OAuth row with
// a missing or BTRIM-empty refresh token as errored. Complete JSONB
// credentials must still match expectedCredentials; nil expected credentials
// encode as an empty object, and an absent SQL executor returns an error.
//
// The predicate does not compare proxy identity or require current scheduling
// eligibility, because this path handles the stored missing-token identity
// itself. The update and its event share one statement. A mismatch returns
// false, while a matching write disables scheduling and uses detached
// snapshot synchronization before returning true.
func (r *ProviderStore) SetGrokOAuthErrorIfCredentialsUnchanged(
	ctx context.Context,
	id int64,
	expectedCredentials map[string]any,
	errorMsg string,
) (bool, error) {
	if r == nil || r.sql == nil {
		return false, errors.New("provider repository SQL executor is not configured")
	}
	expectedJSON, err := json.Marshal(normalizeJSONMap(expectedCredentials))
	if err != nil {
		return false, err
	}
	// Treat missing-token containment as one identity-owned state change; the
	// matching-row CTE also prevents an event for a token that changed before
	// this write.
	result, err := r.sql.ExecContext(ctx, `
		WITH updated AS (
		UPDATE providers AS a
		SET status = $1,
			error_message = $2,
			schedulable = FALSE,
			updated_at = NOW()
		WHERE a.id = $3
			AND a.deleted_at IS NULL
			AND a.platform = $4
			AND a.type = $5
			AND a.status = $6
			AND a.credentials = $7::jsonb
			AND NULLIF(BTRIM(a.credentials->>'refresh_token'), '') IS NULL
		RETURNING a.id
		)
		INSERT INTO scheduler_outbox (event_type, provider_id, group_id, payload)
		SELECT $8, updated.id, NULL, NULL FROM updated
	`,
		acctcore.StatusError,
		errorMsg,
		id,
		acctcore.PlatformGrok,
		acctcore.ProviderTypeOAuth,
		acctcore.StatusActive,
		string(expectedJSON),
		r.eventName(ProviderChanged),
	)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rowsAffected == 0 {
		return false, nil
	}
	r.afterChangeDetached(ctx, id)
	return true, nil
}

// UpdateGrokOAuthCredentialsIfUnchanged replaces a Grok OAuth credential
// object when complete expected credentials and nullable proxy ID still
// match. Nil credential maps encode as empty objects; this path has no
// active-status predicate, and false reports a missing, deleted, or changed
// identity.
//
// Its modifying CTE inserts a scheduling event from the same updated row, so
// an outbox statement failure rejects the credential change as well. An
// absent SQL executor or failed JSON encoding returns an error. Applied
// replacements synchronize the current snapshot using the detached timeout
// context before the method returns true.
func (r *ProviderStore) UpdateGrokOAuthCredentialsIfUnchanged(
	ctx context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	credentials map[string]any,
) (bool, error) {
	if r == nil || r.sql == nil {
		return false, errors.New("provider repository SQL executor is not configured")
	}
	expectedJSON, err := json.Marshal(normalizeJSONMap(expectedCredentials))
	if err != nil {
		return false, err
	}
	credentialsJSON, err := json.Marshal(normalizeJSONMap(credentials))
	if err != nil {
		return false, err
	}
	// Insert the event from the replaced row on this same statement so a
	// notification failure rejects the token replacement instead of leaving
	// split effects.
	result, err := r.sql.ExecContext(ctx, `
		WITH updated AS (
		UPDATE providers AS a
		SET credentials = $1::jsonb,
			updated_at = NOW()
		WHERE a.id = $2
			AND a.deleted_at IS NULL
			AND a.platform = $3
			AND a.type = $4
			AND a.credentials = $5::jsonb
			AND a.proxy_id IS NOT DISTINCT FROM $6
		RETURNING a.id
		)
		INSERT INTO scheduler_outbox (event_type, provider_id, group_id, payload)
		SELECT $7, updated.id, NULL, NULL FROM updated
	`,
		string(credentialsJSON),
		id,
		acctcore.PlatformGrok,
		acctcore.ProviderTypeOAuth,
		string(expectedJSON),
		expectedProxyID,
		r.eventName(ProviderChanged),
	)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rowsAffected == 0 {
		return false, nil
	}
	r.afterChangeDetached(ctx, id)
	return true, nil
}

// SetGrokOAuthRefreshErrorIfCredentialsUnchanged marks the active Grok OAuth
// identity observed by a failed background refresh as errored. It compares
// complete credentials and nullable proxy ID even when a refresh token
// exists, distinguishing this path from missing-token containment.
//
// Status error and schedulable=false are written with the supplied message.
// Their scheduling event shares the modifying CTE, so statement failure
// rejects both effects. No matching row returns false, and nil expected
// credentials encode as an empty object. Applied errors synchronize the
// current snapshot with cancellation detached from the failed request.
func (r *ProviderStore) SetGrokOAuthRefreshErrorIfCredentialsUnchanged(
	ctx context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	errorMsg string,
) (bool, error) {
	if r == nil || r.sql == nil {
		return false, errors.New("provider repository SQL executor is not configured")
	}
	expectedJSON, err := json.Marshal(normalizeJSONMap(expectedCredentials))
	if err != nil {
		return false, err
	}
	// Record this background failure only while its active identity still matches,
	// coupling the disabled switch and message to the event for that same row.
	result, err := r.sql.ExecContext(ctx, `
		WITH updated AS (
		UPDATE providers AS a
		SET status = $1,
			error_message = $2,
			schedulable = FALSE,
			updated_at = NOW()
		WHERE a.id = $3
			AND a.deleted_at IS NULL
			AND a.platform = $4
			AND a.type = $5
			AND a.status = $6
			AND a.credentials = $7::jsonb
			AND a.proxy_id IS NOT DISTINCT FROM $8
		RETURNING a.id
		)
		INSERT INTO scheduler_outbox (event_type, provider_id, group_id, payload)
		SELECT $9, updated.id, NULL, NULL FROM updated
	`,
		acctcore.StatusError,
		errorMsg,
		id,
		acctcore.PlatformGrok,
		acctcore.ProviderTypeOAuth,
		acctcore.StatusActive,
		string(expectedJSON),
		expectedProxyID,
		r.eventName(ProviderChanged),
	)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rowsAffected == 0 {
		return false, nil
	}
	r.afterChangeDetached(ctx, id)
	return true, nil
}

// SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged records a
// background-refresh pause while complete credentials, proxy ID, and active
// Grok OAuth status still match. A stored pause at least as late as until
// keeps its deadline and reason, returning false even when identity remains
// current.
//
// The matching update and its scheduling event share one statement, so the
// pause cannot survive an event insertion failure in that statement. Nil
// expected credentials encode as an empty object, and until is supplied
// directly rather than validated as future time. Applied extensions
// synchronize the current snapshot through the detached timeout context.
func (r *ProviderStore) SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged(
	ctx context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	until time.Time,
	reason string,
) (bool, error) {
	if r == nil || r.sql == nil {
		return false, errors.New("provider repository SQL executor is not configured")
	}
	expectedJSON, err := json.Marshal(normalizeJSONMap(expectedCredentials))
	if err != nil {
		return false, err
	}
	// Require a later pause before the CTE emits an event; an equal or earlier
	// observation leaves the current reason attached to the longer stored deadline.
	result, err := r.sql.ExecContext(ctx, `
		WITH updated AS (
		UPDATE providers AS a
		SET temp_unschedulable_until = $1,
			temp_unschedulable_reason = $2,
			updated_at = NOW()
		WHERE a.id = $3
			AND a.deleted_at IS NULL
			AND a.platform = $4
			AND a.type = $5
			AND a.status = $6
			AND a.credentials = $7::jsonb
			AND a.proxy_id IS NOT DISTINCT FROM $8
			AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until < $1)
		RETURNING a.id
		)
		INSERT INTO scheduler_outbox (event_type, provider_id, group_id, payload)
		SELECT $9, updated.id, NULL, NULL FROM updated
	`,
		until,
		reason,
		id,
		acctcore.PlatformGrok,
		acctcore.ProviderTypeOAuth,
		acctcore.StatusActive,
		string(expectedJSON),
		expectedProxyID,
		r.eventName(ProviderChanged),
	)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rowsAffected == 0 {
		return false, nil
	}
	r.afterChangeDetached(ctx, id)
	return true, nil
}

// SetGrokCredentialTempUnschedulableIfMatch contains a request-time Grok
// credential failure while its identity remains eligible. Complete
// credentials and proxy ID must match the snapshot, and scheduling, pause,
// rate-limit, overload, and enabled-expiry predicates are checked at database
// time.
//
// The deadline takes the later of the stored value and until, while the
// reason is replaced; callers supply the intended deadline, which is not
// checked for future time here. The update and event share one statement. A
// failed match returns false, and matching containment uses detached snapshot
// synchronization after SQL succeeds.
func (r *ProviderStore) SetGrokCredentialTempUnschedulableIfMatch(
	ctx context.Context,
	id int64,
	snapshot acctcore.CredentialMutationSnapshot,
	until time.Time,
	reason string,
) (bool, error) {
	// Recheck request eligibility in SQL before choosing the later deadline, so
	// containment cannot override a provider changed or paused after credential
	// observation.
	result, err := r.sql.ExecContext(ctx, `
		WITH updated AS (
		UPDATE providers AS a
		SET temp_unschedulable_until = CASE
				WHEN a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until < $1 THEN $1
				ELSE a.temp_unschedulable_until
			END,
			temp_unschedulable_reason = $2,
			updated_at = NOW()
		WHERE a.id = $3
			AND a.deleted_at IS NULL
			AND a.status = $4
			AND a.platform = $5
			AND a.type = $6
			AND a.schedulable IS TRUE
			AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= NOW())
			AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= NOW())
			AND (a.overload_until IS NULL OR a.overload_until <= NOW())
			AND (a.auto_pause_on_expired IS NOT TRUE OR a.expires_at IS NULL OR a.expires_at > NOW())
			AND a.credentials = $7::jsonb
			AND a.proxy_id IS NOT DISTINCT FROM $8
		RETURNING a.id
		)
		INSERT INTO scheduler_outbox (event_type, provider_id, group_id, payload)
		SELECT $9, updated.id, NULL, NULL FROM updated
	`, until, reason, id, acctcore.StatusActive, acctcore.PlatformGrok, acctcore.ProviderTypeOAuth,
		snapshot.CredentialsJSON, snapshot.ProxyID, r.eventName(ProviderChanged))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	r.afterChangeDetached(ctx, id)
	return true, nil
}
