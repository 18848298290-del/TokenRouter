package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/vertex"
)

// Vertex lock contention can coincide with cancellation before a token exchange.
// These cases exercise vertex_credentials.go with a cancelling cache, stored
// service-account JSON, and partially loaded proxies. They check cancellation
// errors, parsed key defaults, and the URL selected for the exchange.

// vertexCancelCache exposes a peer token after cancelling the first lock attempt.
type vertexCancelCache struct {
	// AccessTokenCache leaves unconfigured operations on a nil interface. This
	// cancellation case uses the read and lock methods below; other methods panic.
	providercore.AccessTokenCache                    // unused cache-method substitute
	cancel                        context.CancelFunc // lock-triggered request cancellation
	reads                         int                // serial token-read count
}

// TestVertexLockWaitCancellation cancels the request as a competing refresh lock
// is reported. It checks that token acquisition returns context.Canceled and an
// empty token despite a peer token available on a later read.
func TestVertexLockWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache := &vertexCancelCache{cancel: cancel}
	a := &providercore.Record{ID: 99, Type: capability.ProviderTypeServiceAccount, Platform: capability.PlatformGemini, Credentials: map[string]any{"service_account_json": `{"type":"service_account","project_id":"fixture","private_key_id":"fixture","private_key":"unused-key","client_email":"fixture@example.invalid"}`}}
	started := time.Now()
	token, err := VertexServiceAccountAccessToken(ctx, cache, a)
	if !errors.Is(err, context.Canceled) || token != "" {
		t.Fatalf("canceled lock wait continued %v and returned token=%q err=%v; cache reads=%d", time.Since(started), token, err, cache.reads)
	}
}

// TestParseVertexServiceAccountKey supplies stored service-account JSON with no
// token URI. It checks project, email and private-key parsing together with the
// platform default token endpoint in the parsed key.
func TestParseVertexServiceAccountKey(t *testing.T) {
	raw := `{
		"type": "service_account",
		"project_id": "vertex-proj",
		"private_key_id": "kid",
		"private_key": "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n",
		"client_email": "svc@vertex-proj.iam.gserviceaccount.com"
	}`
	provider := &providercore.Record{
		Type:     capability.ProviderTypeServiceAccount,
		Platform: capability.PlatformGemini,
		Credentials: map[string]any{
			"service_account_json": raw,
		},
	}
	key, err := ParseVertexServiceAccountKey(provider)
	require.NoError(t, err)
	require.Equal(t, "vertex-proj", key.ProjectID)
	require.Equal(t, "svc@vertex-proj.iam.gserviceaccount.com", key.ClientEmail)
	require.Equal(t, vertex.DefaultTokenURL, key.TokenURI)
	require.True(t, strings.Contains(key.PrivateKey, "BEGIN PRIVATE KEY"))
}

// TestVertexServiceAccountProxyURL compares a fully loaded proxy with records
// missing either its ID or object. It checks the formatted loaded URL and an
// empty result for either incomplete binding.
func TestVertexServiceAccountProxyURL(t *testing.T) {
	proxyID := int64(7)
	provider := &providercore.Record{
		ProxyID: &proxyID,
		Proxy: &egress.Proxy{
			Protocol: "http",
			Host:     "proxy.example.com",
			Port:     8080,
		},
	}

	require.Equal(t, "http://proxy.example.com:8080", vertexServiceAccountProxyURL(provider))
	require.Empty(t, vertexServiceAccountProxyURL(&providercore.Record{Proxy: provider.Proxy}))
	require.Empty(t, vertexServiceAccountProxyURL(&providercore.Record{ProxyID: &proxyID}))
}

// GetAccessToken counts serial reads and returns empty on the first call.
// Later calls return peer-token with no error; context and key are ignored by
// this cancellation fixture.
func (c *vertexCancelCache) GetAccessToken(context.Context, string) (string, error) {
	c.reads++
	if c.reads > 1 {
		return "peer-token", nil
	}
	return "", nil
}

// AcquireRefreshLock cancels the request and reports that another refresher holds
// the lock. Tests use this synchronous cancellation to exercise the coordinator's
// contention wait before another cache read.
func (c *vertexCancelCache) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	// Cancel before reporting contention so the coordinator enters its wait with
	// cancellation already ready, exposing an unintended peer-token reread
	// after cancellation.
	c.cancel()
	return false, nil
}
