package forward

import (
	"errors"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

const (
	GrokCredentialUnavailableClientMessage                      = "No healthy Grok OAuth provider is currently available"
	GrokCredentialReasonRevoked            GatewayFailureReason = "grok_oauth_credential_revoked"
	GrokCredentialReasonMissing            GatewayFailureReason = "grok_oauth_credentials_missing"
	GrokCredentialReasonEntitlement        GatewayFailureReason = "grok_oauth_entitlement_action_required"
	GrokCredentialReasonProxyInvalid       GatewayFailureReason = "grok_oauth_proxy_invalid"
	GrokCredentialReasonRefreshTransient   GatewayFailureReason = "grok_oauth_refresh_transient"
	GrokCredentialReasonProviderConfig     GatewayFailureReason = "grok_oauth_provider_config"
	GrokCredentialReasonProviderDown       GatewayFailureReason = "grok_oauth_provider_unavailable"
	GrokCredentialReasonProviderChanged    GatewayFailureReason = "grok_oauth_provider_state_changed"
	GrokCredentialReasonStateUpdate        GatewayFailureReason = "grok_oauth_provider_state_update_failed"
	GrokCredentialReasonFailoverTimeout    GatewayFailureReason = "grok_oauth_failover_timeout"
)

// GrokCredentialFailure classifies a credential acquisition failure; its fields are excluded from JSON.
type GrokCredentialFailure struct {
	Scope     GatewayFailureScope  `json:"-"`
	Reason    GatewayFailureReason `json:"-"`
	Action    NextProviderAction   `json:"-"`
	Permanent bool                 `json:"-"`
	Transient bool                 `json:"-"`
	Message   string               `json:"-"`
	snapshot  *providercore.CredentialMutationSnapshot
}

// ClassifyGrokCredentialFailure classifies the error chain and proxy context in precedence order.
func ClassifyGrokCredentialFailure(hasProxy bool, err error) GrokCredentialFailure {
	stableReason := strings.ToLower(strings.TrimSpace(apperror.Reason(err)))
	message := ""
	if err != nil {
		message = strings.ToLower(err.Error())
	}
	contains := func(values ...string) bool {
		for _, value := range values {
			if strings.Contains(stableReason, value) || strings.Contains(message, value) {
				return true
			}
		}
		return false
	}
	var providerConfigErr *providercore.ProviderConfigurationRefreshError
	var containmentErr *providercore.ProviderCycleContainmentRefreshError

	switch {
	case errors.Is(err, providercore.ErrGrokOAuthRefreshTokenMissing), errors.Is(err, providercore.ErrGrokOAuthAccessTokenMissing), errors.Is(err, providercore.ErrGrokOAuthAccessTokenExpired):
		return GrokCredentialFailure{Scope: GatewayFailureScopeProvider, Reason: GrokCredentialReasonMissing, Action: NextProviderRetry, Permanent: true, Message: "Grok OAuth credentials are missing or expired"}
	case contains("invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_reused", "refresh_token_invalidated", "app_session_terminated"):
		return GrokCredentialFailure{Scope: GatewayFailureScopeProvider, Reason: GrokCredentialReasonRevoked, Action: NextProviderRetry, Permanent: true, Message: "Grok OAuth credentials require provider action"}
	case contains("spending limit", "run out of credits", "out of credits", "credits exhausted", "included free usage"):
		// Billing limits and rolling free quotas can recover without replacing credentials.
		// Mark exhaustion transient so later attempts can probe quota again.
		return GrokCredentialFailure{Scope: GatewayFailureScopeProvider, Reason: GrokCredentialReasonRefreshTransient, Action: NextProviderRetry, Transient: true, Message: "Grok OAuth billing quota is temporarily exhausted"}
	case contains("grok_oauth_entitlement_denied", "entitlement_denied", "access_denied", "subscription required", "no active grok subscription"):
		return GrokCredentialFailure{Scope: GatewayFailureScopeProvider, Reason: GrokCredentialReasonEntitlement, Action: NextProviderRetry, Permanent: true, Message: "Grok OAuth entitlement requires provider action"}
	case errors.Is(err, providercore.ErrGrokOAuthConfiguredProxyMiss), contains("grok_oauth_proxy_not_found"):
		return GrokCredentialFailure{Scope: GatewayFailureScopeProvider, Reason: GrokCredentialReasonProxyInvalid, Action: NextProviderRetry, Permanent: true, Message: "Grok OAuth provider proxy configuration is invalid"}
	case errors.Is(err, providercore.ErrRefreshProviderRereadFailed):
		return GrokCredentialFailure{Scope: GatewayFailureScopeShared, Reason: GrokCredentialReasonProviderDown, Action: NextProviderStop, Message: "Grok OAuth provider state is temporarily unavailable"}
	case errors.Is(err, providercore.ErrRefreshCredentialPersist):
		return GrokCredentialFailure{Scope: GatewayFailureScopeShared, Reason: GrokCredentialReasonProviderDown, Action: NextProviderStop, Message: "Grok OAuth shared credential state is temporarily unavailable"}
	case errors.As(err, &containmentErr):
		return GrokCredentialFailure{Scope: GatewayFailureScopeShared, Reason: GrokCredentialReasonProviderDown, Action: NextProviderStop, Message: "Grok OAuth provider state is temporarily unavailable"}
	case errors.Is(err, providercore.ErrRefreshProviderStateChanged):
		return GrokCredentialFailure{Scope: GatewayFailureScopeProvider, Reason: GrokCredentialReasonProviderChanged, Action: NextProviderRetry, Message: "Grok OAuth provider eligibility changed"}
	case errors.As(err, &providerConfigErr):
		return GrokCredentialFailure{Scope: GatewayFailureScopeShared, Reason: GrokCredentialReasonProviderConfig, Action: NextProviderStop, Message: "Grok OAuth provider configuration is unavailable"}
	case errors.Is(err, providercore.ErrGrokOAuthRefreshNotConfigured), contains("invalid_client", "unauthorized_client", "invalid_scope", "unknown scope", "grok oauth service is not configured", "grok_oauth_proxy_not_available"):
		return GrokCredentialFailure{Scope: GatewayFailureScopeShared, Reason: GrokCredentialReasonProviderConfig, Action: NextProviderStop, Message: "Grok OAuth provider configuration is unavailable"}
	case contains("grok_oauth_proxy_lookup_failed"),
		contains("grok_oauth_token_refresh_failed") && contains("status 403") && !hasProxy:
		return GrokCredentialFailure{Scope: GatewayFailureScopeShared, Reason: GrokCredentialReasonProviderDown, Action: NextProviderStop, Message: "Grok OAuth provider is temporarily unavailable"}
	case contains("grok_oauth_client_init_failed") && !hasProxy:
		return GrokCredentialFailure{Scope: GatewayFailureScopeShared, Reason: GrokCredentialReasonProviderConfig, Action: NextProviderStop, Message: "Grok OAuth provider configuration is unavailable"}
	case contains("grok_oauth_request_failed") && !hasProxy:
		return GrokCredentialFailure{Scope: GatewayFailureScopeShared, Reason: GrokCredentialReasonProviderDown, Action: NextProviderStop, Message: "Grok OAuth provider is temporarily unavailable"}
	case contains("status 429", "status 500", "status 502", "status 503", "status 504") && !hasProxy:
		return GrokCredentialFailure{Scope: GatewayFailureScopeShared, Reason: GrokCredentialReasonProviderDown, Action: NextProviderStop, Message: "Grok OAuth provider is temporarily unavailable"}
	default:
		return GrokCredentialFailure{Scope: GatewayFailureScopeProvider, Reason: GrokCredentialReasonRefreshTransient, Action: NextProviderRetry, Transient: true, Message: "Grok OAuth credential refresh is temporarily unavailable"}
	}
}

// SetSnapshot retains the comparison snapshot for conditional persistence; it is excluded from JSON.
func (f *GrokCredentialFailure) SetSnapshot(snapshot *providercore.CredentialMutationSnapshot) {
	f.snapshot = snapshot
}

// Snapshot returns the retained state-comparison data.
func (f GrokCredentialFailure) Snapshot() *providercore.CredentialMutationSnapshot { return f.snapshot }
