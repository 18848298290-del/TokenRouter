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

// grokCredentialStateWriter supplies the proxy-invalid reason for conditional credential state updates.
type grokCredentialStateWriter struct {
	store *providerpostgres.ProviderStore
}

// provideGrokCredentialRecovery binds the shared provider store, runtime blocks, and token source.
func provideGrokCredentialRecovery(store *providerpostgres.ProviderStore, tokens *provider.GrokTokenSource, blocks *provider.RuntimeBlockState) *provider.GrokCredentialRecovery {
	return &provider.GrokCredentialRecovery{
		Read:       store.GetByID,
		State:      grokCredentialStateWriter{store: store},
		Invalidate: tokens.InvalidateToken,
		Runtime:    blocks,
		Warn:       slog.Warn,
	}
}

func provideRequestCredentials(source *provider.OpenAIExecutionCredentials, tokens *provider.GrokTokenSource, recovery *provider.GrokCredentialRecovery, blocks *provider.RuntimeBlockState) *gatewayadapter.RequestCredentials {
	return &gatewayadapter.RequestCredentials{Source: source, HasGrokTokenSource: tokens != nil, Recovery: recovery, Runtime: blocks}
}

func provideRequestCredentialExecutor(runtime *gatewayadapter.RequestCredentials) *gatewayhttp.RequestCredentialExecutor {
	return &gatewayhttp.RequestCredentialExecutor{Runtime: runtime}
}

func (s grokCredentialStateWriter) SetGrokCredentialErrorIfMatch(ctx context.Context, id int64, snapshot provider.CredentialMutationSnapshot, reason string) (bool, error) {
	return s.store.SetGrokCredentialErrorIfMatch(ctx, id, snapshot, reason, string(forward.GrokCredentialReasonProxyInvalid))
}

func (s grokCredentialStateWriter) SetGrokCredentialTempUnschedulableIfMatch(ctx context.Context, id int64, snapshot provider.CredentialMutationSnapshot, until time.Time, reason string) (bool, error) {
	return s.store.SetGrokCredentialTempUnschedulableIfMatch(ctx, id, snapshot, until, reason)
}
