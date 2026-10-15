package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"

	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// request_credentials.go acquires provider credentials and chooses Grok
// recovery or stop decisions within the request budget.
// Grok OAuth credential failures can race with refresh, provider disablement,
// and request cancellation. Acquisition uses one request-owned budget across
// attempts and checks the shared runtime block before and after reading
// tokens. Parent cancellation ends the request directly; the narrower
// credential deadline becomes a request-scoped stop decision.
//
// Failure classification retains a private credential snapshot for conditional
// recovery. The provider recovery component performs persistence and cache
// cleanup, while this adapter selects the resulting retry or stop action and
// associates a sanitized class with request diagnostics. A fresh token
// returned by recovery wins over the failed observation. Missing token
// configuration and uncertain shared-state updates stop provider switching;
// changed provider eligibility can allow another target.

// RequestCredentials combines token resolution, Grok recovery, and shared
// runtime blocking for requests.
type RequestCredentials struct {
	// shared execution credential resolver
	Source *providercore.OpenAIExecutionCredentials
	// HasGrokTokenSource reports app binding availability. A false value stops
	// Grok OAuth resolution before attempting token acquisition through Source.
	HasGrokTokenSource bool
	// conditional failed-credential recovery
	Recovery *providercore.GrokCredentialRecovery
	// shared credential identity block state
	Runtime *providercore.RuntimeBlockState
}

// CredentialObserver associates credential failure classification with the
// current request diagnostics.
type CredentialObserver interface {
	// ObserveCredentialFailure records the provider ID and sanitized classification
	// for this request. The caller retains private snapshot evidence for conditional
	// state updates; that evidence is excluded from serialized diagnostic data.
	ObserveCredentialFailure(int64, forwardcore.GrokCredentialFailure)
}

