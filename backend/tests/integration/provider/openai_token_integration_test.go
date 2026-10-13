//go:build integration

package provider_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// OpenAI token refresh may finish after the provider's OAuth credentials have
// been replaced. A local TLS endpoint pauses the encoded exchange while
// PostgreSQL either retains the observed credentials or commits an
// administrator token.
//
// The fixture injects the TLS client into production authorization and uses the
// shared refresh coordinator with Redis. Each subtest expects one exchange and
// compares the returned access token with both storage and cache, then makes
// another token-source call to check reuse. The transport redirects the
// endpoint while retaining the constructed request path, and the authorization
// wrapper selects the TLS-capable refresh path.

// openAILocalTransport redirects constructed OAuth requests to a local TLS HTTP
// client.
type openAILocalTransport struct {
	client *http.Client // local TLS HTTP client
	target *url.URL     // redirected token endpoint
}

// openAITLSClient selects the production OAuth client's TLS-capable refresh
// request path.
type openAITLSClient struct {
	*openai.OAuthClient // production OAuth request encoder
}

// TestOpenAITokenRefreshUsesOriginalCAS pauses a TLS-backed OpenAI refresh
// while an administrator may replace OAuth credentials. The resulting token
// matches PostgreSQL and Redis after one exchange, and a second call reuses it.
func TestOpenAITokenRefreshUsesOriginalCAS(t *testing.T) {
	for _, adminChange := range []bool{false, true} {
		t.Run(fmt.Sprintf("administrator=%v", adminChange), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			client := testEntClient(t)
			row, err := client.Provider.Create().SetName(fmt.Sprintf("test-openai-%d", time.Now().UnixNano())).SetPlatform(capability.PlatformOpenAI).SetType(capability.ProviderTypeOAuth).SetCredentials(map[string]any{"access_token": "expired", "refresh_token": "original", "expires_at": time.Now().Add(-time.Hour).Unix()}).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
			repo := newProviderStoreContract(client, integrationDB, nil)
			value, err := repo.GetByID(ctx, row.ID)
			require.NoError(t, err)
			// Gate the HTTP token response after exchange entry so the administrator
			// update can commit before production refresh compares the returned credentials.
			entered, resume := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			var resumeOnce sync.Once
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Drain input before the exchange gate; the read outcome is discarded
				// because this fixture uses no body data to choose its token response.
				_, _ = io.Copy(io.Discard, r.Body)
				if calls.Add(1) == 1 {
					close(entered)
				}
				select {
				case <-resume:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "application/json")
				// Token-source completion and comparisons determine the asserted result;
				// this fixture discards the writer's return values after attempting fixed JSON.
				_, _ = io.WriteString(w, `{"access_token":"refreshed","refresh_token":"rotated","token_type":"Bearer","expires_in":3600,"scope":"user:inference"}`)
			}))
			// Register release after server.Close so deferred calls unblock the token
			// handler first, allowing server shutdown even when a later assertion fails.
			defer server.Close()
			defer resumeOnce.Do(func() { close(resume) })
			target, err := url.Parse(server.URL)
			require.NoError(t, err)
			native := openai.NewOAuthClient(openAILocalTransport{client: server.Client(), target: target})
			oauth := provider.NewOpenAIAuthorization(provider.NewOpenAISessionStore(), provideradapter.OpenAIAuthorizationOptions(&provideradapter.OpenAIAuthorizationDependencies{Client: openAITLSClient{OAuthClient: native}}))
			t.Cleanup(func() { require.NoError(t, oauth.StopContext(context.Background())) })
			cache := rediscache.NewOAuthTokenCache(rediscontainer.New(t))
			store := repo
			refresh := provider.NewOAuthRefreshAPI(store, cache, provider.RefreshOptions{
				Now: time.Now, Warn: slog.Warn, Info: slog.Info, Error: slog.Error,
				Platform: provider.ProviderRefreshPlatformPolicy(),
			})
			t.Cleanup(func() { require.NoError(t, refresh.StopContext(context.Background())) })
			executor := &provider.OpenAITokenRefresher{Authorization: oauth}
			tokenSource := &provider.OpenAITokenSource{
				Repository: store, Cache: cache, SetError: store.SetError,
				Metrics: &provider.OpenAITokenMetricsStore{}, Policy: provider.OpenAIProviderRefreshPolicy(),
				Debug: slog.Debug, Warn: slog.Warn,
				Refresh: func(ctx context.Context, record *provider.Record, window time.Duration) (*provider.OAuthRefreshResult, error) {
					return refresh.RefreshIfNeeded(ctx, record, executor, window)
				},
			}
			// result carries one token-source completion through the buffered result channel.
			type result struct {
				token string // returned access token
				err   error  // returned token-source error
			}
			done := make(chan result, 1)
			go func() {
				token, err := tokenSource.GetAccessToken(ctx, provider.CloneRecord(value))
				done <- result{token, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("原生交换未进入")
			}
			expected := "refreshed"
			if adminChange {
				expected = "administrator"
				require.NoError(t, repo.UpdateCredentials(ctx, row.ID, map[string]any{"access_token": expected, "refresh_token": "administrator-refresh", "expires_at": time.Now().Add(time.Hour).Unix()}))
			}
			resumeOnce.Do(func() { close(resume) })
			select {
			case got := <-done:
				require.NoError(t, got.err)
				require.Equal(t, expected, got.token)
			case <-ctx.Done():
				t.Fatal("刷新未收尾")
			}
			require.EqualValues(t, 1, calls.Load())
			persisted, err := repo.GetByID(ctx, row.ID)
			require.NoError(t, err)
			require.Equal(t, expected, persisted.GetCredential("access_token"))
			cached, err := cache.GetAccessToken(ctx, provider.OpenAITokenCacheKey(provider.CloneRecord(value)))
			require.NoError(t, err)
			require.Equal(t, expected, cached)
			// Count exchanges across a second read of the persisted provider to
			// verify that the completed refresh result satisfies the token-source cache path.
			token, err := tokenSource.GetAccessToken(ctx, provider.CloneRecord(persisted))
			require.NoError(t, err)
			require.Equal(t, expected, token)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

// DoWithTLS clones an encoded OAuth request and redirects its scheme and host
// to the local TLS server. The fixture client executes it; its caller owns the
// response body and request cancellation.
func (t openAILocalTransport) DoWithTLS(request *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	copyRequest := request.Clone(request.Context())
	copyRequest.URL.Scheme = t.target.Scheme
	copyRequest.URL.Host = t.target.Host
	return t.client.Do(copyRequest)
}

// RefreshTokenWithClientID delegates refresh to the embedded OAuth client with
// a nonnil TLS profile. That option selects the injected TLS transport; the
// token, proxy, client ID, and context pass through to production encoding.
// Supplied request options are replaced by the fixture's TLS option.
func (c openAITLSClient) RefreshTokenWithClientID(ctx context.Context, token, proxy, clientID string, _ ...openai.OAuthTokenRequestOptions) (*openai.TokenResponse, error) {
	return c.OAuthClient.RefreshTokenWithClientID(ctx, token, proxy, clientID, openai.OAuthTokenRequestOptions{TLSProfile: &tlsfingerprint.Profile{}})
}
