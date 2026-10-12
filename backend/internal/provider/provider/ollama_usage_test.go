package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	upstreamollama "github.com/TokenFlux/TokenRouter/internal/upstream/ollama"
)

const (
	ollamaCloudUsageMaxSessionBytes = 16 * 1024
	ollamaCloudUsageConcurrency     = 4
	ollamaCloudUsageLeaderLockKey   = "ollama:cloud:usage:leader"
)

type ollamaUsageTestEncryptor struct{}

type ollamaUsageTestRepo struct {
	*ollamaUsageRows
	due                 []providercore.Record
	beforeSnapshot      func()
	disableAutoAttempts atomic.Int64
	disableAutoCalls    atomic.Int64
	groupResolveCalls   atomic.Int64
	getByIDCalls        atomic.Int64
}

type ollamaRefreshPreflightIdentityChangeRepo struct {
	*ollamaUsageTestRepo
	getCalls atomic.Int64
}

type ollamaUsageHTTPStub struct {
	status         int
	body           []byte
	header         http.Header
	calls          atomic.Int64
	active         atomic.Int64
	maxActive      atomic.Int64
	beforeResponse func(*http.Request)
	lastRequest    *http.Request
	lastProxyURL   string
	mu             sync.Mutex
}

// ollamaUsageRows holds provider records and returns independent copies.
type ollamaUsageRows struct {
	mu        sync.Mutex
	providers map[int64]*providercore.Record
}

// ollamaUsageSettings implements the key/value operations used by the settings reader.
type ollamaUsageSettings struct {
	settingscore.Repository
	mu     sync.Mutex
	values map[string]string
}

type ollamaUsageTransport interface {
	Do(*http.Request, string, int64, int) (*http.Response, error)
}

// ollamaUsageContract injects a clock and lock fixture into production querying, caching, and
// lifecycle handling.
type ollamaUsageContract struct {
	*providercore.OllamaCloudUsageService
	now       func() time.Time
	lockCache providercore.CNMonitorLeader
}

// ollamaUsageLeader compares lock owners in memory; integration tests cover Redis behavior.
type ollamaUsageLeader struct {
	mu     sync.Mutex
	owners map[string]string
}

// TestOllamaUsageStopOwnsManualRefresh checks that shutdown cancels and waits for manual probes and
// rejects late snapshots.
func TestOllamaUsageStopOwnsManualRefresh(t *testing.T) {
	value := ollamaUsageProvider(901)
	value.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=fixture"
	repo := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{value.ID: value}}}
	entered, cancelled := make(chan struct{}), make(chan struct{})
	upstream := &ollamaUsageHTTPStub{body: ollamaUsageFixture(t), beforeResponse: func(req *http.Request) { close(entered); <-req.Context().Done(); close(cancelled) }}
	svc := newOllamaUsageTestService(t, repo, upstream, &ollamaUsageSettings{}, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := svc.Refresh(ctx, value.ID); done <- err }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("手动查询未退出")
		}
	})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("手动查询未进入 HTTP")
	}
	svc.Stop()
	select {
	case <-cancelled:
	default:
		t.Error("停止没有取消手动查询")
	}
	require.NotContains(t, value.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
}

func TestIsOllamaCloudUsageProviderStrictOfficialHost(t *testing.T) {
	tests := []struct {
		baseURL  string
		platform string
		want     bool
	}{
		{"https://ollama.com", capability.PlatformOpenAI, true},
		{"HTTPS://OLLAMA.COM", capability.PlatformAnthropic, true},
		{"https://www.OLLAMA.com:443/v1", capability.PlatformOpenAI, true},
		{"https://ollama.com:443", capability.PlatformOpenAI, true},
		{"https://ollama.com/", capability.PlatformAnthropic, false},
		{"https://ollama.com/v1/", capability.PlatformOpenAI, false},
		{"http://ollama.com", capability.PlatformOpenAI, false},
		{"https://ollama.com.evil.test", capability.PlatformOpenAI, false},
		{"https://ollama.com:444", capability.PlatformOpenAI, false},
		{"https://user@ollama.com", capability.PlatformOpenAI, false},
		{"https://ollama.com/v2", capability.PlatformOpenAI, false},
		{"https://ollama.com?next=https://evil.test", capability.PlatformOpenAI, false},
		{"https://ollama.com#usage", capability.PlatformOpenAI, false},
	}
	for _, test := range tests {
		t.Run(test.baseURL+test.platform, func(t *testing.T) {
			provider := ollamaUsageProvider(1)
			provider.Platform = test.platform
			provider.Credentials["base_url"] = test.baseURL
			require.Equal(t, test.want, providercore.IsOllamaCloudUsageProvider(provider))
		})
	}
}

