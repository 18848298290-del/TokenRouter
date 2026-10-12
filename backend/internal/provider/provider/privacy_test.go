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

// privacyIdentityWriter models identity changes during a local HTTP privacy query.
type privacyIdentityWriter struct {
	mu      sync.Mutex
	current providercore.Record
}

type privacyProxyReader struct{ egress.ProxyRepository }

type privacyProviderWriter struct {
	writes atomic.Int32
}

// TestEnsureOpenAIPrivacySkipsShadow verifies that shadow providers skip the privacy client factory.
// Supply a nonempty access_token so the request reaches the shadow check instead of
// returning early for missing credentials.
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

func TestPrivacyObservationDoesNotOverwriteNewIdentity(t *testing.T) {
	initial := &providercore.Record{ID: 72, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Credentials: map[string]any{"access_token": "original"}, Extra: map[string]any{"privacy_mode": "old"}}
	writer := &privacyIdentityWriter{current: *initial}
	writer.current.Extra = map[string]any{"privacy_mode": "new-identity-mode"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// TestPrivacyProxyLookupFailureDoesNotConnectDirectly rejects direct requests and success writes after proxy
// lookup fails.
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

// TestRefreshPrivacyProxyLookupFailureDoesNotConnectDirectly checks that background refresh honors required proxies.
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

// UpdatePrivacyModeIfUnchanged models PostgreSQL identity checks and skips writes after a conflict.
func (w *privacyIdentityWriter) UpdatePrivacyModeIfUnchanged(_ context.Context, v providercore.UsageObservationVersion, mode string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !providercore.MatchesCredentialVersion(&w.current, v.CredentialVersion) {
		return false, nil
	}
	w.current.Extra["privacy_mode"] = mode
	return true, nil
}

func (privacyProxyReader) GetByID(context.Context, int64) (*egress.Proxy, error) {
	return nil, errors.New("forced proxy lookup failure")
}

func (r *privacyProviderWriter) UpdatePrivacyModeIfUnchanged(context.Context, providercore.UsageObservationVersion, string) (bool, error) {
	r.writes.Add(1)
	return true, nil
}