// Resolve obtains an execution token and returns its credential kind, applying
// Grok OAuth recovery when required. Nil context becomes Background, nil
// targets fail, and non-Grok targets delegate directly to the configured
// Source.
//
// Grok acquisition respects parent cancellation and the serial request-owned
// CredentialBudget; a nil budget adds no shared deadline. Runtime blocking is
// checked before acquisition and again after success, so an intervening
// credential-state change can reject that token. Recovery can return fresher
// credentials after a stale failure. Remaining errors are classified into
// request, provider, or shared scope and reported through output before a
// sanitized failover error is returned.
// @project-doc docs/interfaces/grok_upstream.md#grok_account_contract
func (s *RequestCredentials) Resolve(ctx context.Context, state *requeststate.CredentialBudget, output CredentialObserver, provider *ExecutionProvider) (string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if provider == nil {
		return "", "", errors.New("provider is nil")
	}
	if !provider.View().IsGrokOAuth() {
		return s.Source.Resolve(ctx, ExecutionRecord(provider))
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if s == nil || !s.HasGrokTokenSource {
		return "", "", credentialFailover(output, provider, forwardcore.GrokCredentialFailure{
			Scope:   forwardcore.GatewayFailureScopeShared,
			Reason:  forwardcore.GrokCredentialReasonProviderConfig,
			Action:  forwardcore.NextProviderStop,
			Message: "Grok OAuth credential provider is unavailable",
		})
	}
	if s.blocked(provider) {
		return "", "", credentialFailover(output, provider, forwardcore.GrokCredentialFailure{
			Scope:   forwardcore.GatewayFailureScopeProvider,
			Reason:  forwardcore.GrokCredentialReasonProviderChanged,
			Action:  forwardcore.NextProviderRetry,
			Message: "Grok OAuth provider is not currently schedulable",
		})
	}

	// Acquire against the same serial request budget so each provider switch spends
	// its remaining allowance. The derived context also retains parent cancellation.
	credentialCtx, cancel, budgetExpired := state.Acquire(ctx)
	// The derived deadline belongs to this acquisition. Cancel it on return so
	// its timer is released while the request budget deadline remains available to later attempts.
	if cancel != nil {
		defer cancel()
	}
	if budgetExpired {
		return "", "", credentialFailover(output, provider, forwardcore.GrokCredentialFailure{
			Scope:   forwardcore.GatewayFailureScopeRequest,
			Reason:  forwardcore.GrokCredentialReasonFailoverTimeout,
			Action:  forwardcore.NextProviderStop,
			Message: "Grok OAuth credential failover budget exhausted",
		})
	}

	token, kind, err := s.Source.Resolve(credentialCtx, ExecutionRecord(provider))
	if err == nil {
		// Credential acquisition can finish after a failed identity was blocked.
		// Recheck shared state before returning a token that may have become ineligible.
		if s.blocked(provider) {
			return "", "", credentialFailover(output, provider, forwardcore.GrokCredentialFailure{
				Scope:   forwardcore.GatewayFailureScopeProvider,
				Reason:  forwardcore.GrokCredentialReasonProviderChanged,
				Action:  forwardcore.NextProviderRetry,
				Message: "Grok OAuth provider is not currently schedulable",
			})
		}
		return token, kind, nil
	}
	if parentErr := ctx.Err(); parentErr != nil {
		return "", "", parentErr
	}
	if credentialCtx.Err() != nil {
		return "", "", credentialFailover(output, provider, forwardcore.GrokCredentialFailure{
			Scope:   forwardcore.GatewayFailureScopeRequest,
			Reason:  forwardcore.GrokCredentialReasonFailoverTimeout,
			Action:  forwardcore.NextProviderStop,
			Message: "Grok OAuth credential failover budget exhausted",
		})
	}

	class := forwardcore.ClassifyGrokCredentialFailure(provider != nil && provider.Record.ProxyID != nil, err)
	if snapshot, ok := providercore.GrokCredentialFailureSnapshot(err); ok {
		class.SetSnapshot(&snapshot)
	}
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	// Conditional recovery compares the failed credential observation with current
	// storage. A fresh token can replace this failure while stale side effects are rejected.
	if class.Permanent || class.Transient {
		freshToken, mutationErr := s.Recovery.Apply(credentialCtx, ExecutionRecord(provider), credentialMutation(class))
		if freshToken != "" {
			return freshToken, "oauth", nil
		}
		if mutationErr != nil {
			if ctx.Err() != nil {
				return "", "", ctx.Err()
			}
			if credentialCtx.Err() != nil {
				return "", "", credentialFailover(output, provider, forwardcore.GrokCredentialFailure{
					Scope:   forwardcore.GatewayFailureScopeRequest,
					Reason:  forwardcore.GrokCredentialReasonFailoverTimeout,
					Action:  forwardcore.NextProviderStop,
					Message: "Grok OAuth credential failover budget exhausted",
				})
			}
			if errors.Is(mutationErr, providercore.ErrRefreshProviderStateChanged) {
				class = forwardcore.GrokCredentialFailure{
					Scope:   forwardcore.GatewayFailureScopeProvider,
					Reason:  forwardcore.GrokCredentialReasonProviderChanged,
					Action:  forwardcore.NextProviderRetry,
					Message: "Grok OAuth provider eligibility changed",
				}
			} else if errors.Is(mutationErr, providercore.ErrRefreshProviderRereadFailed) {
				class = forwardcore.GrokCredentialFailure{
					Scope:   forwardcore.GatewayFailureScopeShared,
					Reason:  forwardcore.GrokCredentialReasonProviderDown,
					Action:  forwardcore.NextProviderStop,
					Message: "Grok OAuth provider state is temporarily unavailable",
				}
			} else {
				class = forwardcore.GrokCredentialFailure{
					Scope:   forwardcore.GatewayFailureScopeShared,
					Reason:  forwardcore.GrokCredentialReasonStateUpdate,
					Action:  forwardcore.NextProviderStop,
					Message: "Grok OAuth provider state could not be updated safely",
				}
			}
		}
	}
	return "", "", credentialFailover(output, provider, class)
}

// credentialFailover reports credential classification through the optional
// observer and creates a sanitized provider-auth failover error. provider must
// be nonnil when observing; the class supplies scope, reason, and
// next-provider action, while client text stays generic.
func credentialFailover(output CredentialObserver, provider *ExecutionProvider, class forwardcore.GrokCredentialFailure) error {
	if strings.TrimSpace(class.Message) == "" {
		class.Message = "Grok OAuth credentials are unavailable"
	}
	if output != nil {
		output.ObserveCredentialFailure(provider.Record.ID, class)
	}
	return &forwardcore.UpstreamFailoverError{
		Stage:  forwardcore.GatewayFailureStageProviderAuth,
		Scope:  class.Scope,
		Reason: class.Reason, NextProviderAction: class.Action, ClientStatusCode: http.StatusServiceUnavailable,
		ClientMessage: forwardcore.GrokCredentialUnavailableClientMessage,
	}
}

// credentialMutation transfers classification and comparison evidence into a
// provider recovery mutation.
func credentialMutation(class forwardcore.GrokCredentialFailure) providercore.GrokCredentialMutation {
	return providercore.GrokCredentialMutation{
		Permanent:     class.Permanent,
		Transient:     class.Transient,
		VerifyMissing: class.Reason == forwardcore.GrokCredentialReasonMissing,
		VerifyProxy:   class.Reason == forwardcore.GrokCredentialReasonProxyInvalid,
		Reason:        string(class.Reason),
		Snapshot:      class.Snapshot(),
	}
}

// blocked checks the runtime block against the provider ID and current
// credential identity. Nil service or target returns false, and Runtime owns
// missing-state handling and identity-mismatch recovery.
func (s *RequestCredentials) blocked(value *ExecutionProvider) bool {
	if s == nil || value == nil {
		return false
	}
	return s.Runtime.Blocked(value.Record.ID, func() string { return providercore.RefreshCredentialIdentity(ExecutionRecord(value)) })
}
