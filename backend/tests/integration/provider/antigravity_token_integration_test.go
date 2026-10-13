//go:build integration

package provider_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// Antigravity token refresh can finish after an administrator has replaced the
// provider credentials. The local TLS server suspends the token response while
// PostgreSQL accepts the administrator update, then the token source completes
// through the production authorization and refresh coordinator.
//
// Redis supplies the access-token cache and refresh lock used by this path.
// Both subtests check one token exchange and require the returned token,
// persisted access token, and cached token to agree. The unchanged case expects
// the exchanged token; the changed case expects the administrator token. A
// separate loadCodeAssist response supplies the fixture project and
// subscription metadata used by authorization discovery.

// antigravityFixtureTransport redirects constructed native requests to the
// local TLS fixture.
type antigravityFixtureTransport struct {
	target    *url.URL          // local TLS endpoint
	transport http.RoundTripper // fixture request executor
}

// RoundTrip clones the request and sends it to the local TLS fixture while
// retaining the original path and query. The configured RoundTripper handles
// the response; the caller owns response-body cleanup.
func (f antigravityFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	target := *request.URL
	target.Scheme = f.target.Scheme
	target.Host = f.target.Host
	copy.URL = &target
	copy.Host = f.target.Host
	return f.transport.RoundTrip(copy)
}

// TestAntigravityNativeRefreshUsesOriginalCAS pauses a local native token
// exchange while an administrator may replace credentials. Exactly one exchange
// must leave the returned, persisted, and Redis-cached access tokens equal to
// the winning identity.
func TestAntigravityNativeRefreshUsesOriginalCAS(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("administrator=%v", changed), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			client := testEntClient(t)
			row, err := client.Provider.Create().SetName(fmt.Sprintf("test-antigravity-%d", time.Now().UnixNano())).SetPlatform(capability.PlatformAntigravity).SetType(capability.ProviderTypeOAuth).SetCredentials(map[string]any{"access_token": "expired", "refresh_token": "original", "expires_at": time.Now().Add(-time.Hour).Unix(), "project_id": "fixture-project", "email": "fixture@example.invalid"}).Save(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, client.Provider.DeleteOneID(row.ID).Exec(context.Background())) })
			repo := newProviderStoreContract(client, integrationDB, nil)
			value, err := repo.GetByID(ctx, row.ID)
			require.NoError(t, err)
			// Gate the HTTP token response after exchange entry so the administrator
			// update can commit before production refresh compares the returned credentials.
			entered, resume := make(chan struct{}), make(chan struct{})
			var resumeOnce sync.Once
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					require.NoError(t, r.ParseForm())
					require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
					require.Equal(t, "original", r.Form.Get("refresh_token"))
					if calls.Add(1) == 1 {
						close(entered)
					}
					select {
					case <-resume:
					case <-r.Context().Done():
						return
					}
					w.Header().Set("Content-Type", "application/json")
					// Token-source completion determines the asserted outcome; this handler
					// discards the write result after attempting its fixed exchange JSON.
					_, _ = io.WriteString(w, `{"access_token":"refreshed","refresh_token":"rotated","token_type":"Bearer","expires_in":3600}`)
					return
				}
				if strings.HasSuffix(r.URL.Path, ":loadCodeAssist") {
					// Drain loadCodeAssist input before returning fixed project/subscription
					// metadata; a drain error does not select different data in this fixture.
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "application/json")
					// The fixture discards this metadata write result, leaving the native
					// client to parse whichever project and tier response bytes were delivered.
					_, _ = io.WriteString(w, `{"cloudaicompanionProject":"fixture-project","paidTier":{"id":"g1-pro-tier"}}`)
					return
				}
				t.Errorf("非夹具请求 %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}))
			// Register release after server.Close so deferred calls unblock the token
			// handler first, allowing server shutdown even when a later assertion fails.
			defer server.Close()
			defer resumeOnce.Do(func() { close(resume) })
			localURL, err := url.Parse(server.URL)
			require.NoError(t, err)
			localClient := &http.Client{Transport: antigravityFixtureTransport{target: localURL, transport: server.Client().Transport}, Timeout: 10 * time.Second}
			oauth := providercore.NewAntigravityAuthorization(provideradapter.AntigravityAuthorizationOptions(nil))
			defer func() { require.NoError(t, oauth.StopContext(context.Background())) }()
			oauth.Options.NewClient = func(proxy string) (providercore.AntigravityAuthorizationClient, error) {
				return antigravity.NewClientWithOptions(proxy, antigravity.ClientOptions{HTTPClient: localClient})
			}
			cache := rediscache.NewOAuthTokenCache(rediscontainer.New(t))
			store := repo
			refresh := providercore.NewOAuthRefreshAPI(store, cache, providercore.RefreshOptions{
				Now: time.Now, Warn: slog.Warn, Info: slog.Info, Error: slog.Error,
				Platform: providercore.ProviderRefreshPlatformPolicy(),
			})
			t.Cleanup(func() { require.NoError(t, refresh.StopContext(context.Background())) })
			// Print refresh diagnostics as best effort; the callback discards stdout
			// write errors because those diagnostics do not choose the exchanged credentials.
			executor := &providercore.AntigravityRefreshRules{
				RefreshProviderToken: oauth.RefreshProviderToken, BuildProviderCredentials: oauth.BuildProviderCredentials,
				Printf: func(format string, args ...any) { _, _ = fmt.Printf(format, args...) }, Logf: log.Printf,
			}
			tokenSource := &providercore.AntigravityTokenSource{Options: providercore.AntigravityTokenOptions{
				Repository: store, Cache: cache, Policy: providercore.AntigravityProviderRefreshPolicy(),
				Debug: slog.Debug, Warn: slog.Warn,
				Refresh: func(ctx context.Context, record *providercore.Record, window time.Duration) (*providercore.OAuthRefreshResult, error) {
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
				token, err := tokenSource.GetAccessToken(ctx, providercore.CloneRecord(value))
				done <- result{token, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("原生交换未进入")
			}
			expected := "refreshed"
			if changed {
				expected = "administrator"
				require.NoError(t, repo.UpdateCredentials(ctx, row.ID, map[string]any{"access_token": expected, "refresh_token": "administrator-refresh", "expires_at": time.Now().Add(time.Hour).Unix(), "project_id": "fixture-project", "email": "fixture@example.invalid"}))
			}
			resumeOnce.Do(func() { close(resume) })
			select {
			case got := <-done:
				require.NoError(t, got.err)
				require.Equal(t, expected, got.token)
			case <-ctx.Done():
				t.Fatal("刷新未退出")
			}
			require.EqualValues(t, 1, calls.Load())
			persisted, err := repo.GetByID(ctx, row.ID)
			require.NoError(t, err)
			require.Equal(t, expected, persisted.GetCredential("access_token"))
			cached, err := cache.GetAccessToken(ctx, providercore.AntigravityTokenCacheKey(value))
			require.NoError(t, err)
			require.Equal(t, expected, cached)
			// Remove the asserted token before the Redis fixture shuts down, keeping
			// explicit cache cleanup after the completed token, database, and cache comparisons.
			require.NoError(t, cache.DeleteAccessToken(ctx, providercore.AntigravityTokenCacheKey(value)))
		})
	}
}
