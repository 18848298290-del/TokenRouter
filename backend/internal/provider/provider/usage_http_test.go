package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	acctcore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// cnDecisionRepo rotates the selected identity immediately before monitor
// health writes are compared.
type cnDecisionRepo struct {
	*cnUsageMonitorRepo // embedded monitor decision capture repository
	// changed prevents repeated fixture rotation. The first decision advances
	// UpdatedAt and installs a replacement credential map before comparison.
	changed bool
}

// cnUsageMonitorRepo captures monitor snapshots and provider cooldown writes
// under its fixture mutex.
type cnUsageMonitorRepo struct {
	// mu protects base repository providers, platform lists, snapshot captures,
	// and recorded health effects. Individual readers return shallow record state; the base
	// health comparison and delegated mutation use separate lock acquisitions.
	mu         sync.Mutex                 // fixture row and capture access lock
	providers  map[int64]*acctcore.Record // stored monitor records by identifier
	byPlatform map[string][]int64         // platform-ordered provider fixture identifiers
	// writes retains copied outer snapshot objects after accepted comparisons.
	// Nested amounts and observation references remain shared with the caller for
	// assertions.
	writes []*acctcore.CNUsageMonitorSnapshot
	// casResult gates snapshot capture after a matching UpdatedAt. False rejects
	// that write without appending the requested observation.
	casResult        bool
	pauseReason      string    // latest captured monitor suspension reason
	pauseUntil       time.Time // latest captured monitor suspension deadline
	clearCalls       int       // captured monitor recovery clear count
	updateExtraCalls int       // captured generic Extra write count
}

// cnUsageMonitorHTTP captures monitor requests and can wait for their
// cancellation before returning.
type cnUsageMonitorHTTP struct {
	// mu protects calls, requests, and the response configuration captured by
	// DoWithTLS. The method releases it before startup notification or a
	// cancellable blocked wait.
	mu       sync.Mutex      // monitor request capture access lock
	calls    int             // observed native monitor request count
	requests []*http.Request // cloned captured monitor request objects
	// status supplies the unblocked response code. Zero becomes HTTP 200 after
	// request capture and any startup signal.
	status int    // scripted HTTP response code
	body   string // configured native usage response text
	// started optionally receives a nonblocking startup signal. Signals can be
	// dropped when no receiver or buffer slot is available.
	started chan struct{}
	// block makes the captured call wait for request cancellation and return its
	// context error. False permits configured status and body response creation.
	block bool
}

// cnUsageMonitorLeaderLock supplies a fixed acquisition result and counts
// runner lease attempts.
type cnUsageMonitorLeaderLock struct {
	acquired bool // fixed fixture lease acquisition result
	calls    int  // observed fixture lease attempt count
}

// cnQueryFixtureOptions carries outbound URL policy and periodic monitor
// options for usage fixtures.
type cnQueryFixtureOptions struct {
	Policy  egress.UsageURLPolicy     // native query root trust policy
	Monitor acctcore.CNMonitorOptions // periodic monitor option override values
}

// upstreamUsageProviderRepoStub exposes one mutable provider identity with
// optional lookup notifications.
type upstreamUsageProviderRepoStub struct {
	mu sync.Mutex // single fixture provider access lock
	// provider retains the one stored identity. A nil pointer models deletion, and
	// GetByID otherwise returns a shallow value copy.
	provider *acctcore.Record
	// getEvent optionally receives nonblocking lookup notifications under the
	// provider lock. Tests drain earlier signals before waiting for another
	// singleflight caller.
	getEvent chan struct{}
}

// blockingUpstreamUsageHTTP gates shared usage queries and counts requests
// across independently cancelled waiters.
type blockingUpstreamUsageHTTP struct {
	// started is closed once before request waiting. Configure a nonnil channel so
	// tests can observe that the shared fetch reached transport.
	started chan struct{}
	// release permits a successful scripted response when signalled. A nil channel
	// leaves request cancellation as the only available completion path.
	release chan struct{}
	body    string       // scripted shared-query response JSON
	once    sync.Once    // first request startup signal guard
	calls   atomic.Int32 // shared upstream request attempt count
}

// upstreamUsageHTTPStub serializes captured requests and consumes scripted
// native usage outcomes.
type upstreamUsageHTTPStub struct {
	// mu keeps captured requests aligned with sequential scripted responses. A
	// call appends its request before consuming an outcome, including transport
	// errors or an empty script.
	mu       sync.Mutex      // script and capture access lock
	requests []*http.Request // cloned captured native usage requests
	// responses is consumed in call order while mu is held. Empty queues return
	// the missing-endpoint fixture response, and errors precede body creation.
	responses []struct {
		status int    // scripted HTTP response code
		body   string // scripted native response text
		err    error  // scripted transport failure result
	}
}

// TestCNMonitorOldIdentityCannotPauseNewCredentials replaces the provider
// identity before a low-balance health decision is written. The fixture must
// run its replacement hook and leave the old pause absent from captured reason
// and stored deadline.
func TestCNMonitorOldIdentityCannotPauseNewCredentials(t *testing.T) {
	value := newCNUsageMonitorProvider(1, capability.PlatformKimi, acctcore.ProviderModePayG)
	repo := &cnDecisionRepo{cnUsageMonitorRepo: &cnUsageMonitorRepo{providers: map[int64]*acctcore.Record{1: value}, byPlatform: map[string][]int64{capability.PlatformKimi: {1}}, casResult: true}}
	upstream := &cnUsageMonitorHTTP{body: `{"code":0,"data":{"available_balance":0.1}}`}
	cfg := newCNQueryFixtureOptions()
	cfg.Monitor.BalanceThreshold = 0.5
	usage := newCNUsageFixture(repo, upstream, cfg, nil)
	monitor := newCNMonitorFixture(repo, usage, cfg)
	monitor.RunOnce(context.Background())
	require.True(t, repo.changed)
	require.Empty(t, repo.pauseReason, "旧查询健康结论不得写到管理员替换后的身份")
	require.Nil(t, value.TempUnschedulableUntil)
}

// TestCNUsageMonitorRunOncePersistsUnifiedSnapshotWithoutLegacyWrites queries
// an official Kimi balance through the periodic monitor. Storage must receive a
// valid unified snapshot while the generic Extra writer remains unused and
// redirects stay disabled.
func TestCNUsageMonitorRunOncePersistsUnifiedSnapshotWithoutLegacyWrites(t *testing.T) {
	provider := newCNUsageMonitorProvider(1, capability.PlatformKimi, acctcore.ProviderModePayG)
	repo := &cnUsageMonitorRepo{
		providers:  map[int64]*acctcore.Record{1: provider},
		byPlatform: map[string][]int64{capability.PlatformKimi: {1}},
		casResult:  true,
	}
	upstream := &cnUsageMonitorHTTP{body: `{"code":0,"data":{"available_balance":12.5}}`}
	service := newCNUsageMonitorForTest(repo, upstream, newCNQueryFixtureOptions())
	service.RunOnce(context.Background())

	require.Len(t, repo.writes, 1)
	snapshot := repo.writes[0]
	require.Equal(t, acctcore.CNUsageMonitorSnapshotVersion, snapshot.Version)
	require.Equal(t, acctcore.UpstreamUsageAdapterKimiBalance, snapshot.Adapter)
	require.Equal(t, capability.PlatformKimi, snapshot.Provider)
	require.Equal(t, "balance", snapshot.Mode)
	require.NotNil(t, snapshot.Balance)
	require.InDelta(t, 12.5, *snapshot.Balance.Remaining, 1e-9)
	require.Empty(t, snapshot.LastError)
	require.Zero(t, repo.updateExtraCalls, "纯适配器和协调器不得调用通用 UpdateExtra")
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "api.moonshot.cn", upstream.requests[0].URL.Hostname())
	require.True(t, upstreamcore.HTTPUpstreamRedirectsDisabled(upstream.requests[0].Context()))
}

