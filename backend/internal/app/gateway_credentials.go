package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayadapter "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
)

// gateway_credentials.go constructs gateway credential resolution and connects
// Grok credential recovery to conditional provider mutations.
// Grok request failures can arrive after credentials or proxy settings have
// changed. Recovery uses the shared provider store for conditional mutations
// and token invalidation, passing the proxy-invalid reason that selects the
// store's additional proxy-existence predicate.

// grokCredentialStateWriter adapts conditional Grok credential containment to
// provider storage.
type grokCredentialStateWriter struct {
	store *providerpostgres.ProviderStore // conditional provider mutations
}

// provideGrokCredentialRecovery binds stored identity reads, conditional
// containment, and token invalidation. store and tokens supply required method
// callbacks, while the runtime-block service is passed through for request-time
// recovery decisions.
func provideGrokCredentialRecovery(store *providerpostgres.ProviderStore, tokens *provider.GrokTokenSource, blocks *provider.RuntimeBlockState) *provider.GrokCredentialRecovery {
	return &provider.GrokCredentialRecovery{
		Read:       store.GetByID,
		State:      grokCredentialStateWriter{store: store},
		Invalidate: tokens.InvalidateToken,
		Runtime:    blocks,
		Warn:       slog.Warn,
	}
}

// provideRequestCredentials combines OpenAI credential lookup and Grok recovery
// dependencies. HasGrokTokenSource records whether tokens is present; the
// request-credential runtime uses that flag when resolving Grok execution
// credentials.
func provideRequestCredentials(source *provider.OpenAIExecutionCredentials, tokens *provider.GrokTokenSource, recovery *provider.GrokCredentialRecovery, blocks *provider.RuntimeBlockState) *gatewayadapter.RequestCredentials {
	return &gatewayadapter.RequestCredentials{Source: source, HasGrokTokenSource: tokens != nil, Recovery: recovery, Runtime: blocks}
}

// provideRequestCredentialExecutor wraps request credential resolution for HTTP
// execution consumers.
func provideRequestCredentialExecutor(runtime *gatewayadapter.RequestCredentials) *gatewayhttp.RequestCredentialExecutor {
	return &gatewayhttp.RequestCredentialExecutor{Runtime: runtime}
}

// SetGrokCredentialErrorIfMatch delegates containment using the observed
// credentials and proxy identity. The store returns false when its matching
// conditions fail and receives the proxy-invalid reason for its additional
// missing-proxy check.
func (s grokCredentialStateWriter) SetGrokCredentialErrorIfMatch(ctx context.Context, id int64, snapshot provider.CredentialMutationSnapshot, reason string) (bool, error) {
	return s.store.SetGrokCredentialErrorIfMatch(ctx, id, snapshot, reason, string(forward.GrokCredentialReasonProxyInvalid))
}

// SetGrokCredentialTempUnschedulableIfMatch delegates temporary containment for
// the observed credential identity. The store chooses its deadline from the
// current and supplied values, and false reports that the conditional update
// did not apply.
func (s grokCredentialStateWriter) SetGrokCredentialTempUnschedulableIfMatch(ctx context.Context, id int64, snapshot provider.CredentialMutationSnapshot, until time.Time, reason string) (bool, error) {
	return s.store.SetGrokCredentialTempUnschedulableIfMatch(ctx, id, snapshot, until, reason)
}
