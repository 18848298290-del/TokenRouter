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

// Ollama fixture values express session bytes, the expected parallel-fetch
// count, and the leader namespace used by runner assertions.
const (
	ollamaCloudUsageMaxSessionBytes = 16 * 1024
	ollamaCloudUsageConcurrency     = 4
	ollamaCloudUsageLeaderLockKey   = "ollama:cloud:usage:leader"
)

// ollamaUsageTestEncryptor uses reversible prefix framing for managed-session
// test fixtures.
type ollamaUsageTestEncryptor struct{}

// ollamaUsageTestRepo models synchronized group session writes and records
// usage maintenance calls.
type ollamaUsageTestRepo struct {
	*ollamaUsageRows // embedded synchronized provider row store
	// due overrides stored-row enumeration when nonempty. ListDue returns copies
	// of its first limited entries, regardless of supplied timing filters.
	due []providercore.Record
	// beforeSnapshot optionally mutates identity before snapshot locking and
	// comparison. It runs outside the row mutex so the hook can acquire that lock.
	beforeSnapshot      func()
	disableAutoAttempts atomic.Int64 // attempted automatic-refresh disable count
	disableAutoCalls    atomic.Int64 // accepted automatic-refresh disable count
	groupResolveCalls   atomic.Int64 // observed group member query count
	getByIDCalls        atomic.Int64 // observed provider lookup count
}

// ollamaRefreshPreflightIdentityChangeRepo rotates credentials during the
// second provider lookup for refresh preflight.
type ollamaRefreshPreflightIdentityChangeRepo struct {
	*ollamaUsageTestRepo              // embedded group usage fixture repository
	getCalls             atomic.Int64 // preflight provider lookup count
}

// ollamaUsageHTTPStub captures browser requests and measures concurrent fetches
// with optional response gates.
type ollamaUsageHTTPStub struct {
	// status supplies the fixture response code. Zero selects HTTP 200 after the
	// optional response hook has run.
	status int
	// body supplies the HTML bytes copied into each fixture response reader.
	// Configure them before transport starts and keep the slice stable during
	// concurrent fetches.
	body []byte // configured browser response HTML bytes
	// header supplies shared response headers when nonnil. Nil uses fresh HTML
	// defaults; callers keep configured maps stable during concurrent requests.
	header http.Header
	// calls, active, and maxActive are atomic counters used during concurrent
	// fetches. Do publishes request and proxy captures under mu before its
	// response hook can block.
	calls     atomic.Int64 // total browser fetch call count
	active    atomic.Int64 // current browser fetch call count
	maxActive atomic.Int64 // recorded peak concurrent fetch count
	// beforeResponse optionally runs after request capture and before response
	// creation. Tests use it to hold fetches or observe cancellation.
	beforeResponse func(*http.Request)
	// lastRequest and lastProxyURL describe the same most recently captured call.
	// mu protects their publication, and assertions read them after the relevant
	// fetches finish.
	lastRequest  *http.Request // latest captured browser request pointer
	lastProxyURL string        // latest captured selected proxy URL
	mu           sync.Mutex    // last request capture access lock
}

// ollamaUsageRows protects stored records and returns copied supported
// credential and Extra collections.
type ollamaUsageRows struct {
	// mu protects providers during reads, group selection, and managed writes.
	// Returned map collections are copied under the lock; stored member pointers
	// remain shared within write operations.
	mu        sync.Mutex                     // stored provider row access lock
	providers map[int64]*providercore.Record // shared stored provider record pointers
}

// ollamaUsageSettings protects string settings used by the production runtime
// reader.
type ollamaUsageSettings struct {
	settingscore.Repository // unimplemented settings repository methods
	// mu protects values during both settings lookup and update. The fixture
	// embeds other repository methods, which remain unavailable unless tests
	// configure that interface.
	mu     sync.Mutex        // setting value access lock
	values map[string]string // stored runtime-setting string values
}

