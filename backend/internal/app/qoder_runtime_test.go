package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// TestNewQoderGatewayServiceUsesInjectedRefreshAPI checks that refresh and execution share the injected session cache.
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
