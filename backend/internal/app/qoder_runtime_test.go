package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// Request-side Qoder refresh and execution must refer to the same session
// provider. The scenario places a credential-matched session into that provider
// and asks an execution target for its session, making pointer sharing and the
// injected coordinator visible.

// TestNewQoderGatewayServiceUsesInjectedRefreshAPI assembles refresh and
// execution using one injected token provider. It checks coordinator/provider
// identity and confirms the target returns the same cached session for matching
// credentials, then stops the owned provider in cleanup.
func TestNewQoderGatewayServiceUsesInjectedRefreshAPI(t *testing.T) {
	tokens := provideQoderTokens(nil, nil)
	t.Cleanup(func() { require.NoError(t, tokens.StopContext(context.Background())) })
	coordinator := &provider.OAuthRefreshAPI{}
	refresh := provideQoderRequestRefresh(nil, tokens, coordinator, nil, nil)
	runtime := provideQoderRuntime(tokens, nil, nil, nil, nil)
	require.Same(t, coordinator, refresh.Coordinator)
	require.Same(t, tokens, refresh.Tokens)
	value := &provider.Record{ID: 1, Platform: provider.PlatformQoder, Type: provider.ProviderTypeCosy, Credentials: map[string]any{}}
	session := &qoder.SessionContext{Identity: &qoder.AuthIdentity{SecurityOauthToken: "fixture"}}
	tokens.Core.Sessions[value.ID] = provider.QoderSessionCacheEntry[*qoder.SessionContext]{CredentialsHash: provider.QoderCredentialsHash(value.Credentials), Session: session}
	got, err := runtime.Target(qoder.RequestMetadata{}, value).Session(context.Background())
	require.NoError(t, err)
	require.Same(t, session, got)
}