// ollamaUsageTransport sends prepared browser usage requests with provider
// transport parameters.
type ollamaUsageTransport interface {
	// Do sends the prepared browser request with selected proxy, provider ID, and
	// concurrency. It returns a transport error or response whose body the usage
	// fetcher closes.
	Do(*http.Request, string, int64, int) (*http.Response, error)
}

// ollamaUsageContract retains production usage lifecycle with replaceable clock
// and lease fixtures.
type ollamaUsageContract struct {
	*providercore.OllamaCloudUsageService // embedded production usage lifecycle owner
	// now supplies the current fixture clock through a retained options callback.
	// Tests replace it before queries and keep it stable during execution.
	now func() time.Time
	// lockCache optionally supplies the lease backend read by the retained
	// callback. Nil lets AcquireSingletonLease apply its no-backend execution
	// rule.
	lockCache providercore.CNMonitorLeader
}

// ollamaUsageLeader models owner-checked lease acquisition and release without
// TTL expiry.
type ollamaUsageLeader struct {
	// mu protects owners during claim checks and owner-matched deletion. The map
	// records no expiry, so fixture leases stay claimed until their matching owner
	// releases them.
	mu     sync.Mutex        // lease owner access lock
	owners map[string]string // claimed lease keys and owners
}

// TestOllamaUsageStopOwnsManualRefresh blocks a manual browser fetch until its
// context is cancelled. Stop must deliver request cancellation before returning
// and leave the stored record without a late usage snapshot.
func TestOllamaUsageStopOwnsManualRefresh(t *testing.T) {
	value := ollamaUsageProvider(901)
	value.Extra[providercore.OllamaCloudUsageSessionExtraKey] = "cipher:wos-session=fixture"
	repo := &ollamaUsageTestRepo{ollamaUsageRows: &ollamaUsageRows{providers: map[int64]*providercore.Record{value.ID: value}}}
	entered, cancelled := make(chan struct{}), make(chan struct{})
	// Signal browser entry, hold the reply until service cancellation, and signal
	// that cancellation before returning. This keeps Stop's wait obligation
	// observable before late-response assertions.
	upstream := &ollamaUsageHTTPStub{body: ollamaUsageFixture(t), beforeResponse: func(req *http.Request) { close(entered); <-req.Context().Done(); close(cancelled) }}
	svc := newOllamaUsageTestService(t, repo, upstream, &ollamaUsageSettings{}, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// Keep refresh in flight until Stop cancels its owned context. The buffered
	// result channel lets cleanup wait for completion without holding up the query
	// return.
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

// TestIsOllamaCloudUsageProviderStrictOfficialHost compares allowed official
// HTTPS forms with lookalike, userinfo, query, fragment, port, and path
// variants. Eligibility must accept only the supported normalized official
// forms.
func TestIsOllamaCloudUsageProviderStrictOfficialHost(t *testing.T) {
	tests := []struct {
		baseURL  string // supplied provider upstream root
		platform string // provider platform under test
		want     bool   // expected official-host eligibility result
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

// TestOllamaCloudUsageSessionEncryptionFailClosedAndWriteOnlyState tests
// unavailable encryption configuration, unsupported cookies, and a corrupted
// stored session. Managed state must retain approved encoded cookies while
// public state omits session text and refresh rejects undecodable values.
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

// TestOllamaCloudUsageGroupSharesAcrossPlatformsURLVariantsAndDynamicSiblings
// groups matching API keys across supported platform and URL forms. State must
// display the valid group snapshot, include a later sibling, and resolve a list
// with one group lookup.
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
	// Add a matching sibling after the first state query. Publish it under the row
	// lock so later group resolution must discover current membership.
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

// TestOllamaCloudUsageSaveAutoRefreshAndDeleteAreGroupScoped changes session
// and refresh settings through two matching records. Both members must receive
// managed state and cleanup, and initial session saving must leave the different
// key unconfigured.
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

// TestOllamaCloudUsageRefreshSingleflightAndRunnerDeduplicateSharedGroup holds
// one group fetch while another sibling joins. Both manual callers must share
// one request, and a later due scan must fetch that group once.
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

// TestOllamaCloudUsageRefreshRejectsGroupChangeBeforeUpstreamRequest rotates
// the provider key during preflight reread. Refresh must return
// identity-changed before transport and leave the stored snapshot absent.
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

// TestOllamaCloudUsageRefreshUsesFixedURLCookieAndNoRedirects supplies a
// managed session containing an unrelated tracking cookie. The fetch must use
// the fixed settings URL, approved cookie, no bearer token, and disabled
// redirects.
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

// TestOllamaCloudUsageManualRefreshUsesShortIndependentInterval repeats a
// manual query under a fixed clock. The repeat must be limited, while saving a
// repaired session clears observation state and permits an immediate new fetch.
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

// TestOllamaCloudUsageRefreshUsesHydratedProxyIdentity supplies a loaded proxy
// matching its selected ID. The browser fetch must receive that proxy URL while
// refresh returns without an error.
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

// TestOllamaCloudUsageRedirectAndBodyLimitArePersistedSafely returns a redirect
// or oversized successful body. Refresh must save failed snapshots with the
// corresponding safe reason codes instead of accepting those responses.
func TestOllamaCloudUsageRedirectAndBodyLimitArePersistedSafely(t *testing.T) {
	for _, test := range []struct {
		name   string // usage failure scenario label
		status int    // supplied HTTP response status
		body   []byte // supplied browser response bytes
		reason string // expected safe snapshot failure code
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

// TestOllamaCloudUsageRefreshRejectsIdentityChange rotates the API key
// immediately before snapshot storage. Refresh must return identity-changed and
// leave that replacement record without the stale observation.
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

// TestOllamaCloudUsageRunnerHonorsLeaderLockAndBackoff holds the runner lease
// under another owner and then releases it. Scanning must skip nonleader work,
// fetch after acquisition, and apply increasing bounded failure delays with
// a Retry-After lower bound.
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

// TestOllamaCloudUsageRunnerDisablesAutoRefreshAfterUnpersistableIdentityError
// supplies a selected but missing proxy relation. The runner must disable
// automatic refresh once and avoid sending repeated browser requests for that
// invalid record.
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

// TestOllamaCloudUsageRunnerIdentityChangePreservesOldGroupAndDoesNotLoop
// rotates an anchor after due selection while its sibling remains valid. Stale
// conditional disabling must spare the sibling group, and a later cycle must
// refresh that sibling normally.
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
	// Inject one rotation before snapshot comparison while retaining the old
	// sibling session. The hook clears the changed anchor's managed keys under the
	// row lock.
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

// TestOllamaCloudUsageSingleflightConcurrencyAndRunnerSwitches checks
// default-disabled scans, shared manual requests, and seven distinct due
// records. Manual coalescing must send once and enabled scanning must respect
// the shared four-request concurrency limit.
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
	// Hold the first shared request while both callers reach the service, then
	// wait for their returns before asserting one transport call and beginning due
	// scanning.
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

// Encrypt adds the fixture's reversible cipher prefix to supplied session text.
// It models an encoded stored value without cryptographic protection, letting
// service tests focus on managed-state handling.
func (ollamaUsageTestEncryptor) Encrypt(value string) (string, error) { return "cipher:" + value, nil }

// Decrypt removes the fixture prefix or returns its authentication error when
// absent. It performs no cryptographic operation, and malformed stored values
// fail the service's session-decoding path.
func (ollamaUsageTestEncryptor) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, "cipher:") {
		return "", errors.New("authentication failed")
	}
	return strings.TrimPrefix(value, "cipher:"), nil
}

// GetByID atomically counts a provider lookup and delegates to the copied row
// reader.
func (r *ollamaUsageTestRepo) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	r.getByIDCalls.Add(1)
	return r.ollamaUsageRows.GetByID(ctx, id)
}

// ListOllamaCloudUsageGroupProviders returns copies of stored members whose
// fingerprints match any supplied anchor. It counts the group lookup and holds
// the row mutex through membership selection and copying; invalid anchors
// contribute no group.
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

// cloneOllamaUsageTestProvider replaces credential and Extra maps through
// CRSMergeMap before returning a record value. Supported nested collections are
// copied, while typed snapshot pointers and other record references remain
// shared and are treated as immutable.
func cloneOllamaUsageTestProvider(provider providercore.Record) providercore.Record {
	provider.Credentials = providercore.CRSMergeMap(nil, provider.Credentials)
	provider.Extra = providercore.CRSMergeMap(nil, provider.Extra)
	return provider
}

// SaveOllamaCloudUsageSession stores the encoded session and automatic flag on
// the matched group and removes its prior snapshots. The expected anchor is
// required and row identity is checked while holding the repository mutex.
//
// Membership selection and every member mutation share that lock, so the write
// uses one current group instead of a set computed before rotation. A missing
// or changed anchor aborts before mutation. Accepted writes update session and
// refresh policy together, leaving later usage retrieval to produce a snapshot
// for that session.
func (r *ollamaUsageTestRepo) SaveOllamaCloudUsageSession(_ context.Context, expected *providercore.Record, ciphertext string, autoRefresh bool) error {
	// Hold identity comparison, current group selection, and every managed member
	// update under one row lock so rotation cannot redirect a stale write into
	// another group.
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

// DeleteOllamaCloudUsageSession removes session, automatic-refresh, and
// snapshot keys from the matched group. The expected anchor identifies the
// current stored row, and identity errors return before any managed state is
// removed.
//
// The repository mutex covers anchor comparison, membership selection, and all
// deletions. That keeps a rotated anchor from using a stale group to erase
// another member's session. Shared records are changed in place under this
// lock, and subsequent copied reads observe the resulting managed state.
func (r *ollamaUsageTestRepo) DeleteOllamaCloudUsageSession(_ context.Context, expected *providercore.Record) error {
	// Hold identity comparison, current group selection, and every managed member
	// update under one row lock so rotation cannot redirect a stale write into
	// another group.
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

// SetOllamaCloudUsageAutoRefresh applies the supplied flag after validating
// group and expected session under the row mutex. It returns identity-changed
// when membership or the session comparison fails.
//
// Accepted members first receive the expected managed Extra values, then the
// new automatic flag. Holding membership and mutation in one critical section
// prevents a changed session from being used to toggle a formerly associated
// group. Managed snapshot pointers can remain shared in this fixture, so tests
// treat their values as immutable.
func (r *ollamaUsageTestRepo) SetOllamaCloudUsageAutoRefresh(_ context.Context, expected *providercore.Record, enabled bool) error {
	// Hold identity comparison, current group selection, and every managed member
	// update under one row lock so rotation cannot redirect a stale write into
	// another group.
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

// UpdateOllamaCloudUsageSnapshot runs the optional race hook before locking and
// then stores one snapshot pointer across the current matched group. Membership
// and expected-session failures return identity-changed before publication.
//
// The hook can rotate credentials using the row mutex itself, so it runs
// outside that lock. Afterward comparison, managed-state copying, and snapshot
// installation share the critical section. All members retain the same supplied
// snapshot pointer, which callers leave immutable while state readers inspect
// the group.
func (r *ollamaUsageTestRepo) UpdateOllamaCloudUsageSnapshot(_ context.Context, expected *providercore.Record, snapshot *providercore.OllamaCloudUsageSnapshot) error {
	// Run the identity-change hook before taking the row mutex, then compare the
	// expected group and session under that lock before publishing the
	// observation.
	if r.beforeSnapshot != nil {
		r.beforeSnapshot()
	}
	// Hold identity comparison, current group selection, and every managed member
	// update under one row lock so rotation cannot redirect a stale write into
	// another group.
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

// DisableOllamaCloudUsageAutoRefresh counts an attempt before validating the
// group and session under the row mutex. A failed comparison returns
// identity-changed without counting an accepted disable or changing members.
//
// Accepted members receive expected managed values, disable automatic refresh,
// and drop snapshots in the same critical section. The separate attempt and
// accepted counters let runner tests prove that a stale anchor attempted
// cleanup but did not disable a still-valid sibling group. Shared snapshot
// references are removed, not modified.
func (r *ollamaUsageTestRepo) DisableOllamaCloudUsageAutoRefresh(_ context.Context, expected *providercore.Record) error {
	r.disableAutoAttempts.Add(1)
	// Hold identity comparison, current group selection, and every managed member
	// update under one row lock so rotation cannot redirect a stale write into
	// another group.
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

// ollamaGroupMembersLocked returns shared current group members after comparing
// the expected anchor identity. The caller holds the row mutex; changed anchors
// return identity-changed, invalid fingerprints return provider-invalid, and
// matched records receive initialized Extra maps.
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

// ollamaExpectedSessionExistsLocked reports whether any selected member has the
// expected session entry value. The caller holds the row mutex; comparison uses
// interface equality and can match two absent entries as nil. Supply comparable
// values, as the managed session strings and absent entries used here.
func (r *ollamaUsageTestRepo) ollamaExpectedSessionExistsLocked(members []*providercore.Record, expected *providercore.Record) bool {
	for _, member := range members {
		if member.Extra[providercore.OllamaCloudUsageSessionExtraKey] == expected.Extra[providercore.OllamaCloudUsageSessionExtraKey] {
			return true
		}
	}
	return false
}

// applyOllamaUsageTestManagedExtra replaces the three managed usage keys from
// source while retaining unrelated Extra. Callers hold the owning row lock and
// supply an initialized destination map; copied values remain shared with
// source.
func applyOllamaUsageTestManagedExtra(provider, source *providercore.Record) {
	for _, key := range []string{providercore.OllamaCloudUsageSessionExtraKey, providercore.OllamaCloudUsageAutoRefreshExtraKey, providercore.OllamaCloudUsageSnapshotExtraKey} {
		delete(provider.Extra, key)
		if value, ok := source.Extra[key]; ok {
			provider.Extra[key] = value
		}
	}
}

// ListDueOllamaCloudUsageProviders returns copied records from the configured
// due slice or stored map up to a positive limit. It ignores timing parameters
// in this fixture; callers do not depend on stored-map iteration order.
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

// GetByID rotates the stored API key on its second lookup before returning a
// copied row. The mutation holds the row mutex, making refresh preflight
// observe a changed group before transport execution.
func (r *ollamaRefreshPreflightIdentityChangeRepo) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	if r.getCalls.Add(1) == 2 {
		r.mu.Lock()
		// Change the key at preflight reread before a browser request can be
		// admitted, reproducing a selected record whose group identity has already
		// rotated.
		r.providers[id].Credentials["api_key"] = "rotated-before-refresh"
		r.mu.Unlock()
	}
	return r.ollamaUsageRows.GetByID(ctx, id)
}

// sameOllamaUsageTestIdentity compares platform, type, credentials, and proxy
// ID for two nonnil records. It ignores other fields, and nil records return
// false, matching the fixture's group-write identity selection.
func sameOllamaUsageTestIdentity(left, right *providercore.Record) bool {
	return left != nil && right != nil && left.Platform == right.Platform && left.Type == right.Type &&
		reflect.DeepEqual(left.Credentials, right.Credentials) && reflect.DeepEqual(left.ProxyID, right.ProxyID)
}

// Do captures a nonnil browser request and returns configured status, headers,
// and HTML bytes. Zero status selects HTTP 200, nil headers select HTML
// defaults, and callers close the returned body.
//
// Atomic counters track call count, current activity, and its recorded peak
// across independently blocked fetches. Request and proxy capture use the mutex
// before the optional response hook runs, while fixture response settings
// remain stable during calls. The hook can wait for release or cancellation,
// allowing tests to verify service admission and late-response handling.
func (s *ollamaUsageHTTPStub) Do(req *http.Request, proxyURL string, _ int64, _ int) (*http.Response, error) {
	s.calls.Add(1)
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for {
		peak := s.maxActive.Load()
		// Concurrent fetches can raise the peak together. Retry only a lost peak
		// update so a later observation cannot replace a higher recorded activity
		// count.
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

// DoWithTLS delegates browser capture to Do and ignores the selected
// fingerprint profile.
func (s *ollamaUsageHTTPStub) DoWithTLS(req *http.Request, proxyURL string, providerID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, providerID, concurrency)
}

// ollamaUsageProvider creates an active schedulable official-host API-key
// record with fresh maps. Its ID supplies fixture name and key values, allowing
// tests to combine or separate group identities deliberately.
func ollamaUsageProvider(id int64) *providercore.Record {
	return &providercore.Record{
		ID: id, Name: fmt.Sprintf("ollama-%d", id), Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://ollama.com", "api_key": fmt.Sprintf("key-%d", id)},
		Extra:       map[string]any{}, Status: providercore.StatusActive, Schedulable: true, Concurrency: 1,
	}
}

// newOllamaUsageTestService constructs production usage behavior with the
// fixture encoder and supplied dependencies. It registers Stop for test cleanup
// and passes fixedKey through as the encryption-configuration gate.
func newOllamaUsageTestService(t *testing.T, repo *ollamaUsageTestRepo, upstream ollamaUsageTransport, settingsRepo settingscore.Repository, fixedKey bool) *ollamaUsageContract {
	t.Helper()
	svc := newOllamaUsageContract(repo, upstream, settingsRepo, ollamaUsageTestEncryptor{}, fixedKey)
	t.Cleanup(svc.Stop)
	return svc
}

// ollamaUsageFixture reads the checked-in settings HTML used by native usage
// parser fixtures.
func ollamaUsageFixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("../../upstream/ollama/testdata/ollama_settings_usage.html")
	require.NoError(t, err)
	return body
}

// GetByID returns a copied record under the row mutex or provider-not-found for
// missing IDs. Supported credential and Extra collections are copied, while
// other record references follow cloneOllamaUsageTestProvider's shared-value
// rules.
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

// GetValue reads an exact setting key under the settings mutex. Missing keys
// return the production not-found sentinel, allowing the runtime reader to
// apply its defined defaults.
func (r *ollamaUsageSettings) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.values[key]
	if !ok {
		return "", settingscore.ErrSettingNotFound
	}
	return v, nil
}

// Set stores a string under the settings mutex and initializes an absent map.
// The same lock covers reads, keeping fixture settings lookup and publication
// synchronized.
func (r *ollamaUsageSettings) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	r.values[key] = value
	return nil
}

// newOllamaUsageContract binds production usage behavior to the fixture's live
// clock, optional lease backend, transport, and settings. Nil transport leaves
// Fetch unset; nil settings leaves configuration absent, and fixedKey controls
// session-save eligibility.
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

// TryAcquireLeaderLock claims an unowned key for the supplied owner under the
// lease mutex. Existing keys reject acquisition even for the same owner, and
// this fixture ignores both caller context and TTL.
//
// The map holds each claim until an owner-matched release removes it.
// Comparison and owner publication stay under one lock so competing callers
// cannot both claim the key. Tests use this controlled lifetime to check runner
// competition; timer-based lease expiry remains outside this in-memory fixture.
func (l *ollamaUsageLeader) TryAcquireLeaderLock(_ context.Context, key, owner string, _ time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.owners == nil {
		l.owners = make(map[string]string)
	}
	// Hold the key-existence check and owner installation in one critical section,
	// so two runner claims cannot both acquire the same fixture lease.
	if _, ok := l.owners[key]; ok {
		return false, nil
	}
	l.owners[key] = owner
	return true, nil
}

// ReleaseLeaderLock removes a claim when its stored owner equals the supplied
// owner and always reports nil. The caller cannot release another owner's claim
// through this comparison.
//
// Comparison and deletion share the lease mutex with acquisition, so a
// concurrent claimant sees either the retained old owner or the released key.
// The fixture tracks no expiry, making this operation the normal lifetime
// endpoint for an accepted claim. Missing keys have no retained state to
// remove, and mismatched owners leave the existing claim intact.
func (l *ollamaUsageLeader) ReleaseLeaderLock(_ context.Context, key, owner string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Compare the owner before deleting under the acquisition lock. A release from
	// an earlier or different runner then cannot remove the current claim.
	if l.owners[key] == owner {
		delete(l.owners, key)
	}
	return nil
}
