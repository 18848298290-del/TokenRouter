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

// openAILocalTransport sends constructed token requests to a local TLS fixture.
type openAILocalTransport struct {
	client *http.Client
	target *url.URL
}

// openAITLSClient exercises production OAuth encoding and parsing through the injectable TLS transport.
type openAITLSClient struct{ *openai.OAuthClient }

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
			entered, resume := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			var resumeOnce sync.Once
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				_, _ = io.WriteString(w, `{"access_token":"refreshed","refresh_token":"rotated","token_type":"Bearer","expires_in":3600,"scope":"user:inference"}`)
			}))
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
			type result struct {
				token string
				err   error
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
			// A second call reads the cached token without another exchange.
			token, err := tokenSource.GetAccessToken(ctx, provider.CloneRecord(persisted))
			require.NoError(t, err)
			require.Equal(t, expected, token)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func (t openAILocalTransport) DoWithTLS(request *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	copyRequest := request.Clone(request.Context())
	copyRequest.URL.Scheme = t.target.Scheme
	copyRequest.URL.Host = t.target.Host
	return t.client.Do(copyRequest)
}

func (c openAITLSClient) RefreshTokenWithClientID(ctx context.Context, token, proxy, clientID string, _ ...openai.OAuthTokenRequestOptions) (*openai.TokenResponse, error) {
	return c.OAuthClient.RefreshTokenWithClientID(ctx, token, proxy, clientID, openai.OAuthTokenRequestOptions{TLSProfile: &tlsfingerprint.Profile{}})
}