// TestCNUsageMonitorFailurePreservesLastSuccess starts with a successful
// DeepSeek balance observation and returns a later upstream error. The new
// snapshot must record failure while retaining the earlier successful balance
// and observation time.
func TestCNUsageMonitorFailurePreservesLastSuccess(t *testing.T) {
	provider := newCNUsageMonitorProvider(2, capability.PlatformDeepseek, acctcore.ProviderModePayG)
	queryConfig, err := acctcore.EffectiveUpstreamUsageConfig(provider)
	require.NoError(t, err)
	queryConfig.Adapter = acctcore.CNUpstreamUsageAdapterName(provider)
	observed := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	remaining := 8.0
	provider.Extra[acctcore.CNUsageMonitorSnapshotExtraKey] = &acctcore.CNUsageMonitorSnapshot{
		Version:       acctcore.CNUsageMonitorSnapshotVersion,
		Adapter:       queryConfig.Adapter,
		IdentityHash:  acctcore.UpstreamUsageContextFingerprint(provider, queryConfig, UpstreamUsageBaseURL(provider)),
		Provider:      capability.PlatformDeepseek,
		Mode:          "balance",
		Unit:          "CNY",
		Balance:       &acctcore.UpstreamUsageAmount{Remaining: &remaining},
		ObservedAt:    &observed,
		LastAttemptAt: observed,
	}
	repo := &cnUsageMonitorRepo{
		providers:  map[int64]*acctcore.Record{2: provider},
		byPlatform: map[string][]int64{capability.PlatformDeepseek: {2}},
		casResult:  true,
	}
	upstream := &cnUsageMonitorHTTP{status: http.StatusBadGateway, body: `{}`}
	service := newCNUsageMonitorForTest(repo, upstream, newCNQueryFixtureOptions())
	service.RunOnce(context.Background())

	require.Len(t, repo.writes, 1)
	snapshot := repo.writes[0]
	require.NotNil(t, snapshot.LastError)
	require.Equal(t, observed, *snapshot.ObservedAt)
	require.InDelta(t, remaining, *snapshot.Balance.Remaining, 1e-9)
}

// TestCNUsageMonitorCustomHostRequiresExplicitAllowlist selects a custom relay
// before and after adding its enabled allowlist entry. Monitoring must skip the
// untrusted request and permit one fetch once that host is allowed.
func TestCNUsageMonitorCustomHostRequiresExplicitAllowlist(t *testing.T) {
	provider := newCNUsageMonitorProvider(3, capability.PlatformDeepseek, acctcore.ProviderModePayG)
	provider.Credentials["base_url"] = "https://relay.example/v1"
	repo := &cnUsageMonitorRepo{
		providers:  map[int64]*acctcore.Record{3: provider},
		byPlatform: map[string][]int64{capability.PlatformDeepseek: {3}},
		casResult:  true,
	}
	upstream := &cnUsageMonitorHTTP{body: `{}`}
	cfg := newCNQueryFixtureOptions()
	service := newCNUsageMonitorForTest(repo, upstream, cfg)
	service.RunOnce(context.Background())
	require.Zero(t, upstream.calls)
	require.Len(t, repo.writes, 1)
	require.NotNil(t, repo.writes[0].LastError)

	cfg.Policy.Enabled = true
	cfg.Policy.UpstreamHosts = []string{"relay.example"}
	repo.writes = nil
	upstream.body = `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"3"}]}`
	service = newCNUsageMonitorForTest(repo, upstream, cfg)
	service.RunOnce(context.Background())
	require.Equal(t, 1, upstream.calls)
	require.Len(t, repo.writes, 1)
	require.Equal(t, "relay.example", upstream.requests[0].URL.Hostname())
}

// TestCNUsageMonitorSkipsCycleWhenNotLeader rejects lease acquisition for a
// valid monitor provider. One leader attempt must occur while transport and
// snapshot storage remain unused for that cycle.
func TestCNUsageMonitorSkipsCycleWhenNotLeader(t *testing.T) {
	provider := newCNUsageMonitorProvider(4, capability.PlatformKimi, acctcore.ProviderModePayG)
	repo := &cnUsageMonitorRepo{
		providers:  map[int64]*acctcore.Record{4: provider},
		byPlatform: map[string][]int64{capability.PlatformKimi: {4}},
		casResult:  true,
	}
	upstream := &cnUsageMonitorHTTP{body: `{}`}
	lock := &cnUsageMonitorLeaderLock{acquired: false}
	service := newCNUsageMonitorForTest(repo, upstream, newCNQueryFixtureOptions(), func(o *acctcore.CNMonitorOptions) { o.Leader = lock })
	service.RunOnce(context.Background())
	require.Equal(t, 1, lock.calls)
	require.Zero(t, upstream.calls)
	require.Empty(t, repo.writes)
}

// TestCNUsageMonitorDefaultOffAndStopCancelsProbe checks default-disabled
// startup and an enabled blocked request. Disabled startup must leave snapshot
// captures empty, while Stop must finish after the later blocked probe is
// cancelled.
func TestCNUsageMonitorDefaultOffAndStopCancelsProbe(t *testing.T) {
	repo := &cnUsageMonitorRepo{providers: map[int64]*acctcore.Record{}, byPlatform: map[string][]int64{}, casResult: true}
	service := newCNUsageMonitorForTest(repo, &cnUsageMonitorHTTP{}, newCNQueryFixtureOptions())
	require.NoError(t, service.StartContext(context.Background()))
	// Monitoring is disabled by default, so startup writes nothing.
	require.Empty(t, repo.writes)

	provider := newCNUsageMonitorProvider(5, capability.PlatformKimi, acctcore.ProviderModePayG)
	repo.providers[5] = provider
	repo.byPlatform[capability.PlatformKimi] = []int64{5}
	started := make(chan struct{}, 1)
	upstream := &cnUsageMonitorHTTP{started: started, block: true}
	cfg := newCNQueryFixtureOptions()
	cfg.Monitor.Enabled = true
	service = newCNUsageMonitorForTest(repo, upstream, cfg, func(o *acctcore.CNMonitorOptions) { o.Interval = time.Millisecond })
	require.NoError(t, service.StartContext(context.Background()))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("监控探测未启动")
	}
	// Run Stop separately while transport is blocked, then wait for its completion
	// signal under a bounded test budget to detect a shutdown that fails to drain.
	done := make(chan struct{})
	go func() {
		service.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop 未取消进行中的探测")
	}
}

// TestCNUsageBalanceThresholdUsesAllCurrenciesAndIdentityReason compares two
// wallet currencies before and after both fall below threshold. Low-balance
// classification must consider every currency and retain the identity-derived
// monitor reason prefix.
func TestCNUsageBalanceThresholdUsesAllCurrenciesAndIdentityReason(t *testing.T) {
	available := true
	result := &acctcore.UpstreamUsageQueryResult{
		Mode:      "balance",
		Available: &available,
		Balances: []acctcore.UpstreamUsageBalanceEntry{
			{Currency: "CNY", Remaining: 0.1},
			{Currency: "USD", Remaining: 2},
		},
	}
	low, known := acctcore.CNUsageBalanceBelowThreshold(result, 0.5)
	require.True(t, known)
	require.False(t, low)
	result.Balances[1].Remaining = 0.2
	low, known = acctcore.CNUsageBalanceBelowThreshold(result, 0.5)
	require.True(t, known)
	require.True(t, low)
	require.True(t, strings.HasPrefix(acctcore.CNUsageMonitorReason("abc"), "cn_usage_monitor:abc:"))
}