func TestOllamaCloudUsageSessionEncryptionFailClosedAndWriteOnlyState(t *testing.T) {
	provider := ollamaUsageProvider(7)
	repo := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{7: provider}}}
	settings := &ollamaUsageSettings{}

	ephemeral := newOllamaUsageTestService(t, repo, &ollamaUsageHTTPStub{}, settings, false)
	_, err := ephemeral.SaveSession(context.Background(), 7, "wos-session=plaintext-secret")
	require.ErrorIs(t, err, providercore.ErrOllamaCloudUsageEncryptionKey)
	require.NotContains(t, provider.Extra, providercore.OllamaCloudUsageSessionExtraKey)

	svc := newOllamaUsageTestService(t, repo, &ollamaUsageHTTPStub{}, settings, true)
	_, err = svc.SaveSession(context.Background(), 7, "tracking=arbitrary-only")
	require.Error(t, err)
	require.NotContains(t, provider.Extra, providercore.OllamaCloudUsageSessionExtraKey)

	state, err := svc.SaveSession(context.Background(), 7, "tracking=must-not-persist; wos-session=plaintext-secret")
	require.NoError(t, err)
	require.True(t, state.Configured)
	stored, ok := provider.Extra[providercore.OllamaCloudUsageSessionExtraKey].(string)
	require.True(t, ok)
	require.Equal(t, "cipher:wos-session=plaintext-secret", stored)
	require.NotContains(t, stored, "tracking")
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "plaintext-secret")
	require.NotContains(t, string(raw), "cipher:")

	provider.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "plaintext-secret"
	_, err = svc.Refresh(context.Background(), 7)
	require.ErrorContains(t, err, "cannot be decrypted")
}

func TestOllamaCloudUsageGroupSharesAcrossPlatformsURLVariantsAndDynamicSiblings(t *testing.T) {
	source := ollamaUsageProvider(71)
	source.Credentials["api_key"] = "shared-key"
	source.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=shared"
	source.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey] = true
	source.Extra[providercore.OllamaCloudUsageSnapshotExtraKey] = &providercore.OllamaCloudUsageSnapshot{
		Status: providercore.OllamaCloudUsageStatusOK,
		Data:   &providercore.OllamaCloudUsageData{Plan: "pro"},
	}
	source.UpdatedAt = time.Now().Add(-time.Minute)
	sibling := ollamaUsageProvider(72)
	sibling.Platform = capability.PlatformAnthropic
	sibling.Credentials = map[string]any{"base_url": "HTTPS://WWW.OLLAMA.COM:443/v1", "api_key": "shared-key"}
	sibling.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=shared"
	sibling.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey] = true
	sibling.UpdatedAt = time.Now()
	different := ollamaUsageProvider(73)
	different.Credentials["api_key"] = "different-key"
	repo := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{
		source.ID: source, sibling.ID: sibling, different.ID: different,
	}}}
	svc := newOllamaUsageTestService(t, repo, &ollamaUsageHTTPStub{}, &ollamaUsageSettings{}, true)

	state, err := svc.GetState(context.Background(), sibling.ID)
	require.NoError(t, err)
	require.True(t, state.Configured)
	require.True(t, state.AutoRefreshEnabled)
	require.Equal(t, "pro", state.Snapshot.Data.Plan)

	differentState, err := svc.GetState(context.Background(), different.ID)
	require.NoError(t, err)
	require.False(t, differentState.Configured)

	newSibling := ollamaUsageProvider(74)
	newSibling.Platform = capability.PlatformAnthropic
	newSibling.Credentials = map[string]any{"base_url": "https://ollama.com:443", "api_key": "shared-key"}
	repo.mu.Lock()
	repo.providers[newSibling.ID] = newSibling
	repo.mu.Unlock()
	newState, err := svc.GetState(context.Background(), newSibling.ID)
	require.NoError(t, err)
	require.True(t, newState.Configured)
	require.Equal(t, state.Snapshot, newState.Snapshot)

	before := repo.groupResolveCalls.Load()
	require.NoError(t, svc.ResolveProviders(context.Background(), []*providercore.Record{source, sibling, different, newSibling}))
	require.Equal(t, before+1, repo.groupResolveCalls.Load(), "one list batch must issue one group lookup")
}

