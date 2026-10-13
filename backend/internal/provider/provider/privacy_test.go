package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// privacyIdentityWriter models synchronized credential changes during a local
// privacy query.
type privacyIdentityWriter struct {
	// mu protects current during the HTTP fixture's credential replacement and
	// conditional privacy update. The test acquires the same lock before
	// inspecting the final stored mode.
	mu      sync.Mutex          // fixture provider access lock
	current providercore.Record // current privacy observation identity
}

// privacyProxyReader forces proxy lookup failure for privacy request
// eligibility tests.
type privacyProxyReader struct {
	egress.ProxyRepository // unimplemented proxy repository methods
}

// privacyProviderWriter counts conditional privacy writes attempted by the
// service under test.
type privacyProviderWriter struct {
	writes atomic.Int32 // attempted privacy write count
}

// TestEnsureOpenAIPrivacySkipsShadow supplies a shadow with an access token so
// eligibility reaches the shadow rule. The privacy client factory must remain
// unused and Ensure must return an empty mode.
func TestEnsureOpenAIPrivacySkipsShadow(t *testing.T) {
	pid := int64(100)
	shadow := &providercore.Record{
		ID:               200,
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		ParentProviderID: &pid,

		Credentials: map[string]any{"access_token": "shadow-passthrough-token"},
	}
	privacyCalled := false
	svc := providercore.NewPrivacyService(nil, nil, PrivacyOptions(func(proxyURL string) (*req.Client, error) {
		privacyCalled = true
		return nil, errors.New("should not reach factory for shadow provider")
	}, openai.PrivacyEndpoints{}))
	got := svc.EnsureOpenAIPrivacy(context.Background(), shadow)
	require.Equal(t, "", got)
	require.False(t, privacyCalled, "privacyClientFactory 不应被影子提供商触发")
}

// TestAdminService_EnsureOpenAIPrivacy_RetriesNonSuccessModes starts with
// failed or Cloudflare-blocked privacy state. Ensure must attempt the factory
// once and return failed state when that factory reports an error.
func TestAdminService_EnsureOpenAIPrivacy_RetriesNonSuccessModes(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{openai.PrivacyModeFailed, openai.PrivacyModeCFBlocked} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			privacyCalls := 0
			factory := func(proxyURL string) (*req.Client, error) {
				privacyCalls++
				return nil, errors.New("factory failed")
			}

			provider := &providercore.Record{
				ID:       101,
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
				Credentials: map[string]any{
					"access_token": "token-1",
				},
				Extra: map[string]any{
					"privacy_mode": mode,
				},
			}

			svc := providercore.NewPrivacyService(&privacyIdentityWriter{current: *provider}, nil, PrivacyOptions(factory, openai.PrivacyEndpoints{}))
			got := svc.EnsureOpenAIPrivacy(context.Background(), provider)

			require.Equal(t, openai.PrivacyModeFailed, got)
			require.Equal(t, 1, privacyCalls)
		})
	}
}

// TestTokenRefreshService_ensureOpenAIPrivacy_RetriesNonSuccessModes refreshes
// failed or Cloudflare-blocked privacy state. Refresh must attempt the privacy
// factory once rather than treating those stored modes as successful
// completion.
func TestTokenRefreshService_ensureOpenAIPrivacy_RetriesNonSuccessModes(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{openai.PrivacyModeFailed, openai.PrivacyModeCFBlocked} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			privacyCalls := 0
			factory := func(proxyURL string) (*req.Client, error) {
				privacyCalls++
				return nil, errors.New("factory failed")
			}

			provider := &providercore.Record{
				ID:       202,
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
				Credentials: map[string]any{
					"access_token": "token-2",
				},
				Extra: map[string]any{
					"privacy_mode": mode,
				},
			}

			svc := providercore.NewPrivacyService(&privacyIdentityWriter{current: *provider}, nil, PrivacyOptions(factory, openai.PrivacyEndpoints{}))
			svc.RefreshOpenAIPrivacy(context.Background(), provider)

			require.Equal(t, 1, privacyCalls)
		})
	}
}