// TestCNUpstreamUsageAdaptersPreserveConfiguredHostAndNormalizeResults queries
// CN coding and balance adapters through custom roots. Requests must retain the
// relay host, fixed paths, authentication and organization headers, while
// returned usage follows each adapter's normalization.
func TestCNUpstreamUsageAdaptersPreserveConfiguredHostAndNormalizeResults(t *testing.T) {
	tests := []struct {
		name        string                                               // native adapter scenario label
		provider    *acctcore.Record                                     // provider record under evaluation
		response    string                                               // supplied native usage response JSON
		wantAdapter string                                               // expected native adapter identifier
		wantPath    string                                               // expected request endpoint path
		wantQuery   string                                               // expected organization query selection
		wantAuth    string                                               // expected upstream Authorization value
		wantOrg     string                                               // expected organization header value
		wantProject string                                               // expected project header value
		assert      func(*testing.T, *acctcore.UpstreamUsageQueryResult) // adapter-specific normalized result assertions
	}{
		{
			name: "Kimi Coding 百分比窗口",
			provider: &acctcore.Record{
				ID: 11, Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
				Credentials: map[string]any{"api_key": "kimi-key", "provider_mode": acctcore.ProviderModeCoding, "base_url": "https://relay.example/coding/v1"},
			},
			response:    `{"limits":[{"detail":{"limit":100,"remaining":25,"resetTime":"2026-08-24T00:00:00Z"}}],"usage":{"limit":1000,"remaining":800,"resetTime":"2026-08-30T00:00:00Z"}}`,
			wantAdapter: acctcore.UpstreamUsageAdapterKimiCoding,
			wantPath:    "/coding/v1/usages",
			wantAuth:    "Bearer kimi-key",
			assert: func(t *testing.T, result *acctcore.UpstreamUsageQueryResult) {
				require.Equal(t, "PERCENT", result.Unit)
				require.Len(t, result.Limits, 2)
				require.InDelta(t, 75, *result.Limits[0].Used, 1e-9)
			},
		},
		{
			name: "智谱 Coding 裸密钥",
			provider: &acctcore.Record{
				ID: 12, Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
				Credentials: map[string]any{"api_key": "zhipu-key", "provider_mode": acctcore.ProviderModeCoding, "base_url": "https://relay.example/api/coding/paas/v4"},
			},
			response:    `{"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":32,"nextResetTime":1787529600000}]}}`,
			wantAdapter: acctcore.UpstreamUsageAdapterZhipuCoding,
			wantPath:    "/api/monitor/usage/quota/limit",
			wantQuery:   "",
			wantAuth:    "zhipu-key",
			assert: func(t *testing.T, result *acctcore.UpstreamUsageQueryResult) {
				require.Len(t, result.Limits, 1)
				require.InDelta(t, 32, *result.Limits[0].Used, 1e-9)
			},
		},
		{
			name: "智谱团队 Coding 组织项目",
			provider: &acctcore.Record{
				ID: 121, Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
				Credentials: map[string]any{"api_key": "zhipu-team-key", "provider_mode": acctcore.ProviderModeCoding, "base_url": "https://relay.example/api/coding/paas/v4", "zhipu_organization": "org-demo", "zhipu_project": "proj-demo"},
			},
			response:    `{"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":12,"nextResetTime":1787529600000}]}}`,
			wantAdapter: acctcore.UpstreamUsageAdapterZhipuCoding,
			wantPath:    "/api/monitor/usage/quota/limit",
			wantQuery:   "2",
			wantAuth:    "zhipu-team-key",
			wantOrg:     "org-demo",
			wantProject: "proj-demo",
			assert: func(t *testing.T, result *acctcore.UpstreamUsageQueryResult) {
				require.Len(t, result.Limits, 1)
				require.InDelta(t, 12, *result.Limits[0].Used, 1e-9)
			},
		},
		{
			name: "Kimi 按量余额",
			provider: &acctcore.Record{
				ID: 13, Platform: capability.PlatformKimi, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
				Credentials: map[string]any{"api_key": "kimi-payg", "base_url": "https://relay.example/v1"},
			},
			response:    `{"code":0,"data":{"available_balance":"6.25"}}`,
			wantAdapter: acctcore.UpstreamUsageAdapterKimiBalance,
			wantPath:    "/v1/users/me/balance",
			wantAuth:    "Bearer kimi-payg",
			assert: func(t *testing.T, result *acctcore.UpstreamUsageQueryResult) {
				require.Equal(t, "CNY", result.Unit)
				require.InDelta(t, 6.25, *result.Balance.Remaining, 1e-9)
			},
		},
		{
			name: "DeepSeek 多币种余额",
			provider: &acctcore.Record{
				ID: 14, Platform: capability.PlatformDeepseek, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
				Credentials: map[string]any{"api_key": "deepseek-key", "base_url": "https://relay.example/anthropic", "api_protocol": acctcore.APIProtocolAnthropic},
			},
			response:    `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"3.5"},{"currency":"USD","total_balance":"1.25"}]}`,
			wantAdapter: acctcore.UpstreamUsageAdapterDeepseekBalance,
			wantPath:    "/user/balance",
			wantAuth:    "Bearer deepseek-key",
			assert: func(t *testing.T, result *acctcore.UpstreamUsageQueryResult) {
				require.Len(t, result.Balances, 2)
				require.NotNil(t, result.Available)
				require.True(t, *result.Available)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &upstreamUsageProviderRepoStub{provider: test.provider}
			upstream := &upstreamUsageHTTPStub{responses: []struct {
				status int    // scripted HTTP response code
				body   string // scripted native response text
				err    error  // scripted transport failure result
			}{{status: http.StatusOK, body: test.response}}}
			service := newUsageContractService(repo, upstream, testUpstreamUsageConfig())
			result, err := service.QueryProvider(context.Background(), test.provider.ID)
			require.NoError(t, err)
			require.Equal(t, test.wantAdapter, result.Adapter)
			require.Len(t, upstream.requests, 1)
			request := upstream.requests[0]
			require.Equal(t, "relay.example", request.URL.Hostname())
			require.Equal(t, test.wantPath, request.URL.Path)
			require.Equal(t, test.wantQuery, request.URL.Query().Get("type"))
			require.Equal(t, test.wantAuth, request.Header.Get("Authorization"))
			require.Equal(t, test.wantOrg, request.Header.Get("bigmodel-organization"))
			require.Equal(t, test.wantProject, request.Header.Get("bigmodel-project"))
			require.True(t, upstreamcore.HTTPUpstreamRedirectsDisabled(request.Context()))
			test.assert(t, result)
		})
	}
}

// TestCNUpstreamUsageUnsupportedPayGDoesNotSendRequest selects Zhipu
// pay-as-you-go usage with no supported balance adapter. Query must return
// unsupported before any captured transport request is sent.
func TestCNUpstreamUsageUnsupportedPayGDoesNotSendRequest(t *testing.T) {
	provider := &acctcore.Record{
		ID: 15, Platform: capability.PlatformZhipu, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive,
		Credentials: map[string]any{"api_key": "zhipu-key", "provider_mode": acctcore.ProviderModePayG},
	}
	repo := &upstreamUsageProviderRepoStub{provider: provider}
	upstream := &upstreamUsageHTTPStub{}
	service := newUsageContractService(repo, upstream, testUpstreamUsageConfig())
	_, err := service.QueryProvider(context.Background(), provider.ID)
	require.ErrorIs(t, err, acctcore.ErrUpstreamUsageUnsupported)
	require.Empty(t, upstream.requests)
}

// TestZivvUsageQueryUsesVersionedBalanceEndpoint queries a relay using its
// configured Zivv adapter. The result must retain wallet balance while the
// request uses v1/user/balance, the real key, and disabled redirects.
func TestZivvUsageQueryUsesVersionedBalanceEndpoint(t *testing.T) {
	provider := &acctcore.Record{
		ID: 17, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-zivv", "base_url": "https://zivv.example/"},
		Extra:       map[string]any{acctcore.UpstreamUsageQueryExtraKey: map[string]any{"adapter": acctcore.UpstreamUsageAdapterZivv}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int    // scripted HTTP response code
		body   string // scripted native response text
		err    error  // scripted transport failure result
	}{
		{status: http.StatusOK, body: `{"balance":900.49,"currency":"USD","is_available":true,"key_limit":0,"key_used":20646.4,"plan_name":"cc b","total_used":22460.66}`},
	}}
	service := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
	result, err := service.QueryProvider(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, acctcore.UpstreamUsageAdapterZivv, result.Adapter)
	require.Equal(t, 900.49, *result.Balance.Remaining)

	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "/v1/user/balance", upstream.requests[0].URL.Path)
	require.Equal(t, "Bearer sk-zivv", upstream.requests[0].Header.Get("Authorization"))
	require.True(t, upstreamcore.HTTPUpstreamRedirectsDisabled(upstream.requests[0].Context()))
}