func TestOllamaCloudUsageSaveAutoRefreshAndDeleteAreGroupScoped(t *testing.T) {
	first := ollamaUsageProvider(81)
	first.Credentials["api_key"] = "shared-key"
	second := ollamaUsageProvider(82)
	second.Platform = capability.PlatformAnthropic
	second.Credentials = map[string]any{"base_url": "https://www.ollama.com/v1", "api_key": "shared-key"}
	different := ollamaUsageProvider(83)
	different.Credentials["api_key"] = "different-key"
	repo := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{
		first.ID: first, second.ID: second, different.ID: different,
	}}}
	svc := newOllamaUsageTestService(t, repo, &ollamaUsageHTTPStub{}, &ollamaUsageSettings{}, true)

	state, err := svc.SaveSession(context.Background(), second.ID, "wos-session=shared-browser")
	require.NoError(t, err)
	require.True(t, state.Configured)
	require.Equal(t, "cipher:wos-session=shared-browser", first.Extra[providercore.OllamaCloudUsageSessionExtraKey])
	require.Equal(t, first.Extra[providercore.OllamaCloudUsageSessionExtraKey], second.Extra[providercore.OllamaCloudUsageSessionExtraKey])
	require.NotContains(t, different.Extra, providercore.OllamaCloudUsageSessionExtraKey)

	state, err = svc.SetAutoRefresh(context.Background(), first.ID, true)
	require.NoError(t, err)
	require.True(t, state.AutoRefreshEnabled)
	require.Equal(t, true, first.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey])
	require.Equal(t, true, second.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey])

	state, err = svc.DeleteSession(context.Background(), second.ID)
	require.NoError(t, err)
	require.False(t, state.Configured)
	for _, member := range []*providercore.Record{first, second} {
		require.NotContains(t, member.Extra, providercore.OllamaCloudUsageSessionExtraKey)
		require.NotContains(t, member.Extra, providercore.OllamaCloudUsageAutoRefreshExtraKey)
		require.NotContains(t, member.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
	}
}

func TestOllamaCloudUsageRefreshSingleflightAndRunnerDeduplicateSharedGroup(t *testing.T) {
	first := ollamaUsageProvider(91)
	first.Credentials["api_key"] = "shared-key"
	first.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=shared"
	first.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey] = true
	second := ollamaUsageProvider(92)
	second.Platform = capability.PlatformAnthropic
	second.Credentials = map[string]any{"base_url": "https://www.ollama.com:443/v1", "api_key": "shared-key"}
	second.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=shared"
	second.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey] = true
	repo := &ollamaUsageTestRepo{
		ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{first.ID: first, second.ID: second}},
		due:             []providercore.Record{*first, *second},
	}
	settingsRepo := &ollamaUsageSettings{values: map[string]string{
		providercore.SettingKeyOllamaCloudUsageSettings: `{"enabled":true,"interval_minutes":60}`,
	}}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	upstream := &ollamaUsageHTTPStub{body: ollamaUsageFixture(t), beforeResponse: func(*http.Request) {
		once.Do(func() { close(started) })
		<-release
	}}
	svc := newOllamaUsageTestService(t, repo, upstream, settingsRepo, true)

	errs := make(chan error, 2)
	go func() { _, err := svc.Refresh(context.Background(), first.ID); errs <- err }()
	<-started
	// The first caller has reached the upstream stub after constructing its group key and reading
	// provider state.
	loadsBeforeSecond := repo.getByIDCalls.Load()
	go func() { _, err := svc.Refresh(context.Background(), second.ID); errs <- err }()
	// Wait for the second caller to begin its provider lookup before releasing the first request.
	// Releasing too early could let the second call start a separate refresh and hit the thirty-second
	// manual-refresh limit.
	require.Eventually(t, func() bool {
		return repo.getByIDCalls.Load() > loadsBeforeSecond
	}, 5*time.Second, time.Millisecond, "第二个调用方必须在释放首个请求前到达 singleflight")
	close(release)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.Equal(t, int64(1), upstream.calls.Load())
	require.NotNil(t, providercore.DecodeOllamaCloudUsageSnapshot(first.Extra))
	require.Equal(t, providercore.DecodeOllamaCloudUsageSnapshot(first.Extra), providercore.DecodeOllamaCloudUsageSnapshot(second.Extra))

	delete(first.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
	delete(second.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
	upstream.beforeResponse = nil
	require.NoError(t, svc.RunDue(context.Background()))
	require.Equal(t, int64(2), upstream.calls.Load(), "RunDue must issue one request for the shared group")
}

func TestOllamaCloudUsageRefreshRejectsGroupChangeBeforeUpstreamRequest(t *testing.T) {
	provider := ollamaUsageProvider(94)
	provider.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=secret"
	base := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{provider.ID: provider}}}
	repo := &ollamaRefreshPreflightIdentityChangeRepo{ollamaUsageTestRepo: base}
	upstream := &ollamaUsageHTTPStub{body: ollamaUsageFixture(t)}
	svc := newOllamaUsageContract(repo, upstream, &ollamaUsageSettings{}, ollamaUsageTestEncryptor{}, true)
	t.Cleanup(svc.Stop)

	_, err := svc.Refresh(context.Background(), provider.ID)

	require.ErrorIs(t, err, providercore.ErrOllamaCloudUsageIdentityChanged)
	require.Zero(t, upstream.calls.Load())
	require.NotContains(t, provider.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
}

