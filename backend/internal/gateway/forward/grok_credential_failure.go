package forward

import (
	"errors"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// Grok credential acquisition failures can belong to one provider or to shared
// refresh infrastructure. Classification selects both a diagnostic reason and
// whether another provider may be tried, while permanent and transient flags
// tell credential recovery which conditional state-update path to consider.
// Provider eligibility, stored snapshots, and remaining request budgets are
// still checked by the credential executor.
//
// Specific sentinel errors and stable text patterns precede broad transport
// failures. Exhausted billing quotas select temporary recovery before the
// entitlement patterns that require provider action. Some generic refresh or
// transport failures are shared when no provider proxy is configured, while a
// configured proxy leaves the unrecognized failure on the provider-transient
// path. The proxy flag describes configuration, not proof that a working proxy
// participated in the failed exchange.

// Grok credential reasons provide stable labels for diagnostic output and
// conditional provider-state recovery independently of raw error messages.
const (
	// GrokCredentialUnavailableClientMessage supplies the final unavailable-provider
	// message used by credential failover without exposing a raw credential-acquisition error.
	GrokCredentialUnavailableClientMessage = "No healthy Grok OAuth provider is currently available"
	// GrokCredentialReasonRevoked identifies a revoked or invalidated OAuth credential.
	GrokCredentialReasonRevoked GatewayFailureReason = "grok_oauth_credential_revoked"
	// GrokCredentialReasonMissing identifies missing or expired OAuth credential data.
	GrokCredentialReasonMissing GatewayFailureReason = "grok_oauth_credentials_missing"
	// GrokCredentialReasonEntitlement identifies entitlement requiring provider action.
	GrokCredentialReasonEntitlement GatewayFailureReason = "grok_oauth_entitlement_action_required"
	// GrokCredentialReasonProxyInvalid identifies invalid provider proxy configuration.
	GrokCredentialReasonProxyInvalid GatewayFailureReason = "grok_oauth_proxy_invalid"
	// GrokCredentialReasonRefreshTransient identifies temporary credential recovery.
	GrokCredentialReasonRefreshTransient GatewayFailureReason = "grok_oauth_refresh_transient"
	// GrokCredentialReasonProviderConfig identifies unavailable shared refresh configuration.
	GrokCredentialReasonProviderConfig GatewayFailureReason = "grok_oauth_provider_config"
	// GrokCredentialReasonProviderDown identifies unavailable shared provider state.
	GrokCredentialReasonProviderDown GatewayFailureReason = "grok_oauth_provider_unavailable"
	// GrokCredentialReasonProviderChanged identifies changed provider eligibility.
	GrokCredentialReasonProviderChanged GatewayFailureReason = "grok_oauth_provider_state_changed"
	// GrokCredentialReasonStateUpdate identifies failed conditional credential updates.
	GrokCredentialReasonStateUpdate GatewayFailureReason = "grok_oauth_provider_state_update_failed"
	// GrokCredentialReasonFailoverTimeout identifies an exhausted request credential budget.
	GrokCredentialReasonFailoverTimeout GatewayFailureReason = "grok_oauth_failover_timeout"
)

// GrokCredentialFailure classifies a credential acquisition failure; its fields are excluded from JSON.
type GrokCredentialFailure struct {
	Scope  GatewayFailureScope  `json:"-"` // affected credential failure scope
	Reason GatewayFailureReason `json:"-"` // stable credential diagnostic label
	Action NextProviderAction   `json:"-"` // provider-switch permission or stop
	// Permanent and Transient select credential recovery's persistent error or
	// temporary cooldown paths. The classifier sets at most one; recovery gives
	// Permanent precedence if another caller supplies both flags.
	Permanent bool   `json:"-"` // persistent credential action requested
	Transient bool   `json:"-"` // temporary credential cooldown requested
	Message   string `json:"-"` // credential diagnostic summary text
	// snapshot retains conditional-update comparison data by reference. Recovery
	// consumes it after classification, so callers leave the supplied snapshot
	// unchanged during that update or provide their own copy before mutation.
	snapshot *providercore.CredentialMutationSnapshot
}

// ClassifyGrokCredentialFailure selects a scope, reason, provider-switch action,
// and intended credential-recovery flags from the error chain. hasProxy records
// whether the selected provider has proxy configuration; it does not certify
// proxy availability or the path used by a failed request.
//
// Specific missing, revoked, quota, entitlement, and proxy evidence takes
// precedence over broad refresh or transport patterns. Shared state/configuration
// failures stop switching, while provider-specific failures permit another
// provider. Unrecognized errors, including nil, return the provider-transient
// classification. The credential executor separately compares retained state,
// applies recovery, and enforces its request budget before any later attempt.
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
		// Exhaustion is classified before entitlement denial because its recovery
		// path uses a temporary cooldown. It does not request the persistent
		// credential-error mutation selected for revoked or denied credentials.
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
		// No recognized sentinel or shared-pattern branch matched. Temporary
		// provider recovery can cool this acquisition failure while its retry
		// action leaves selection of another provider available.
		return GrokCredentialFailure{Scope: GatewayFailureScopeProvider, Reason: GrokCredentialReasonRefreshTransient, Action: NextProviderRetry, Transient: true, Message: "Grok OAuth credential refresh is temporarily unavailable"}
	}
}

// SetSnapshot retains conditional credential-update comparison data by
// reference; nil clears it. The caller leaves that snapshot unchanged while
// recovery consumes it, because this setter does not clone the pointed-to value.
func (f *GrokCredentialFailure) SetSnapshot(snapshot *providercore.CredentialMutationSnapshot) {
	f.snapshot = snapshot
}

// Snapshot returns the retained conditional-update comparison pointer, or nil
// when none was stored. It shares the setter's object; credential recovery
// supplies its current-record snapshot when this result is nil.
func (f GrokCredentialFailure) Snapshot() *providercore.CredentialMutationSnapshot { return f.snapshot }
