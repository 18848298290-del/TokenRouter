package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
)

func (r *ProviderStore) SetGrokCredentialErrorIfMatch(
	ctx context.Context,
	id int64,
	snapshot acctcore.CredentialMutationSnapshot,
	errorMsg string,
	proxyInvalidReason string,
) (bool, error) {
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

// SetGrokOAuthErrorIfCredentialsUnchanged marks an active Grok OAuth provider as invalid when its complete
// JSONB credentials match the observed value and its refresh token is missing or blank.
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

// UpdateGrokOAuthCredentialsIfUnchanged saves credentials when the complete credentials and proxy still match the refresh snapshot.
// The outbox insert shares the update statement, so an outbox failure rolls back the credential update.
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

// SetGrokOAuthRefreshErrorIfCredentialsUnchanged records a background refresh error for an active provider
// whose complete credentials and proxy match the failed attempt, regardless of refresh-token presence.
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

// SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged temporarily pauses an active provider
// when its credentials and proxy match the attempt, extending the pause only when until is later.
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

func (r *ProviderStore) SetGrokCredentialTempUnschedulableIfMatch(
	ctx context.Context,
	id int64,
	snapshot acctcore.CredentialMutationSnapshot,
	until time.Time,
	reason string,
) (bool, error) {
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