func TestOllamaCloudUsageRefreshUsesFixedURLCookieAndNoRedirects(t *testing.T) {
	provider := ollamaUsageProvider(8)
	provider.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=browser-secret; tracking=must-not-send"
	repo := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{8: provider}}}
	upstream := &ollamaUsageHTTPStub{body: ollamaUsageFixture(t)}
	svc := newOllamaUsageTestService(t, repo, upstream, &ollamaUsageSettings{}, true)
	fixedNow := time.Date(2026, time.July, 22, 15, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixedNow }

	state, err := svc.Refresh(context.Background(), 8)
	require.NoError(t, err)
	require.Equal(t, providercore.OllamaCloudUsageStatusOK, state.Snapshot.Status)
	require.Equal(t, "https://ollama.com/settings", upstream.lastRequest.URL.String())
	require.Equal(t, "ollama.com", upstream.lastRequest.Host)
	require.Equal(t, "wos-session=browser-secret", upstream.lastRequest.Header.Get("Cookie"))
	require.NotContains(t, upstream.lastRequest.Header.Get("Cookie"), "tracking")
	require.Empty(t, upstream.lastRequest.Header.Get("Authorization"))
	require.True(t, upstreamcore.HTTPUpstreamRedirectsDisabled(upstream.lastRequest.Context()))
}

func TestOllamaCloudUsageManualRefreshUsesShortIndependentInterval(t *testing.T) {
	provider := ollamaUsageProvider(12)
	provider.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=initial"
	repo := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{12: provider}}}
	upstream := &ollamaUsageHTTPStub{body: ollamaUsageFixture(t)}
	svc := newOllamaUsageTestService(t, repo, upstream, &ollamaUsageSettings{}, true)
	fixedNow := time.Date(2026, time.July, 22, 15, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return fixedNow }

	_, err := svc.Refresh(context.Background(), 12)
	require.NoError(t, err)
	_, err = svc.Refresh(context.Background(), 12)
	require.ErrorIs(t, err, providercore.ErrOllamaCloudUsageRefreshRateLimited)
	require.Equal(t, int64(1), upstream.calls.Load())

	// Saving a repaired session clears the snapshot so an administrator can query usage immediately.
	_, err = svc.SaveSession(context.Background(), 12, "wos-session=repaired")
	require.NoError(t, err)
	_, err = svc.Refresh(context.Background(), 12)
	require.NoError(t, err)
	require.Equal(t, int64(2), upstream.calls.Load())
}

func TestOllamaCloudUsageRefreshUsesHydratedProxyIdentity(t *testing.T) {
	provider := ollamaUsageProvider(13)
	provider.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=secret"
	proxyID := int64(4)
	provider.ProxyID = &proxyID
	provider.Proxy = &egress.Proxy{
		ID: proxyID, Protocol: "http", Host: "127.0.0.1", Port: 3128,
		Username: "proxy-user", Password: "proxy-pass", Status: providercore.StatusActive,
	}
	repo := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{13: provider}}}
	upstream := &ollamaUsageHTTPStub{body: ollamaUsageFixture(t)}
	svc := newOllamaUsageTestService(t, repo, upstream, &ollamaUsageSettings{}, true)

	_, err := svc.Refresh(context.Background(), 13)
	require.NoError(t, err)
	require.Equal(t, provider.Proxy.URL(), upstream.lastProxyURL)
}

func TestOllamaCloudUsageRedirectAndBodyLimitArePersistedSafely(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   []byte
		reason string
	}{
		{"redirect", http.StatusFound, nil, "redirect_blocked"},
		{"body limit", http.StatusOK, make([]byte, upstreamollama.MaxBodyBytes+1), "response_too_large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := ollamaUsageProvider(9)
			provider.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=secret"
			repo := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{9: provider}}}
			svc := newOllamaUsageTestService(t, repo, &ollamaUsageHTTPStub{status: test.status, body: test.body}, &ollamaUsageSettings{}, true)
			state, err := svc.Refresh(context.Background(), 9)
			require.NoError(t, err)
			require.Equal(t, providercore.OllamaCloudUsageStatusFailed, state.Snapshot.Status)
			require.Equal(t, test.reason, state.Snapshot.LastError)
		})
	}
}

func TestOllamaCloudUsageRefreshRejectsIdentityChange(t *testing.T) {
	provider := ollamaUsageProvider(10)
	provider.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=secret"
	repo := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{10: provider}}}
	repo.beforeSnapshot = func() { provider.Credentials["api_key"] = "rotated" }
	svc := newOllamaUsageTestService(t, repo, &ollamaUsageHTTPStub{body: ollamaUsageFixture(t)}, &ollamaUsageSettings{}, true)
	_, err := svc.Refresh(context.Background(), 10)
	require.ErrorIs(t, err, providercore.ErrOllamaCloudUsageIdentityChanged)
	require.NotContains(t, provider.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
}

