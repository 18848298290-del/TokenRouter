//go:build integration

package provider_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// Claude refresh may overlap an administrator's replacement of OAuth
// credentials. A local HTTP token endpoint holds the exchange response until
// the test either commits new credentials or leaves the original identity in
// place.
//
// The production authorization, refresh coordinator, PostgreSQL store, and
// Redis token cache complete the suspended request together. Each case expects
// one exchange and compares the returned token with both persisted and cached
// credentials. The changed identity returns the administrator token, while the
// unchanged identity stores the exchanged token. A second token-source call
// checks that the completed state is reused without another HTTP exchange.

// TestClaudeTokenRefreshUsesOriginalCAS holds a Claude token response while an
// administrator may commit replacement credentials. One exchange yields
// matching returned, persisted, and cached tokens, and a second token-source
// call reuses the completed result.
func TestClaudeTokenRefreshUsesOriginalCAS(t *testing.T) {
	for _, adminChange := range []bool{false, true} {
		t.Run(fmt.Sprintf("administrator=%v", adminChange), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			client := testEntClient(t)
			row, err := client.Provider.Create().SetName(fmt.Sprintf("test-claude-%d", time.Now().UnixNano())).SetPlatform(capability.PlatformAnthropic).SetType(capability.ProviderTypeOAuth).SetCredentials(map[string]any{"access_token": "expired", "refresh_token": "original", "expires_at": time.Now().Add(-time.Hour).Unix()}).Save(ctx)
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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			native := anthropic.NewOAuthClient()
			native.TokenURL = server.URL
			oauth := provider.NewClaudeAuthorization(native, provideradapter.ClaudeAuthorizationOptions(nil))
			defer func() { require.NoError(t, oauth.StopContext(context.Background())) }()
			cache := rediscache.NewOAuthTokenCache(rediscontainer.New(t))
			store := repo
			refresh := provider.NewOAuthRefreshAPI(store, cache, provider.RefreshOptions{
				Now: time.Now, Warn: slog.Warn, Info: slog.Info, Error: slog.Error,
				Platform: provider.ProviderRefreshPlatformPolicy(),
			})
			t.Cleanup(func() { require.NoError(t, refresh.StopContext(context.Background())) })
			executor := &provider.ClaudeTokenRefresher{Authorization: oauth}
			tokenSource := &provider.ClaudeTokenSource{Options: provider.ClaudeTokenOptions{
				Repository: store, Cache: cache, Policy: provider.ClaudeProviderRefreshPolicy(),
				Debug: slog.Debug, Warn: slog.Warn,
				Refresh: func(ctx context.Context, record *provider.Record, window time.Duration) (*provider.OAuthRefreshResult, error) {
					return refresh.RefreshIfNeeded(ctx, record, executor, window)
				},
			}}
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
			cached, err := cache.GetAccessToken(ctx, provider.ClaudeTokenCacheKey(provider.CloneRecord(value)))
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