// TestPrivacyObservationDoesNotOverwriteNewIdentity replaces credentials during
// the settings request while keeping a newer stored privacy mode. Conditional
// persistence must reject the delayed observation and retain that newer mode.
func TestPrivacyObservationDoesNotOverwriteNewIdentity(t *testing.T) {
	initial := &providercore.Record{ID: 72, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Credentials: map[string]any{"access_token": "original"}, Extra: map[string]any{"privacy_mode": "old"}}
	writer := &privacyIdentityWriter{current: *initial}
	writer.current.Extra = map[string]any{"privacy_mode": "new-identity-mode"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Replace credentials while the settings response is being served, using the
		// writer's lock so the later comparison sees the administrator identity.
		writer.mu.Lock()
		writer.current.Credentials = map[string]any{"access_token": "admin-new"}
		writer.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	svc := providercore.NewPrivacyService(writer, nil, PrivacyOptions(func(string) (*req.Client, error) { return req.C(), nil }, openai.PrivacyEndpoints{Settings: server.URL}))
	svc.ForceOpenAIPrivacy(context.Background(), initial)
	writer.mu.Lock()
	defer writer.mu.Unlock()
	require.Equal(t, "new-identity-mode", writer.current.Extra["privacy_mode"])
}

// TestPrivacyProxyLookupFailureDoesNotConnectDirectly exercises Ensure and
// Force with a proxy reader that fails. Neither path may contact the platform
// directly or publish a successful privacy write.
func TestPrivacyProxyLookupFailureDoesNotConnectDirectly(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "ensure", true: "force"}[force], func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			writer := &privacyProviderWriter{}
			svc := providercore.NewPrivacyService(writer, privacyProxyReader{}, PrivacyOptions(func(string) (*req.Client, error) { return req.C().SetTimeout(time.Second), nil }, openai.PrivacyEndpoints{Settings: server.URL}))
			id := int64(99)
			value := &providercore.Record{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, ProxyID: &id, Credentials: map[string]any{"access_token": "test-token"}}
			if force {
				svc.ForceOpenAIPrivacy(context.Background(), value)
			} else {
				svc.EnsureOpenAIPrivacy(context.Background(), value)
			}
			require.Zero(t, calls.Load(), "代理回源失败不得连接平台")
			require.Zero(t, writer.writes.Load())
		})
	}
}

// TestRefreshPrivacyProxyLookupFailureDoesNotConnectDirectly exercises
// background refresh with a failing or absent required proxy reader. The
// platform request and conditional privacy writer must both remain unused.
func TestRefreshPrivacyProxyLookupFailureDoesNotConnectDirectly(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "lookup_failure", true: "missing_reader"}[missing], func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			writer := &privacyProviderWriter{}
			var proxies egress.ProxyRepository = privacyProxyReader{}
			if missing {
				proxies = nil
			}
			svc := providercore.NewPrivacyService(writer, proxies, PrivacyOptions(func(string) (*req.Client, error) { return req.C().SetTimeout(time.Second), nil }, openai.PrivacyEndpoints{Settings: server.URL}))
			id := int64(99)
			value := &providercore.Record{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, ProxyID: &id, Credentials: map[string]any{"access_token": "test-token"}}
			svc.RefreshOpenAIPrivacy(context.Background(), value)
			require.Zero(t, calls.Load(), "后台刷新不得绕过显式代理")
			require.Zero(t, writer.writes.Load())
		})
	}
}

// UpdatePrivacyModeIfUnchanged compares the current provider identity and
// writes its privacy mode under one mutex. A credential-version mismatch
// returns false without changing Extra, while a match records the supplied
// mode.
//
// The local server can replace credentials before the query returns, so
// comparison and mutation stay in the same critical section as that
// replacement. This fixture protects the stale-observation test from accepting
// a response for credentials that no longer own the stored privacy state.
func (w *privacyIdentityWriter) UpdatePrivacyModeIfUnchanged(_ context.Context, v providercore.UsageObservationVersion, mode string) (bool, error) {
	// Keep identity comparison and Extra mutation under the same lock as the HTTP
	// fixture replacement, preventing a successful write for a superseded
	// credential version.
	w.mu.Lock()
	defer w.mu.Unlock()
	if !providercore.MatchesCredentialVersion(&w.current, v.CredentialVersion) {
		return false, nil
	}
	w.current.Extra["privacy_mode"] = mode
	return true, nil
}

// GetByID returns the fixture's forced error for every proxy lookup.
func (privacyProxyReader) GetByID(context.Context, int64) (*egress.Proxy, error) {
	return nil, errors.New("forced proxy lookup failure")
}

// UpdatePrivacyModeIfUnchanged counts an attempted privacy write and reports
// success for any supplied version. Proxy eligibility tests expect this
// unconditional fixture callback to remain unused when lookup fails.
func (r *privacyProviderWriter) UpdatePrivacyModeIfUnchanged(context.Context, providercore.UsageObservationVersion, string) (bool, error) {
	r.writes.Add(1)
	return true, nil
}