func TestOllamaCloudUsageRunnerHonorsLeaderLockAndBackoff(t *testing.T) {
	provider := ollamaUsageProvider(11)
	provider.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=secret"
	provider.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey] = true
	repo := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{11: provider}}}
	upstream := &ollamaUsageHTTPStub{body: ollamaUsageFixture(t)}
	settingsRepo := &ollamaUsageSettings{values: map[string]string{
		providercore.SettingKeyOllamaCloudUsageSettings: `{"enabled":true,"interval_minutes":60}`,
	}}
	cache := &ollamaUsageLeader{}
	_, acquired := providercore.AcquireSingletonLease(context.Background(), cache, nil, ollamaCloudUsageLeaderLockKey, "peer", time.Minute)
	require.True(t, acquired)
	svc := newOllamaUsageTestService(t, repo, upstream, settingsRepo, true)
	svc.lockCache = cache
	require.NoError(t, svc.RunDue(context.Background()))
	require.Zero(t, upstream.calls.Load())
	require.NoError(t, cache.ReleaseLeaderLock(context.Background(), ollamaCloudUsageLeaderLockKey, "peer"))
	require.NoError(t, svc.RunDue(context.Background()))
	require.Equal(t, int64(1), upstream.calls.Load())

	firstFailure := providercore.NextOllamaCloudUsageDelay(60, 1, 0, rand.Int64N)
	thirdFailure := providercore.NextOllamaCloudUsageDelay(60, 3, 0, rand.Int64N)
	require.Greater(t, thirdFailure, firstFailure)
	require.GreaterOrEqual(t, providercore.NextOllamaCloudUsageDelay(60, 1, 3*time.Hour, rand.Int64N), 3*time.Hour)
	require.LessOrEqual(t, providercore.NextOllamaCloudUsageDelay(60, 20, 0, rand.Int64N), providercore.OllamaCloudUsageMaxDelay+5*time.Minute)
}

func TestOllamaCloudUsageRunnerDisablesAutoRefreshAfterUnpersistableIdentityError(t *testing.T) {
	provider := ollamaUsageProvider(14)
	provider.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=secret"
	provider.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey] = true
	missingProxyID := int64(99)
	provider.ProxyID = &missingProxyID
	provider.Proxy = nil
	repo := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{14: provider}}}
	settingsRepo := &ollamaUsageSettings{values: map[string]string{
		providercore.SettingKeyOllamaCloudUsageSettings: `{"enabled":true,"interval_minutes":60}`,
	}}
	upstream := &ollamaUsageHTTPStub{body: ollamaUsageFixture(t)}
	svc := newOllamaUsageTestService(t, repo, upstream, settingsRepo, true)

	require.NoError(t, svc.RunDue(context.Background()))
	require.Equal(t, int64(1), repo.disableAutoCalls.Load())
	require.Equal(t, false, provider.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey])
	require.Zero(t, upstream.calls.Load())

	require.NoError(t, svc.RunDue(context.Background()))
	require.Equal(t, int64(1), repo.disableAutoCalls.Load())
	require.Zero(t, upstream.calls.Load())
}

func TestOllamaCloudUsageRunnerIdentityChangePreservesOldGroupAndDoesNotLoop(t *testing.T) {
	anchor := ollamaUsageProvider(15)
	anchor.Credentials["api_key"] = "shared-before-rotation"
	anchor.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=secret"
	anchor.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey] = true
	sibling := ollamaUsageProvider(16)
	sibling.Platform = capability.PlatformAnthropic
	sibling.Credentials = map[string]any{"api_key": "shared-before-rotation", "base_url": "https://www.ollama.com:443/v1"}
	sibling.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=secret"
	sibling.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey] = true
	dueAnchor := *anchor
	dueAnchor.Credentials = providercore.CRSMergeMap(nil, anchor.Credentials)
	dueAnchor.Extra = providercore.CRSMergeMap(nil, anchor.Extra)
	repo := &ollamaUsageTestRepo{
		ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{
			anchor.ID: anchor, sibling.ID: sibling,
		}},
		due: []providercore.Record{dueAnchor},
	}
	var rotateOnce sync.Once
	repo.beforeSnapshot = func() {
		rotateOnce.Do(func() {
			repo.mu.Lock()
			defer repo.mu.Unlock()
			anchor.Credentials["api_key"] = "rotated-provider-key"
			delete(anchor.Extra, providercore.OllamaCloudUsageSessionExtraKey)
			delete(anchor.Extra, providercore.OllamaCloudUsageAutoRefreshExtraKey)
			delete(anchor.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
		})
	}
	settingsRepo := &ollamaUsageSettings{values: map[string]string{
		providercore.SettingKeyOllamaCloudUsageSettings: `{"enabled":true,"interval_minutes":60}`,
	}}
	upstream := &ollamaUsageHTTPStub{body: ollamaUsageFixture(t)}
	svc := newOllamaUsageTestService(t, repo, upstream, settingsRepo, true)

	require.NoError(t, svc.RunDue(context.Background()))
	require.Equal(t, int64(1), repo.disableAutoAttempts.Load())
	require.Zero(t, repo.disableAutoCalls.Load(), "the stale anchor CAS must not disable the old sibling group")
	require.Equal(t, true, sibling.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey])
	require.NotContains(t, anchor.Extra, providercore.OllamaCloudUsageAutoRefreshExtraKey)

	repo.due = []providercore.Record{*anchor, *sibling}
	require.NoError(t, svc.RunDue(context.Background()))
	require.Equal(t, int64(1), repo.disableAutoAttempts.Load(), "the changed provider must not be retried")
	require.Equal(t, true, sibling.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey])
	require.NotNil(t, providercore.DecodeOllamaCloudUsageSnapshot(sibling.Extra), "the still-valid sibling must refresh normally")
	require.Equal(t, int64(2), upstream.calls.Load())
}

