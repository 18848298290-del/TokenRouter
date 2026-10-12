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

// cnDecisionRepo simulates credential replacement after identity reread and before a health write.
type cnDecisionRepo struct {
	*cnUsageMonitorRepo
	changed bool
}

type cnUsageMonitorRepo struct {
	mu               sync.Mutex
	providers        map[int64]*acctcore.Record
	byPlatform       map[string][]int64
	writes           []*acctcore.CNUsageMonitorSnapshot
	casResult        bool
	pauseReason      string
	pauseUntil       time.Time
	clearCalls       int
	updateExtraCalls int
}

type cnUsageMonitorHTTP struct {
	mu       sync.Mutex
	calls    int
	requests []*http.Request
	status   int
	body     string
	started  chan struct{}
	block    bool
}

type cnUsageMonitorLeaderLock struct {
	acquired bool
	calls    int
}

// cnQueryFixtureOptions supplies query URL policy and monitoring limits.
type cnQueryFixtureOptions struct {
	Policy  egress.UsageURLPolicy
	Monitor acctcore.CNMonitorOptions
}

type upstreamUsageProviderRepoStub struct {
	mu       sync.Mutex
	provider *acctcore.Record
	getEvent chan struct{}
}

type blockingUpstreamUsageHTTP struct {
	started chan struct{}
	release chan struct{}
	body    string
	once    sync.Once
	calls   atomic.Int32
}

type upstreamUsageHTTPStub struct {
	mu        sync.Mutex
	requests  []*http.Request
	responses []struct {
		status int
		body   string
		err    error
	}
}

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