// TestNewAPIUsageQueryUsesTokenQuotaEndpoint combines display configuration,
// key quota, and wallet responses with an attempted authorization override.
// Paths and credentials must follow the fixed query order, and normalized
// results must omit the key.
func TestNewAPIUsageQueryUsesTokenQuotaEndpoint(t *testing.T) {
	provider := &acctcore.Record{
		ID: 11, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-new-api", "base_url": "https://new-api.example/v1",
			"header_override_enabled": true,
			"header_overrides":        map[string]any{"x-custom": "usage-query", "authorization": "Bearer wrong-key"},
		},
		Extra: map[string]any{acctcore.UpstreamUsageQueryExtraKey: map[string]any{"adapter": acctcore.UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int    // scripted HTTP response code
		body   string // scripted native response text
		err    error  // scripted transport failure result
	}{
		{status: http.StatusOK, body: `{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000}}`},
		{status: http.StatusOK, body: `{"code":true,"message":"ok","data":{"object":"token_usage","name":"Default Token","total_granted":632500000,"total_used":360000,"total_available":632140000,"unlimited_quota":false,"expires_at":1893456000}}`},
		{status: http.StatusOK, body: `{"balance_infos":[{"currency":"USD","total_balance":"1264.28"}]}`},
	}}
	service := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
	result, err := service.QueryProvider(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, 1264.28, *result.Usage.Balance.Remaining)
	require.Equal(t, 1264.28, *result.Usage.Limits[0].Remaining)
	require.Equal(t, "Default Token", result.Usage.Subscription.PlanName)
	require.Equal(t, 1264.28, *result.Usage.Subscription.Remaining)
	require.NotContains(t, string(mustJSONMarshal(t, result)), "sk-new-api")

	upstream.mu.Lock()
	require.Len(t, upstream.requests, 3)
	require.Empty(t, upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer sk-new-api", upstream.requests[1].Header.Get("Authorization"))
	require.Equal(t, "/api/status", upstream.requests[0].URL.Path)
	require.Equal(t, "/api/usage/token/", upstream.requests[1].URL.Path)
	require.Equal(t, "/user/balance", upstream.requests[2].URL.Path)
	for _, request := range upstream.requests {
		require.Equal(t, "usage-query", anthropic.GetHeaderRaw(request.Header, "x-custom"))
		require.True(t, upstreamcore.HTTPUpstreamRedirectsDisabled(request.Context()))
	}
	upstream.mu.Unlock()
}

// TestNewAPIUsageEndpointMissingIsUnsupported supplies display configuration
// followed by a missing key-usage endpoint. The native adapter must return
// unsupported rather than proceeding with invented token quota data.
func TestNewAPIUsageEndpointMissingIsUnsupported(t *testing.T) {
	provider := &acctcore.Record{
		ID: 12, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive,
		Credentials: map[string]any{"api_key": "sk-new-api", "base_url": "https://new-api.example/v1"},
		Extra:       map[string]any{acctcore.UpstreamUsageQueryExtraKey: map[string]any{"adapter": acctcore.UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int    // scripted HTTP response code
		body   string // scripted native response text
		err    error  // scripted transport failure result
	}{
		{status: http.StatusOK, body: `{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000}}`},
		{status: http.StatusNotFound, body: `{}`},
	}}
	service := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
	_, err := service.QueryProvider(context.Background(), provider.ID)
	require.ErrorIs(t, err, acctcore.ErrUpstreamUsageUnsupported)
}

// TestDeepSeekBalanceAdapterRejectsMalformedPayloads supplies missing,
// wrong-shaped, empty, and nonnumeric balance entries. Each malformed response
// must return the invalid-response error through the configured native usage
// query.
func TestDeepSeekBalanceAdapterRejectsMalformedPayloads(t *testing.T) {
	cases := []struct {
		name string // native adapter scenario label
		body string // scripted native response text
	}{
		{name: "missing balance_infos", body: `{"is_available":true}`},
		{name: "non array balance_infos", body: `{"balance_infos":{}}`},
		{name: "empty balance_infos", body: `{"balance_infos":[]}`},
		{name: "invalid total_balance", body: `{"balance_infos":[{"currency":"CNY","total_balance":"not-a-number"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &acctcore.Record{
				ID: 901, Platform: capability.PlatformDeepseek, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
				Credentials: map[string]any{"api_key": "deepseek-key", "base_url": "https://relay.example/anthropic", "api_protocol": acctcore.APIProtocolAnthropic},
			}
			upstream := &upstreamUsageHTTPStub{responses: []struct {
				status int    // scripted HTTP response code
				body   string // scripted native response text
				err    error  // scripted transport failure result
			}{{status: http.StatusOK, body: tc.body}}}
			svc := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
			_, err := svc.QueryProvider(context.Background(), provider.ID)
			require.ErrorIs(t, err, acctcore.ErrUpstreamUsageInvalidResponse)
		})
	}
}

// TestDeepSeekBalanceAdapterPreservesValidZeroBalance returns an unavailable
// wallet with a numeric zero amount. Query must succeed and retain both zero
// remaining balance and the false availability observation.
func TestDeepSeekBalanceAdapterPreservesValidZeroBalance(t *testing.T) {
	provider := &acctcore.Record{
		ID: 902, Platform: capability.PlatformDeepseek, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "deepseek-key", "base_url": "https://relay.example/anthropic", "api_protocol": acctcore.APIProtocolAnthropic},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int    // scripted HTTP response code
		body   string // scripted native response text
		err    error  // scripted transport failure result
	}{{status: http.StatusOK, body: `{"is_available":false,"balance_infos":[{"currency":"CNY","total_balance":"0"}]}`}}}
	svc := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())

	result, err := svc.QueryProvider(context.Background(), provider.ID)
	require.NoError(t, err)
	require.NotNil(t, result.Usage)
	require.NotNil(t, result.Usage.Balance)
	require.Zero(t, *result.Usage.Balance.Remaining)
	require.False(t, *result.Usage.Available)
}

// TestNewAPIUsageContinuesWhenStatusProbeFails returns failed display
// configuration before valid key quota and wallet data. Query must complete
// three calls, authenticate the key and wallet requests, and return the supplied
// wallet amount.
func TestNewAPIUsageContinuesWhenStatusProbeFails(t *testing.T) {
	provider := &acctcore.Record{
		ID: 13, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive,
		Credentials: map[string]any{"api_key": "sk-new-api", "base_url": "https://new-api.example/v1"},
		Extra:       map[string]any{acctcore.UpstreamUsageQueryExtraKey: map[string]any{"adapter": acctcore.UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int    // scripted HTTP response code
		body   string // scripted native response text
		err    error  // scripted transport failure result
	}{
		{status: http.StatusServiceUnavailable, body: `{"success":false}`},
		{status: http.StatusOK, body: `{"code":true,"data":{"object":"token_usage","name":"Token","total_granted":632500000,"total_used":360000,"total_available":632140000,"unlimited_quota":false,"expires_at":0}}`},
		{status: http.StatusOK, body: `{"balance_infos":[{"currency":"USD","total_balance":"1264.28"}]}`},
	}}
	service := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
	result, err := service.QueryProvider(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, 1264.28, *result.Usage.Balance.Remaining)

	upstream.mu.Lock()
	require.Len(t, upstream.requests, 3)
	require.Empty(t, upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer sk-new-api", upstream.requests[1].Header.Get("Authorization"))
	require.Equal(t, "Bearer sk-new-api", upstream.requests[2].Header.Get("Authorization"))
	upstream.mu.Unlock()
}

// TestNewAPIUsageUsesConfiguredUserWalletToken supplies unlimited key quota
// plus user-level wallet credentials. Wallet lookup must use its access token
// and user ID while the result keeps unlimited key state and omits the token.
func TestNewAPIUsageUsesConfiguredUserWalletToken(t *testing.T) {
	provider := &acctcore.Record{
		ID: 14, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-new-api", "base_url": "https://new-api.example/v1",
			acctcore.NewAPIUserAccessTokenCredentialKey: "pat-secret", acctcore.NewAPIUserIDCredentialKey: "42",
		},
		Extra: map[string]any{acctcore.UpstreamUsageQueryExtraKey: map[string]any{"adapter": acctcore.UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int    // scripted HTTP response code
		body   string // scripted native response text
		err    error  // scripted transport failure result
	}{
		{status: http.StatusOK, body: `{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000}}`},
		{status: http.StatusOK, body: `{"code":true,"data":{"object":"token_usage","name":"tf","total_granted":-1,"total_used":1,"total_available":-2,"unlimited_quota":true,"expires_at":0}}`},
		{status: http.StatusOK, body: `{"success":true,"data":{"id":42,"quota":632140000,"used_quota":360000}}`},
	}}
	service := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
	result, err := service.QueryProvider(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, 1264.28, *result.Usage.Balance.Remaining)
	require.True(t, result.Usage.Subscription.Unlimited)

	upstream.mu.Lock()
	require.Len(t, upstream.requests, 3)
	require.Equal(t, "/api/user/self", upstream.requests[2].URL.Path)
	require.Equal(t, "Bearer pat-secret", upstream.requests[2].Header.Get("Authorization"))
	require.Equal(t, "42", upstream.requests[2].Header.Get("New-Api-User"))
	forbidden := string(mustJSONMarshal(t, result))
	require.NotContains(t, forbidden, "pat-secret")
	upstream.mu.Unlock()
}

// TestNewAPIUsageUsesWalletTokenWithoutConfiguredUserID supplies an unlimited
// key and wallet token without a user ID. Wallet remaining must use user quota
// alone, leaving total and used absent and omitting New-Api-User.
func TestNewAPIUsageUsesWalletTokenWithoutConfiguredUserID(t *testing.T) {
	provider := &acctcore.Record{
		ID: 16, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-new-api", "base_url": "https://new-api.example/v1",
			acctcore.NewAPIUserAccessTokenCredentialKey: "pat-secret",
		},
		Extra: map[string]any{acctcore.UpstreamUsageQueryExtraKey: map[string]any{"adapter": acctcore.UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int    // scripted HTTP response code
		body   string // scripted native response text
		err    error  // scripted transport failure result
	}{
		{status: http.StatusOK, body: `{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000}}`},
		{status: http.StatusOK, body: `{"code":true,"data":{"object":"token_usage","name":"tf","total_granted":-27753,"total_used":2006775843,"total_available":-2006803596,"unlimited_quota":true,"expires_at":0}}`},
		{status: http.StatusOK, body: `{"success":true,"data":{"id":2,"quota":608218554,"used_quota":2006781446}}`},
	}}
	service := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
	result, err := service.QueryProvider(context.Background(), provider.ID)
	require.NoError(t, err)
	require.InDelta(t, 1216.437108, *result.Usage.Balance.Remaining, 0.000001)
	require.Nil(t, result.Usage.Balance.Used)
	require.Nil(t, result.Usage.Balance.Total)
	require.True(t, result.Usage.Subscription.Unlimited)

	upstream.mu.Lock()
	require.Len(t, upstream.requests, 3)
	require.Equal(t, "/api/user/self", upstream.requests[2].URL.Path)
	require.Equal(t, "Bearer pat-secret", upstream.requests[2].Header.Get("Authorization"))
	require.Empty(t, upstream.requests[2].Header.Get("New-Api-User"))
	upstream.mu.Unlock()
}

// TestNewAPIUsageRequiresWalletInsteadOfTokenQuota returns unlimited token data
// and an HTML wallet response. Query must report wallet-unavailable so key
// quota is not presented as a wallet balance.
func TestNewAPIUsageRequiresWalletInsteadOfTokenQuota(t *testing.T) {
	provider := &acctcore.Record{
		ID: 15, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive,
		Credentials: map[string]any{"api_key": "sk-new-api", "base_url": "https://new-api.example/v1"},
		Extra:       map[string]any{acctcore.UpstreamUsageQueryExtraKey: map[string]any{"adapter": acctcore.UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int    // scripted HTTP response code
		body   string // scripted native response text
		err    error  // scripted transport failure result
	}{
		{status: http.StatusOK, body: `{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000}}`},
		{status: http.StatusOK, body: `{"code":true,"data":{"object":"token_usage","name":"tf","total_granted":-1,"total_used":1,"total_available":-2,"unlimited_quota":true,"expires_at":0}}`},
		{status: http.StatusOK, body: `<html>frontend</html>`},
	}}
	service := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
	_, err := service.QueryProvider(context.Background(), provider.ID)
	require.ErrorIs(t, err, acctcore.ErrUpstreamUsageWalletUnavailable)
}

// TestUpstreamUsageServiceQueriesAPIKeyWithoutMutatingProvider queries a
// supported API-key relay and records adapter metrics. The result, request, and
// success metric must match the fixture, and current credential and Extra maps
// must compare equal to the shallow reference. That reference shares its maps
// with the provider, so these assertions cannot detect in-place map mutation.
func TestUpstreamUsageServiceQueriesAPIKeyWithoutMutatingProvider(t *testing.T) {
	provider := &acctcore.Record{
		ID:          7,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://usage.example/v1"},
		Extra:       map[string]any{},
		Concurrency: 2,
	}
	// Snapshot the record shallowly for equality checks. Credentials and Extra
	// remain shared, so replacement differences can be detected while in-place
	// edits to those shared maps remain outside this comparison.
	original := *provider
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int    // scripted HTTP response code
		body   string // scripted native response text
		err    error  // scripted transport failure result
	}{
		{status: http.StatusOK, body: `{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"payg","remaining":12.5,"balance":12.5}`},
	}}
	repo := &upstreamUsageProviderRepoStub{provider: provider}
	service := newUsageContractService(repo, upstream, testUpstreamUsageConfig())
	result, err := service.QueryProvider(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, provider.ID, result.ProviderID)
	require.Equal(t, acctcore.UpstreamUsageAdapterSub2API, result.Adapter)
	require.Equal(t, 12.5, *result.Usage.Balance.Remaining)
	require.Equal(t, original.Credentials, provider.Credentials)
	require.Equal(t, original.Extra, provider.Extra)
	require.Equal(t, int64(1), service.SnapshotMetrics().Counts[acctcore.UpstreamUsageAdapterSub2API+":success"])

	upstream.mu.Lock()
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "/v1/usage", upstream.requests[0].URL.Path)
	require.Equal(t, "Bearer sk-test", upstream.requests[0].Header.Get("Authorization"))
	require.True(t, upstreamcore.HTTPUpstreamRedirectsDisabled(upstream.requests[0].Context()))
	upstream.mu.Unlock()
}

// TestUpstreamUsageServiceRejectsBedrockAndSupportsBatchErrors queries a
// Bedrock record individually and in a batch containing invalid IDs. Individual
// failure must become a per-provider batch error without failing the whole
// batch.
func TestUpstreamUsageServiceRejectsBedrockAndSupportsBatchErrors(t *testing.T) {
	provider := &acctcore.Record{ID: 1, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeBedrock, Credentials: map[string]any{"api_key": "x", "base_url": "https://example.com"}}
	repo := &upstreamUsageProviderRepoStub{provider: provider}
	service := newUsageContractService(repo, &upstreamUsageHTTPStub{}, testUpstreamUsageConfig())
	_, err := service.QueryProvider(context.Background(), 1)
	require.ErrorIs(t, err, acctcore.ErrUpstreamUsageProviderInvalid)

	_, errorsByID, err := service.QueryBatch(context.Background(), []int64{1, 0, -1})
	require.NoError(t, err)
	require.ErrorIs(t, errorsByID[1], acctcore.ErrUpstreamUsageProviderInvalid)
}

// TestUpstreamUsageServiceReportsMissingProxyAsRequestFailure supplies ProxyID
// without a loaded relation. The query must return the stable request-failed
// error for that unavailable selected proxy configuration.
func TestUpstreamUsageServiceReportsMissingProxyAsRequestFailure(t *testing.T) {
	proxyID := int64(9)
	provider := &acctcore.Record{
		ID: 9, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive,
		ProxyID: &proxyID, Credentials: map[string]any{"api_key": "key", "base_url": "https://example.com"},
	}
	service := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, &upstreamUsageHTTPStub{}, testUpstreamUsageConfig())
	_, err := service.QueryProvider(context.Background(), provider.ID)
	require.ErrorIs(t, err, acctcore.ErrUpstreamUsageRequestFailed)
}

// TestUpstreamUsageServiceTimeoutError injects a transport deadline failure
// under a caller timeout. Query must expose the stable upstream-usage timeout
// error to its caller.
func TestUpstreamUsageServiceTimeoutError(t *testing.T) {
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int    // scripted HTTP response code
		body   string // scripted native response text
		err    error  // scripted transport failure result
	}{{err: context.DeadlineExceeded}}}
	provider := &acctcore.Record{ID: 3, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "x", "base_url": "https://example.com"}}
	service := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := service.QueryProvider(ctx, provider.ID)
	require.ErrorIs(t, err, acctcore.ErrUpstreamUsageTimeout)
}

// TestUpstreamUsageServiceRejectsDisabledQueryAndOversizedResponse first
// disables querying, then supplies a response beyond the byte ceiling. Disabled
// queries must send nothing and the enabled oversized reply must return
// invalid-response.
func TestUpstreamUsageServiceRejectsDisabledQueryAndOversizedResponse(t *testing.T) {
	provider := &acctcore.Record{
		ID: 4, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive,
		Credentials: map[string]any{"api_key": "key", "base_url": "https://example.com"},
		Extra:       map[string]any{acctcore.UpstreamUsageQueryExtraKey: map[string]any{"enabled": false}},
	}
	upstream := &upstreamUsageHTTPStub{}
	service := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
	_, err := service.QueryProvider(context.Background(), provider.ID)
	require.ErrorIs(t, err, acctcore.ErrUpstreamUsageDisabled)
	require.Empty(t, upstream.requests)

	provider.Extra = nil
	upstream.responses = append(upstream.responses, struct {
		status int    // scripted HTTP response code
		body   string // scripted native response text
		err    error  // scripted transport failure result
	}{status: http.StatusOK, body: strings.Repeat("x", 512*1024+1)})
	_, err = service.QueryProvider(context.Background(), provider.ID)
	require.ErrorIs(t, err, acctcore.ErrUpstreamUsageInvalidResponse)
}

// TestUpstreamUsageServiceSingleflightWaitersCancelIndependently holds one
// shared request while a second waiter loads its identity. Cancelling the first
// must leave the second able to receive the result from one transport call.
func TestUpstreamUsageServiceSingleflightWaitersCancelIndependently(t *testing.T) {
	provider := &acctcore.Record{
		ID: 5, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "key", "base_url": "https://example.com"},
	}
	repo := &upstreamUsageProviderRepoStub{provider: provider, getEvent: make(chan struct{}, 16)}
	upstream := &blockingUpstreamUsageHTTP{
		started: make(chan struct{}),
		release: make(chan struct{}),
		body:    `{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"payg","remaining":3,"balance":3}`,
	}
	service := newUsageContractService(repo, upstream, testUpstreamUsageConfig())
	// Keep the first fetch blocked while another caller joins, then cancel the
	// first waiter before release so its lifetime cannot decide the shared
	// request's outcome.
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() {
		_, err := service.QueryProvider(firstCtx, provider.ID)
		firstErr <- err
	}()
	select {
	case <-upstream.started:
	case <-time.After(time.Second):
		t.Fatal("shared query did not start")
	}
	for len(repo.getEvent) > 0 {
		<-repo.getEvent
	}

	secondResult := make(chan *acctcore.UpstreamUsageQueryResult, 1)
	secondErr := make(chan error, 1)
	go func() {
		result, err := service.QueryProvider(context.Background(), provider.ID)
		secondResult <- result
		secondErr <- err
	}()
	select {
	case <-repo.getEvent:
	case <-time.After(time.Second):
		t.Fatal("second waiter did not load its identity snapshot")
	}
	cancelFirst()
	require.ErrorIs(t, <-firstErr, context.Canceled)
	// The lookup notification precedes singleflight admission. Allow that waiting
	// caller to join before releasing the shared response so cancellation
	// isolation is tested on one upstream operation.
	time.Sleep(10 * time.Millisecond)
	close(upstream.release)
	require.NoError(t, <-secondErr)
	require.NotNil(t, <-secondResult)
	require.Equal(t, int32(1), upstream.calls.Load())
}

// TestUpstreamUsageServiceRejectsIdentityChangeAfterQuery changes stored status
// while the response is blocked. The completed shared query must return
// identity-changed instead of accepting data under the earlier provider
// identity.
func TestUpstreamUsageServiceRejectsIdentityChangeAfterQuery(t *testing.T) {
	provider := &acctcore.Record{
		ID: 6, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "key", "base_url": "https://example.com"},
	}
	repo := &upstreamUsageProviderRepoStub{provider: provider}
	upstream := &blockingUpstreamUsageHTTP{
		started: make(chan struct{}),
		release: make(chan struct{}),
		body:    `{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"payg","remaining":3,"balance":3}`,
	}
	service := newUsageContractService(repo, upstream, testUpstreamUsageConfig())
	// Hold the response after its identity snapshot is selected, then mutate
	// stored status before releasing it so the final reread must detect a changed
	// identity.
	resultErr := make(chan error, 1)
	go func() {
		_, err := service.QueryProvider(context.Background(), provider.ID)
		resultErr <- err
	}()
	select {
	case <-upstream.started:
	case <-time.After(time.Second):
		t.Fatal("query did not start")
	}
	repo.mu.Lock()
	repo.provider.Status = "inactive"
	repo.mu.Unlock()
	close(upstream.release)
	require.ErrorIs(t, <-resultErr, acctcore.ErrUpstreamUsageIdentityChanged)
}

// TestUpstreamUsageServiceTreatsDeletionDuringQueryAsIdentityChange removes the
// stored provider while its response is blocked. Completion must report
// identity-changed after the final reread cannot find that record.
func TestUpstreamUsageServiceTreatsDeletionDuringQueryAsIdentityChange(t *testing.T) {
	provider := &acctcore.Record{
		ID: 8, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "key", "base_url": "https://example.com"},
	}
	repo := &upstreamUsageProviderRepoStub{provider: provider}
	upstream := &blockingUpstreamUsageHTTP{
		started: make(chan struct{}),
		release: make(chan struct{}),
		body:    `{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"payg","remaining":3,"balance":3}`,
	}
	service := newUsageContractService(repo, upstream, testUpstreamUsageConfig())
	// Hold the response until storage is deleted, then release it and observe
	// final identity validation rather than accepting a result for a missing
	// provider.
	resultErr := make(chan error, 1)
	go func() {
		_, err := service.QueryProvider(context.Background(), provider.ID)
		resultErr <- err
	}()
	select {
	case <-upstream.started:
	case <-time.After(time.Second):
		t.Fatal("query did not start")
	}
	repo.mu.Lock()
	repo.provider = nil
	repo.mu.Unlock()
	close(upstream.release)
	require.ErrorIs(t, <-resultErr, acctcore.ErrUpstreamUsageIdentityChanged)
}

// TestUpstreamUsageSingleflightResultIsolation coalesces two callers and
// changes one returned balance pointer. The other result and its nested usage
// balance must retain the original value from one shared fetch.
func TestUpstreamUsageSingleflightResultIsolation(t *testing.T) {
	value := &acctcore.Record{ID: 5, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://usage.example/v1"}}
	repo := &upstreamUsageProviderRepoStub{provider: value, getEvent: make(chan struct{}, 16)}
	upstream := &blockingUpstreamUsageHTTP{started: make(chan struct{}), release: make(chan struct{}), body: `{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"payg","remaining":3,"balance":3}`}
	service := newUsageContractService(repo, upstream, testUpstreamUsageConfig())
	// Start two callers around one held response and collect both results before
	// mutating either, so the assertions inspect copies from a single coalesced
	// fetch.
	results := make(chan *acctcore.UpstreamUsageQueryResult, 2)
	errs := make(chan error, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	run := func() { result, err := service.QueryProvider(ctx, value.ID); results <- result; errs <- err }
	go run()
	select {
	case <-upstream.started:
	case <-ctx.Done():
		t.Fatal("首次查询未开始")
	}
	for len(repo.getEvent) > 0 {
		<-repo.getEvent
	}
	go run()
	select {
	case <-repo.getEvent:
	case <-ctx.Done():
		t.Fatal("等待方未读取身份")
	}
	// The second identity lookup completes before its flight join. Hold the shared
	// response briefly longer so both result copies come from the same fetch.
	time.Sleep(20 * time.Millisecond)
	close(upstream.release)
	first, second := <-results, <-results
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.Equal(t, int32(1), upstream.calls.Load())
	*first.Balance.Remaining = 91
	require.Equal(t, 3.0, *second.Balance.Remaining)
	require.Equal(t, 3.0, *second.Usage.Balance.Remaining)
}

// changeIdentity advances UpdatedAt and replaces credentials once for a stored
// fixture row. The wrapper invokes it before conditional health comparison, and
// subsequent calls retain the already rotated identity.
func (r *cnDecisionRepo) changeIdentity(id int64) {
	if r.changed {
		return
	}
	r.changed = true
	// Advance the fixed fixture timestamp so the selected observation's UpdatedAt
	// no longer matches when its delayed health decision reaches comparison.
	r.providers[id].UpdatedAt = r.providers[id].UpdatedAt.Add(time.Second)
	r.providers[id].Credentials = map[string]any{"api_key": "new-admin-key", "provider_mode": acctcore.ProviderModePayG}
}

// SetTempUnschedulable rotates fixture identity before delegating an
// unconditional cooldown capture. Tests use the conditional method to prove
// that a stale decision is rejected after that same rotation point.
func (r *cnDecisionRepo) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.changeIdentity(id)
	return r.cnUsageMonitorRepo.SetTempUnschedulable(ctx, id, until, reason)
}

// SetCNUsageDecisionCAS injects identity rotation before comparing the
// requested UpdatedAt. A mismatch returns false before cooldown mutation;
// matching clear requests delegate recovery and matching pause requests
// delegate the captured suspension.
//
// changeIdentity replaces credentials and advances the stored version once,
// while the monitor still holds its earlier observation. The wrapper's tests
// control that sequence before comparison. Its mutation and version read are
// unlocked; delegated pause and clear use the base writer's lock. This injects
// stale-observation rejection rather than implementing an atomic database CAS.
func (r *cnDecisionRepo) SetCNUsageDecisionCAS(ctx context.Context, id int64, expected time.Time, until time.Time, reason string, clear bool) (bool, error) {
	r.changeIdentity(id)
	if !r.providers[id].UpdatedAt.Equal(expected) {
		return false, nil
	}
	if clear {
		return true, r.ClearTempUnschedulable(ctx, id)
	}
	return true, r.cnUsageMonitorRepo.SetTempUnschedulable(ctx, id, until, reason)
}

// GetByID returns a shallow record copy under the fixture mutex or
// provider-not-found for missing IDs. Credential and Extra maps remain shared
// with storage, so callers keep those nested values stable while using the
// snapshot.
func (r *cnUsageMonitorRepo) GetByID(_ context.Context, id int64) (*acctcore.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider := r.providers[id]
	if provider == nil {
		return nil, acctcore.ErrProviderNotFound
	}
	copy := *provider
	return &copy, nil
}

// ListByPlatform returns shallow record values in the configured platform-ID
// order under the fixture mutex. Missing IDs are omitted, while nested
// credential and Extra values remain shared with their stored records.
func (r *cnUsageMonitorRepo) ListByPlatform(_ context.Context, platform string) ([]acctcore.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := r.byPlatform[platform]
	result := make([]acctcore.Record, 0, len(ids))
	for _, id := range ids {
		if provider := r.providers[id]; provider != nil {
			result = append(result, *provider)
		}
	}
	return result, nil
}

// UpdateCNUsageMonitorSnapshotCAS captures an outer snapshot copy after
// checking UpdatedAt and the configured acceptance flag under the fixture
// mutex. Missing or mismatched rows return false, and nested snapshot values
// remain shared for assertions.
//
// Comparison and capture share one critical section, so the appended observation
// belongs to the version checked by that call. Acceptance records the requested
// snapshot in writes and leaves provider Extra unchanged. Only the outer
// snapshot object is copied; tests keep nested amounts and observation pointers
// stable while inspecting this accepted-write evidence.
func (r *cnUsageMonitorRepo) UpdateCNUsageMonitorSnapshotCAS(
	_ context.Context,
	providerID int64,
	expectedUpdatedAt time.Time,
	snapshot *acctcore.CNUsageMonitorSnapshot,
	_ string,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider := r.providers[providerID]
	if provider == nil || !provider.UpdatedAt.Equal(expectedUpdatedAt) || !r.casResult {
		return false, nil
	}
	// Capture an independent outer snapshot under the accepted version check.
	// Nested observation pointers remain shared, so assertions keep them stable
	// after publication.
	copy := *snapshot
	r.writes = append(r.writes, &copy)
	return true, nil
}

// SetTempUnschedulable captures the deadline and reason and updates any
// matching row under the mutex. Missing rows still retain capture state, and
// the stored deadline uses the supplied value through an escaped pointer.
func (r *cnUsageMonitorRepo) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pauseReason = reason
	r.pauseUntil = until
	if provider := r.providers[id]; provider != nil {
		provider.TempUnschedulableUntil = &until
		provider.TempUnschedulableReason = reason
	}
	return nil
}

// ClearTempUnschedulable counts recovery and clears a matching row's deadline
// and reason under the fixture mutex. Missing rows still count the requested
// clear and return success.
func (r *cnUsageMonitorRepo) ClearTempUnschedulable(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearCalls++
	if provider := r.providers[id]; provider != nil {
		provider.TempUnschedulableUntil = nil
		provider.TempUnschedulableReason = ""
	}
	return nil
}

// UpdateExtra counts generic Extra writes that periodic monitor tests expect to
// skip.
func (r *cnUsageMonitorRepo) UpdateExtra(_ context.Context, _ int64, _ map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateExtraCalls++
	return nil
}

// Do delegates monitor request capture to DoWithTLS with a nil fingerprint
// profile.
func (h *cnUsageMonitorHTTP) Do(req *http.Request, proxyURL string, providerID int64, concurrency int) (*http.Response, error) {
	return h.DoWithTLS(req, proxyURL, providerID, concurrency, nil)
}

// DoWithTLS captures a cloned request and response configuration under the
// fixture mutex, then optionally signals startup or waits for cancellation. A
// blocked call returns its context error; zero unblocked status selects HTTP
// 200, and callers close response bodies.
//
// Capture releases the mutex before startup notification or the blocked wait,
// allowing other fixture calls to proceed while cancellation remains pending.
// The captured block, status, and body values govern this call even if later
// configuration changes. Startup sends can be dropped, so they provide a
// best-effort entry signal while the request context controls blocked completion.
func (h *cnUsageMonitorHTTP) DoWithTLS(
	req *http.Request,
	_ string,
	_ int64,
	_ int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	h.mu.Lock()
	h.calls++
	h.requests = append(h.requests, req.Clone(req.Context()))
	started := h.started
	block := h.block
	status := h.status
	body := h.body
	h.mu.Unlock()
	// Publish startup without waiting after capture, so a blocked probe can still
	// observe cancellation even when its test receiver is not ready.
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// TryAcquireLeaderLock counts the lease attempt and returns the configured
// acceptance flag. It ignores key, owner, and TTL, leaving lease ownership
// behavior to other tests while controlling this runner cycle.
func (l *cnUsageMonitorLeaderLock) TryAcquireLeaderLock(context.Context, string, string, time.Duration) (bool, error) {
	l.calls++
	return l.acquired, nil
}

// ReleaseLeaderLock accepts fixture cleanup without retaining any leader
// ownership state.
func (*cnUsageMonitorLeaderLock) ReleaseLeaderLock(context.Context, string, string) error { return nil }

// newCNUsageMonitorProvider creates an active schedulable API-key record with a
// fixed UpdatedAt and supplied platform and mode. Fresh credentials and Extra
// maps let each monitor test configure its identity independently.
func newCNUsageMonitorProvider(id int64, platform, mode string) *acctcore.Record {
	return &acctcore.Record{
		LoadLocation: time.LoadLocation, ID: id,
		Platform:    platform,
		Type:        capability.ProviderTypeAPIKey,
		Status:      billing.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		UpdatedAt:   time.Date(2026, 8, 23, 1, 0, 0, 0, time.UTC),
		Credentials: map[string]any{
			"api_key":       "sk-test",
			"provider_mode": mode,
		},
		Extra: map[string]any{},
	}
}

// newCNUsageMonitorForTest combines native usage querying and periodic
// monitoring with supplied configuration callbacks. It retains the fixture
// store and transport, and callbacks can alter the monitor options before
// construction.
func newCNUsageMonitorForTest(repo *cnUsageMonitorRepo, upstream httpclient.UpstreamTransport, cfg *cnQueryFixtureOptions, configure ...func(*acctcore.CNMonitorOptions)) *acctcore.CNUsageMonitor {
	usage := newCNUsageFixture(repo, upstream, cfg, nil)
	return newCNMonitorFixture(repo, usage, cfg, configure...)
}

// newCNQueryFixtureOptions creates configured query policy that permits fixture
// HTTP roots. Other policy and monitor fields retain zero values for tests to
// select explicitly or for production defaults to interpret.
func newCNQueryFixtureOptions() *cnQueryFixtureOptions {
	return &cnQueryFixtureOptions{Policy: egress.UsageURLPolicy{Configured: true, AllowInsecureHTTP: true}}
}

// newCNUsageFixture binds native adapters to repository, transport, optional
// TLS profiles, and copied query policy. Readiness requires both repository and
// transport, while missing optional profiles leave request fingerprint
// selection unset.
func newCNUsageFixture(repo acctcore.UpstreamUsageReader, transport httpclient.UpstreamTransport, cfg *cnQueryFixtureOptions, tls *egressprovider.TLSProfiles) *acctcore.UpstreamUsageService {
	options := UsageHTTPOptions{Available: repo != nil && transport != nil}
	if cfg != nil {
		options.Policy = cfg.Policy
	}
	if transport != nil {
		options.Do = transport.DoWithTLS
	}
	if tls != nil {
		options.ResolveTLS = tls.ResolveRequestTLS
	}
	return acctcore.NewUpstreamUsageService(repo, NewUpstreamUsageHTTPExecution(options), acctcore.UpstreamUsageOptions{Now: time.Now})
}

// newCNMonitorFixture constructs periodic monitoring with one-second budgets
// and fixture identity. Supplied monitor fields replace initial enabled,
// interval, concurrency, and threshold values; configuration callbacks run last
// before the production constructor retains options.
func newCNMonitorFixture(repo acctcore.CNMonitorStore, queries *acctcore.UpstreamUsageService, cfg *cnQueryFixtureOptions, configure ...func(*acctcore.CNMonitorOptions)) *acctcore.CNUsageMonitor {
	options := acctcore.CNMonitorOptions{Now: time.Now, InstanceID: "fixture-owner", RoundTimeout: time.Second, ProbeTimeout: time.Second, BalanceThreshold: 0.5}
	if cfg != nil {
		options.Enabled = cfg.Monitor.Enabled
		options.Interval = cfg.Monitor.Interval
		options.Concurrency = cfg.Monitor.Concurrency
		options.BalanceThreshold = cfg.Monitor.BalanceThreshold
		options.HostPolicy = egress.MonitorHostPolicy{Enabled: cfg.Policy.Enabled, AllowInsecureHTTP: cfg.Policy.AllowInsecureHTTP, AllowPrivate: cfg.Policy.AllowPrivateHosts, AllowedHosts: cfg.Policy.UpstreamHosts}
	}
	for _, apply := range configure {
		apply(&options)
	}
	return acctcore.NewCNUsageMonitor(repo, queries, options)
}

// SetCNUsageDecisionCAS compares the stored UpdatedAt under the fixture mutex
// before delegating pause or clear. A mismatch returns false, while accepted
// comparison releases the lock before the delegated writer acquires its own
// lock.
//
// This fixture therefore has separate comparison and mutation phases rather
// than one atomic database operation. The wrapper identity test injects
// rotation before this decision boundary, and its own conditional method
// rejects that change. Tests using this base method inspect requested health
// effects with controlled record state; production conditional-write atomicity
// is not established by this helper.
func (r *cnUsageMonitorRepo) SetCNUsageDecisionCAS(ctx context.Context, id int64, expected, until time.Time, reason string, clear bool) (bool, error) {
	r.mu.Lock()
	matches := r.providers[id] != nil && r.providers[id].UpdatedAt.Equal(expected)
	r.mu.Unlock()
	if !matches {
		return false, nil
	}
	if clear {
		return true, r.ClearTempUnschedulable(ctx, id)
	}
	return true, r.SetTempUnschedulable(ctx, id, until, reason)
}

// GetByID returns a shallow copy of the single fixture provider while holding
// its mutex and ignores the requested ID. Missing providers return not-found;
// optional lookup notifications are nonblocking and nested values remain
// shared.
func (s *upstreamUsageProviderRepoStub) GetByID(_ context.Context, _ int64) (*acctcore.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider == nil {
		return nil, acctcore.ErrProviderNotFound
	}
	// Lookup notifications coordinate test waiters rather than storage itself.
	// Send without waiting so a full assertion channel cannot block provider
	// identity reads.
	if s.getEvent != nil {
		select {
		case s.getEvent <- struct{}{}:
		default:
		}
	}
	copy := *s.provider
	return &copy, nil
}

// Do delegates the shared-query gate to DoWithTLS with a nil profile.
func (s *blockingUpstreamUsageHTTP) Do(req *http.Request, proxyURL string, providerID int64, concurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, providerID, concurrency, nil)
}

// DoWithTLS counts the request, signals first startup once, and waits for
// release or request cancellation. Started is configured before calls, returned
// response bodies belong to callers, and successful release supplies the
// scripted body.
//
// The request's own context controls the shared fetch, independently of HTTP
// waiters that can stop waiting earlier. Keeping release and cancellation in
// one select makes both outcomes available while the response is held. Tests
// use this gate to separate one shared upstream operation from multiple caller
// lifetimes and inspect the service's cancellation and result-copy behavior.
func (s *blockingUpstreamUsageHTTP) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	s.calls.Add(1)
	// Close startup once before waiting, allowing concurrent waiters to observe
	// one shared fetch without another call attempting to close the same signal.
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(s.body))}, nil
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}

// Do delegates scripted usage transport with empty proxy and zero provider
// parameters.
func (s *upstreamUsageHTTPStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return s.DoWithTLS(req, "", 0, 0, nil)
}

// DoWithTLS captures a cloned request and consumes one scripted outcome under
// the transport mutex. Scripted errors return before response construction, an
// empty queue returns HTTP 404 with empty JSON, and callers close returned
// bodies.
//
// Appending the request and removing its scripted outcome share one critical
// section, assigning concurrent callers distinct positions in the capture and
// response sequence. Error outcomes also consume their position. An exhausted
// script still records the request, letting tests distinguish attempted
// transport from a preparation failure that never reached this fixture.
func (s *upstreamUsageHTTPStub) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req.Clone(req.Context()))
	if len(s.responses) == 0 {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	if response.err != nil {
		return nil, response.err
	}
	return &http.Response{StatusCode: response.status, Body: io.NopCloser(strings.NewReader(response.body))}, nil
}

// testUpstreamUsageConfig returns configured URL policy permitting HTTP roots
// used by usage fixtures.
func testUpstreamUsageConfig() egress.UsageURLPolicy {
	return egress.UsageURLPolicy{Configured: true, AllowInsecureHTTP: true}
}

// mustJSONMarshal returns JSON bytes or fails the test on encoding errors.
func mustJSONMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

// newUsageContractService binds the production query service to the supplied
// reader, transport, and URL policy. Readiness requires both dependencies;
// absent transport leaves the sender unset, and the native registry owns
// request preparation.
func newUsageContractService(reader acctcore.UpstreamUsageReader, transport interface {
	// DoWithTLS sends a prepared native usage request using captured provider
	// transport values. It returns a transport error or response whose body the
	// adapter closes during query execution.
	DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error)
}, policy egress.UsageURLPolicy,
) *acctcore.UpstreamUsageService {
	options := UsageHTTPOptions{Available: reader != nil && transport != nil, Policy: policy}
	if transport != nil {
		options.Do = transport.DoWithTLS
	}
	return acctcore.NewUpstreamUsageService(reader, NewUpstreamUsageHTTPExecution(options), acctcore.UpstreamUsageOptions{Now: time.Now})
}