func TestOllamaCloudUsageSingleflightConcurrencyAndRunnerSwitches(t *testing.T) {
	providers := make(map[int64]*providercore.Record)
	for id := int64(1); id <= 7; id++ {
		provider := ollamaUsageProvider(id)
		provider.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=secret"
		provider.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey] = true
		providers[id] = provider
	}
	repo := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: providers}}
	unblock := make(chan struct{})
	entered := make(chan struct{}, 10)
	upstream := &ollamaUsageHTTPStub{body: ollamaUsageFixture(t), beforeResponse: func(*http.Request) {
		entered <- struct{}{}
		<-unblock
	}}
	settingsRepo := &ollamaUsageSettings{values: map[string]string{}}
	svc := newOllamaUsageTestService(t, repo, upstream, settingsRepo, true)

	// Automatic refresh is disabled by default.
	require.NoError(t, svc.RunDue(context.Background()))
	require.Zero(t, upstream.calls.Load())

	settingsRepo.values[providercore.SettingKeyOllamaCloudUsageSettings] = `{"enabled":true,"interval_minutes":60}`
	var singleflight sync.WaitGroup
	singleflight.Add(2)
	for range 2 {
		go func() {
			defer singleflight.Done()
			_, _ = svc.Refresh(context.Background(), 1)
		}()
	}
	<-entered
	close(unblock)
	singleflight.Wait()
	require.Equal(t, int64(1), upstream.calls.Load())

	// Clear snapshots to make every provider due and exercise the shared four-request concurrency limit.
	for _, provider := range providers {
		delete(provider.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
	}
	unblock2 := make(chan struct{})
	upstream.beforeResponse = func(*http.Request) { <-unblock2 }
	done := make(chan struct{})
	go func() {
		_ = svc.RunDue(context.Background())
		close(done)
	}()
	require.Eventually(t, func() bool { return upstream.active.Load() == ollamaCloudUsageConcurrency }, time.Second, 10*time.Millisecond)
	close(unblock2)
	<-done
	require.LessOrEqual(t, upstream.maxActive.Load(), int64(ollamaCloudUsageConcurrency))
	require.Equal(t, int64(8), upstream.calls.Load())
}

func (ollamaUsageTestEncryptor) Encrypt(value string) (string, error) { return "cipher:" + value, nil }

func (ollamaUsageTestEncryptor) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, "cipher:") {
		return "", errors.New("authentication failed")
	}
	return strings.TrimPrefix(value, "cipher:"), nil
}

// GetByID counts lookups so concurrency tests can coordinate callers before singleflight.
func (r *ollamaUsageTestRepo) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	r.getByIDCalls.Add(1)
	return r.ollamaUsageRows.GetByID(ctx, id)
}

func (r *ollamaUsageTestRepo) ListOllamaCloudUsageGroupProviders(_ context.Context, anchors []*providercore.Record) ([]providercore.Record, error) {
	r.groupResolveCalls.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	wanted := make(map[string]struct{}, len(anchors))
	for _, anchor := range anchors {
		if fingerprint, ok := providercore.OllamaCloudUsageGroupFingerprint(anchor); ok {
			wanted[fingerprint] = struct{}{}
		}
	}
	result := make([]providercore.Record, 0, len(r.providers))
	for _, provider := range r.providers {
		fingerprint, ok := providercore.OllamaCloudUsageGroupFingerprint(provider)
		if _, match := wanted[fingerprint]; !ok || !match {
			continue
		}
		result = append(result, cloneOllamaUsageTestProvider(*provider))
	}
	return result, nil
}

// cloneOllamaUsageTestProvider copies mutable maps to model independent database results.
// Shared maps would race when group writes hold r.mu but RunDue reads a returned record without that lock.
func cloneOllamaUsageTestProvider(provider providercore.Record) providercore.Record {
	provider.Credentials = providercore.CRSMergeMap(nil, provider.Credentials)
	provider.Extra = providercore.CRSMergeMap(nil, provider.Extra)
	return provider
}