func TestCNUpstreamUsageAdaptersPreserveConfiguredHostAndNormalizeResults(t *testing.T) {
	tests := []struct {
		name        string
		provider    *acctcore.Record
		response    string
		wantAdapter string
		wantPath    string
		wantQuery   string
		wantAuth    string
		wantOrg     string
		wantProject string
		assert      func(*testing.T, *acctcore.UpstreamUsageQueryResult)
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
				status int
				body   string
				err    error
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

func TestZivvUsageQueryUsesVersionedBalanceEndpoint(t *testing.T) {
	provider := &acctcore.Record{
		ID: 17, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-zivv", "base_url": "https://zivv.example/"},
		Extra:       map[string]any{acctcore.UpstreamUsageQueryExtraKey: map[string]any{"adapter": acctcore.UpstreamUsageAdapterZivv}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
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
		status int
		body   string
		err    error
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

func TestNewAPIUsageEndpointMissingIsUnsupported(t *testing.T) {
	provider := &acctcore.Record{
		ID: 12, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive,
		Credentials: map[string]any{"api_key": "sk-new-api", "base_url": "https://new-api.example/v1"},
		Extra:       map[string]any{acctcore.UpstreamUsageQueryExtraKey: map[string]any{"adapter": acctcore.UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{
		{status: http.StatusOK, body: `{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000}}`},
		{status: http.StatusNotFound, body: `{}`},
	}}
	service := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
	_, err := service.QueryProvider(context.Background(), provider.ID)
	require.ErrorIs(t, err, acctcore.ErrUpstreamUsageUnsupported)
}

// TestDeepSeekBalanceAdapterRejectsMalformedPayloads rejects missing fields and invalid numbers while accepting
// zero balance.
func TestDeepSeekBalanceAdapterRejectsMalformedPayloads(t *testing.T) {
	cases := []struct {
		name string
		body string
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
				status int
				body   string
				err    error
			}{{status: http.StatusOK, body: tc.body}}}
			svc := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
			_, err := svc.QueryProvider(context.Background(), provider.ID)
			require.ErrorIs(t, err, acctcore.ErrUpstreamUsageInvalidResponse)
		})
	}
}

func TestDeepSeekBalanceAdapterPreservesValidZeroBalance(t *testing.T) {
	provider := &acctcore.Record{
		ID: 902, Platform: capability.PlatformDeepseek, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Concurrency: 1,
		Credentials: map[string]any{"api_key": "deepseek-key", "base_url": "https://relay.example/anthropic", "api_protocol": acctcore.APIProtocolAnthropic},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{{status: http.StatusOK, body: `{"is_available":false,"balance_infos":[{"currency":"CNY","total_balance":"0"}]}`}}}
	svc := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())

	result, err := svc.QueryProvider(context.Background(), provider.ID)
	require.NoError(t, err)
	require.NotNil(t, result.Usage)
	require.NotNil(t, result.Usage.Balance)
	require.Zero(t, *result.Usage.Balance.Remaining)
	require.False(t, *result.Usage.Available)
}

func TestNewAPIUsageContinuesWhenStatusProbeFails(t *testing.T) {
	provider := &acctcore.Record{
		ID: 13, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive,
		Credentials: map[string]any{"api_key": "sk-new-api", "base_url": "https://new-api.example/v1"},
		Extra:       map[string]any{acctcore.UpstreamUsageQueryExtraKey: map[string]any{"adapter": acctcore.UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
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
		status int
		body   string
		err    error
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
		status int
		body   string
		err    error
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

func TestNewAPIUsageRequiresWalletInsteadOfTokenQuota(t *testing.T) {
	provider := &acctcore.Record{
		ID: 15, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive,
		Credentials: map[string]any{"api_key": "sk-new-api", "base_url": "https://new-api.example/v1"},
		Extra:       map[string]any{acctcore.UpstreamUsageQueryExtraKey: map[string]any{"adapter": acctcore.UpstreamUsageAdapterNewAPI}},
	}
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{
		{status: http.StatusOK, body: `{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000}}`},
		{status: http.StatusOK, body: `{"code":true,"data":{"object":"token_usage","name":"tf","total_granted":-1,"total_used":1,"total_available":-2,"unlimited_quota":true,"expires_at":0}}`},
		{status: http.StatusOK, body: `<html>frontend</html>`},
	}}
	service := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
	_, err := service.QueryProvider(context.Background(), provider.ID)
	require.ErrorIs(t, err, acctcore.ErrUpstreamUsageWalletUnavailable)
}

func TestUpstreamUsageServiceQueriesAPIKeyWithoutMutatingProvider(t *testing.T) {
	provider := &acctcore.Record{
		ID:          7,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://usage.example/v1"},
		Extra:       map[string]any{},
		Concurrency: 2,
	}
	original := *provider
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
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

func TestUpstreamUsageServiceTimeoutError(t *testing.T) {
	upstream := &upstreamUsageHTTPStub{responses: []struct {
		status int
		body   string
		err    error
	}{{err: context.DeadlineExceeded}}}
	provider := &acctcore.Record{ID: 3, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Credentials: map[string]any{"api_key": "x", "base_url": "https://example.com"}}
	service := newUsageContractService(&upstreamUsageProviderRepoStub{provider: provider}, upstream, testUpstreamUsageConfig())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := service.QueryProvider(ctx, provider.ID)
	require.ErrorIs(t, err, acctcore.ErrUpstreamUsageTimeout)
}

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
		status int
		body   string
		err    error
	}{status: http.StatusOK, body: strings.Repeat("x", 512*1024+1)})
	_, err = service.QueryProvider(context.Background(), provider.ID)
	require.ErrorIs(t, err, acctcore.ErrUpstreamUsageInvalidResponse)
}

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
	// Give the waiting caller time to join singleflight after its preliminary read.
	time.Sleep(10 * time.Millisecond)
	close(upstream.release)
	require.NoError(t, <-secondErr)
	require.NotNil(t, <-secondResult)
	require.Equal(t, int32(1), upstream.calls.Load())
}

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

// TestUpstreamUsageSingleflightResultIsolation checks that coalesced queries return independently mutable results.
func TestUpstreamUsageSingleflightResultIsolation(t *testing.T) {
	value := &acctcore.Record{ID: 5, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey, Status: acctcore.StatusActive, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://usage.example/v1"}}
	repo := &upstreamUsageProviderRepoStub{provider: value, getEvent: make(chan struct{}, 16)}
	upstream := &blockingUpstreamUsageHTTP{started: make(chan struct{}), release: make(chan struct{}), body: `{"isValid":true,"mode":"unrestricted","unit":"USD","planName":"payg","remaining":3,"balance":3}`}
	service := newUsageContractService(repo, upstream, testUpstreamUsageConfig())
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
	// Let both callers join singleflight before releasing the shared request.
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

func (r *cnDecisionRepo) changeIdentity(id int64) {
	if r.changed {
		return
	}
	r.changed = true
	r.providers[id].UpdatedAt = r.providers[id].UpdatedAt.Add(time.Second)
	r.providers[id].Credentials = map[string]any{"api_key": "new-admin-key", "provider_mode": acctcore.ProviderModePayG}
}

func (r *cnDecisionRepo) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.changeIdentity(id)
	return r.cnUsageMonitorRepo.SetTempUnschedulable(ctx, id, until, reason)
}

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
	copy := *snapshot
	r.writes = append(r.writes, &copy)
	return true, nil
}

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

func (r *cnUsageMonitorRepo) UpdateExtra(_ context.Context, _ int64, _ map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateExtraCalls++
	return nil
}

func (h *cnUsageMonitorHTTP) Do(req *http.Request, proxyURL string, providerID int64, concurrency int) (*http.Response, error) {
	return h.DoWithTLS(req, proxyURL, providerID, concurrency, nil)
}

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

func (l *cnUsageMonitorLeaderLock) TryAcquireLeaderLock(context.Context, string, string, time.Duration) (bool, error) {
	l.calls++
	return l.acquired, nil
}

func (*cnUsageMonitorLeaderLock) ReleaseLeaderLock(context.Context, string, string) error { return nil }

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

func newCNUsageMonitorForTest(repo *cnUsageMonitorRepo, upstream httpclient.UpstreamTransport, cfg *cnQueryFixtureOptions, configure ...func(*acctcore.CNMonitorOptions)) *acctcore.CNUsageMonitor {
	usage := newCNUsageFixture(repo, upstream, cfg, nil)
	return newCNMonitorFixture(repo, usage, cfg, configure...)
}

func newCNQueryFixtureOptions() *cnQueryFixtureOptions {
	return &cnQueryFixtureOptions{Policy: egress.UsageURLPolicy{Configured: true, AllowInsecureHTTP: true}}
}

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

func (s *upstreamUsageProviderRepoStub) GetByID(_ context.Context, _ int64) (*acctcore.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider == nil {
		return nil, acctcore.ErrProviderNotFound
	}
	if s.getEvent != nil {
		select {
		case s.getEvent <- struct{}{}:
		default:
		}
	}
	copy := *s.provider
	return &copy, nil
}

func (s *blockingUpstreamUsageHTTP) Do(req *http.Request, proxyURL string, providerID int64, concurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, providerID, concurrency, nil)
}

func (s *blockingUpstreamUsageHTTP) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	s.calls.Add(1)
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(s.body))}, nil
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}

func (s *upstreamUsageHTTPStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return s.DoWithTLS(req, "", 0, 0, nil)
}

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

func testUpstreamUsageConfig() egress.UsageURLPolicy {
	return egress.UsageURLPolicy{Configured: true, AllowInsecureHTTP: true}
}

func mustJSONMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

// newUsageContractService builds a usage service with a transport that records requests and cancellation.
func newUsageContractService(reader acctcore.UpstreamUsageReader, transport interface {
	DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error)
}, policy egress.UsageURLPolicy,
) *acctcore.UpstreamUsageService {
	options := UsageHTTPOptions{Available: reader != nil && transport != nil, Policy: policy}
	if transport != nil {
		options.Do = transport.DoWithTLS
	}
	return acctcore.NewUpstreamUsageService(reader, NewUpstreamUsageHTTPExecution(options), acctcore.UpstreamUsageOptions{Now: time.Now})
}
