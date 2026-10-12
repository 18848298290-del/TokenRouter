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

// vertexCancelCache cancels the call during lock acquisition, before the cache operation returns.
type vertexCancelCache struct {
	providercore.AccessTokenCache
	cancel context.CancelFunc
	reads  int
}

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

func (c *vertexCancelCache) GetAccessToken(context.Context, string) (string, error) {
	c.reads++
	if c.reads > 1 {
		return "peer-token", nil
	}
	return "", nil
}

func (c *vertexCancelCache) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	c.cancel()
	return false, nil
}