func (r *ollamaUsageTestRepo) SaveOllamaCloudUsageSession(_ context.Context, expected *providercore.Record, ciphertext string, autoRefresh bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	members, err := r.ollamaGroupMembersLocked(expected)
	if err != nil {
		return err
	}
	for _, provider := range members {
		provider.Extra[providercore.OllamaCloudUsageSessionExtraKey] = ciphertext
		provider.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey] = autoRefresh
		delete(provider.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
	}
	return nil
}

func (r *ollamaUsageTestRepo) DeleteOllamaCloudUsageSession(_ context.Context, expected *providercore.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	members, err := r.ollamaGroupMembersLocked(expected)
	if err != nil {
		return err
	}
	for _, provider := range members {
		delete(provider.Extra, providercore.OllamaCloudUsageSessionExtraKey)
		delete(provider.Extra, providercore.OllamaCloudUsageAutoRefreshExtraKey)
		delete(provider.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
	}
	return nil
}

func (r *ollamaUsageTestRepo) SetOllamaCloudUsageAutoRefresh(_ context.Context, expected *providercore.Record, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	members, err := r.ollamaGroupMembersLocked(expected)
	if err != nil || !r.ollamaExpectedSessionExistsLocked(members, expected) {
		return providercore.ErrOllamaCloudUsageIdentityChanged
	}
	for _, provider := range members {
		applyOllamaUsageTestManagedExtra(provider, expected)
		provider.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey] = enabled
	}
	return nil
}

func (r *ollamaUsageTestRepo) UpdateOllamaCloudUsageSnapshot(_ context.Context, expected *providercore.Record, snapshot *providercore.OllamaCloudUsageSnapshot) error {
	if r.beforeSnapshot != nil {
		r.beforeSnapshot()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	members, err := r.ollamaGroupMembersLocked(expected)
	if err != nil || !r.ollamaExpectedSessionExistsLocked(members, expected) {
		return providercore.ErrOllamaCloudUsageIdentityChanged
	}
	for _, provider := range members {
		applyOllamaUsageTestManagedExtra(provider, expected)
		provider.Extra[providercore.OllamaCloudUsageSnapshotExtraKey] = snapshot
	}
	return nil
}

func (r *ollamaUsageTestRepo) DisableOllamaCloudUsageAutoRefresh(_ context.Context, expected *providercore.Record) error {
	r.disableAutoAttempts.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	members, err := r.ollamaGroupMembersLocked(expected)
	if err != nil || !r.ollamaExpectedSessionExistsLocked(members, expected) {
		return providercore.ErrOllamaCloudUsageIdentityChanged
	}
	for _, provider := range members {
		applyOllamaUsageTestManagedExtra(provider, expected)
		provider.Extra[providercore.OllamaCloudUsageAutoRefreshExtraKey] = false
		delete(provider.Extra, providercore.OllamaCloudUsageSnapshotExtraKey)
	}
	r.disableAutoCalls.Add(1)
	return nil
}

func (r *ollamaUsageTestRepo) ollamaGroupMembersLocked(expected *providercore.Record) ([]*providercore.Record, error) {
	anchor := r.providers[expected.ID]
	if !sameOllamaUsageTestIdentity(anchor, expected) {
		return nil, providercore.ErrOllamaCloudUsageIdentityChanged
	}
	fingerprint, ok := providercore.OllamaCloudUsageGroupFingerprint(expected)
	if !ok {
		return nil, providercore.ErrOllamaCloudUsageProviderInvalid
	}
	members := make([]*providercore.Record, 0, len(r.providers))
	for _, provider := range r.providers {
		candidate, valid := providercore.OllamaCloudUsageGroupFingerprint(provider)
		if valid && candidate == fingerprint {
			if provider.Extra == nil {
				provider.Extra = make(map[string]any)
			}
			members = append(members, provider)
		}
	}
	return members, nil
}

func (r *ollamaUsageTestRepo) ollamaExpectedSessionExistsLocked(members []*providercore.Record, expected *providercore.Record) bool {
	for _, member := range members {
		if member.Extra[providercore.OllamaCloudUsageSessionExtraKey] == expected.Extra[providercore.OllamaCloudUsageSessionExtraKey] {
			return true
		}
	}
	return false
}

func applyOllamaUsageTestManagedExtra(provider, source *providercore.Record) {
	for _, key := range []string{providercore.OllamaCloudUsageSessionExtraKey, providercore.OllamaCloudUsageAutoRefreshExtraKey, providercore.OllamaCloudUsageSnapshotExtraKey} {
		delete(provider.Extra, key)
		if value, ok := source.Extra[key]; ok {
			provider.Extra[key] = value
		}
	}
}

func (r *ollamaUsageTestRepo) ListDueOllamaCloudUsageProviders(_ context.Context, _ time.Time, _, _ time.Duration, limit int) ([]providercore.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.due) > 0 {
		out := make([]providercore.Record, 0, min(limit, len(r.due)))
		for _, provider := range r.due[:min(limit, len(r.due))] {
			out = append(out, cloneOllamaUsageTestProvider(provider))
		}
		return out, nil
	}
	out := make([]providercore.Record, 0, len(r.providers))
	for _, provider := range r.providers {
		out = append(out, cloneOllamaUsageTestProvider(*provider))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (r *ollamaRefreshPreflightIdentityChangeRepo) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	if r.getCalls.Add(1) == 2 {
		r.mu.Lock()
		r.providers[id].Credentials["api_key"] = "rotated-before-refresh"
		r.mu.Unlock()
	}
	return r.ollamaUsageRows.GetByID(ctx, id)
}

func sameOllamaUsageTestIdentity(left, right *providercore.Record) bool {
	return left != nil && right != nil && left.Platform == right.Platform && left.Type == right.Type &&
		reflect.DeepEqual(left.Credentials, right.Credentials) && reflect.DeepEqual(left.ProxyID, right.ProxyID)
}

func (s *ollamaUsageHTTPStub) Do(req *http.Request, proxyURL string, _ int64, _ int) (*http.Response, error) {
	s.calls.Add(1)
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for {
		peak := s.maxActive.Load()
		if active <= peak || s.maxActive.CompareAndSwap(peak, active) {
			break
		}
	}
	s.mu.Lock()
	s.lastRequest = req
	s.lastProxyURL = proxyURL
	s.mu.Unlock()
	if s.beforeResponse != nil {
		s.beforeResponse(req)
	}
	status := s.status
	if status == 0 {
		status = http.StatusOK
	}
	header := s.header
	if header == nil {
		header = http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(string(s.body))), Request: req}, nil
}

func (s *ollamaUsageHTTPStub) DoWithTLS(req *http.Request, proxyURL string, providerID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, providerID, concurrency)
}

func ollamaUsageProvider(id int64) *providercore.Record {
	return &providercore.Record{
		ID: id, Name: fmt.Sprintf("ollama-%d", id), Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://ollama.com", "api_key": fmt.Sprintf("key-%d", id)},
		Extra:       map[string]any{}, Status: providercore.StatusActive, Schedulable: true, Concurrency: 1,
	}
}

func newOllamaUsageTestService(t *testing.T, repo *ollamaUsageTestRepo, upstream ollamaUsageTransport, settingsRepo settingscore.Repository, fixedKey bool) *ollamaUsageContract {
	t.Helper()
	svc := newOllamaUsageContract(repo, upstream, settingsRepo, ollamaUsageTestEncryptor{}, fixedKey)
	t.Cleanup(svc.Stop)
	return svc
}

func ollamaUsageFixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("../../upstream/ollama/testdata/ollama_settings_usage.html")
	require.NoError(t, err)
	return body
}

func (r *ollamaUsageRows) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.providers[id]
	if v == nil {
		return nil, providercore.ErrProviderNotFound
	}
	copy := cloneOllamaUsageTestProvider(*v)
	return &copy, nil
}

func (r *ollamaUsageSettings) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.values[key]
	if !ok {
		return "", settingscore.ErrSettingNotFound
	}
	return v, nil
}

func (r *ollamaUsageSettings) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	r.values[key] = value
	return nil
}

func newOllamaUsageContract(repo providercore.OllamaProviderReader, transport ollamaUsageTransport, source providercore.RuntimeSettingsStore, cipher providercore.OllamaSessionCipher, fixedKey bool) *ollamaUsageContract {
	s := &ollamaUsageContract{now: time.Now}
	options := providercore.OllamaUsageOptions{
		EncryptionKeyConfigured: fixedKey,
		Now:                     func() time.Time { return s.now() },
		Jitter:                  rand.Int64N,
		InstanceID:              "ollama-contract",
		Lease: func(ctx context.Context, key, owner string, ttl time.Duration) (func(), bool) {
			return providercore.AcquireSingletonLease(ctx, s.lockCache, nil, key, owner, ttl)
		},
	}
	if transport != nil {
		options.Fetch = OllamaUsageFetcher(transport.Do)
	}
	var config providercore.OllamaUsageSettingsStore
	if source != nil {
		config = providercore.NewRuntimeSettings(source, settingscore.ErrSettingNotFound)
	}
	s.OllamaCloudUsageService = providercore.NewOllamaCloudUsageService(repo, config, cipher, options)
	return s
}

func (l *ollamaUsageLeader) TryAcquireLeaderLock(_ context.Context, key, owner string, _ time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.owners == nil {
		l.owners = make(map[string]string)
	}
	if _, ok := l.owners[key]; ok {
		return false, nil
	}
	l.owners[key] = owner
	return true, nil
}

func (l *ollamaUsageLeader) ReleaseLeaderLock(_ context.Context, key, owner string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.owners[key] == owner {
		delete(l.owners, key)
	}
	return nil
}
