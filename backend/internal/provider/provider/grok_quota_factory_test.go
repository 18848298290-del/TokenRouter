package provider

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

// grok_quota_factory_test.go checks billing GETs, active Responses POSTs and
// provider observation writes. These cases exercise grok_quota_factory.go's
// bindings with request-recording transports, checking query identity, header
// storage, local usage, media eligibility and the distinction between billing
// failure and inference rate limiting.
//
// The concurrent flight case holds billing transport while a second caller
// approaches the same claim and an active probe proceeds independently. Separate
// result channels join both billing callers before request counts are inspected.
// A short scheduling interval follows the second caller's entry signal; that
// signal announces its goroutine rather than proving it already joined the
// flight. Logger capture remains in the serial redaction case, with the original
// process logger restored after error and log assertions.

// TestGrokQuotaServiceProbeUsageDoesNotRetryResponsesPost supplies a 502
// active response
// followed by unused success. It checks the stable probe error and exactly one
// Responses POST, distinguishing active probing from billing GET retries.
func TestGrokQuotaServiceProbeUsageDoesNotRetryResponsesPost(t *testing.T) {
	provider := healthyGrokQuotaOAuthProvider(405)
	repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}}
	upstream := &grokQuotaSequenceUpstream{steps: []grokQuotaUpstreamStep{
		{status: http.StatusBadGateway, body: `cloudflare failure`},
		{status: http.StatusOK, body: `{"id":"unexpected_retry"}`},
	}}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	result, err := svc.ProbeUsage(context.Background(), provider.ID)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, "GROK_QUOTA_PROBE_UPSTREAM_ERROR", apperror.Reason(err))
	requests := upstream.snapshotRequests()
	require.Len(t, requests, 1)
	require.Equal(t, http.MethodPost, requests[0].Method)
	require.Equal(t, "/v1/responses", requests[0].URL.Path)
}

// TestGrokQuotaServiceProbeUsageStoresHeaders returns request/token limit headers
// with a successful active response. It checks observed values and timestamps,
// CLI request identity, the minimal streaming payload and stored snapshot data.
func TestGrokQuotaServiceProbeUsageStoresHeaders(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(42)
	repo := &grokQuotaProviderRepo{
		grokQuotaReadStore: &grokQuotaReadStore{
			providersByID: map[int64]*providercore.Record{42: provider},
		},
	}
	upstream := &grokQuotaHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"X-Ratelimit-Limit-Requests":     []string{"10"},
			"X-Ratelimit-Remaining-Requests": []string{"7"},
			"X-Ratelimit-Reset-Requests":     []string{"2000000000"},
			"X-Ratelimit-Limit-Tokens":       []string{"1000"},
			"X-Ratelimit-Remaining-Tokens":   []string{"900"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_probe"}`)),
	}}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	result, err := svc.ProbeUsage(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, result.StatusCode)
	require.Equal(t, "grok-4.5", result.Model)
	require.True(t, result.HeadersObserved)
	require.NotNil(t, result.Snapshot)
	require.True(t, result.Snapshot.HeadersObserved)
	require.Equal(t, "active_probe", result.Snapshot.ObservationSource)
	require.NotEmpty(t, result.Snapshot.LastProbeAt)
	require.NotEmpty(t, result.Snapshot.LastHeadersSeenAt)
	require.NotNil(t, result.Snapshot.Requests)
	require.EqualValues(t, 10, *result.Snapshot.Requests.Limit)
	require.EqualValues(t, 7, *result.Snapshot.Requests.Remaining)
	require.Equal(t, "https://cli-chat-proxy.grok.com/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, xai.CLIClientVersion, upstream.lastReq.Header.Get("X-Grok-Client-Version"))
	require.Equal(t, "application/json, text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "hi", gjson.GetBytes(upstream.lastBody, "input").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.False(t, gjson.GetBytes(upstream.lastBody, "max_output_tokens").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Exists())
	require.NotNil(t, repo.updates[42]["grok_usage_snapshot"])
}

// TestGrokQuotaServiceProbeUsageIgnoresProviderGrokMapping adds provider
// aliases beside
// the probe configuration. It checks both reported model and request JSON still
// use the factory's fixed quota probe model, with no mapped Composer target.
func TestGrokQuotaServiceProbeUsageIgnoresProviderGrokMapping(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(47)
	provider.Credentials["model_mapping"] = map[string]any{
		"grok":          "grok-composer",
		"grok-composer": "grok-composer-2.5-fast",
	}
	repo := &grokQuotaProviderRepo{
		grokQuotaReadStore: &grokQuotaReadStore{
			providersByID: map[int64]*providercore.Record{47: provider},
		},
	}
	upstream := &grokQuotaHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_probe"}`)),
	}}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	result, err := svc.ProbeUsage(context.Background(), 47)
	require.NoError(t, err)
	require.Equal(t, "grok-4.5", result.Model)
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.NotContains(t, string(upstream.lastBody), "grok-composer")
}

// TestGrokQuotaServiceProbeUsageReportsProbeModelOnUpstreamError returns a
// model error
// from the active endpoint. It checks the stable error category and diagnostic
// retention of the configured quota probe model.
func TestGrokQuotaServiceProbeUsageReportsProbeModelOnUpstreamError(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(48)
	repo := &grokQuotaProviderRepo{
		grokQuotaReadStore: &grokQuotaReadStore{
			providersByID: map[int64]*providercore.Record{48: provider},
		},
	}
	upstream := &grokQuotaHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"code":"invalid-argument","error":"Model not found"}`)),
	}}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	_, err := svc.ProbeUsage(context.Background(), 48)
	require.Error(t, err)
	require.Equal(t, "GROK_QUOTA_PROBE_UPSTREAM_ERROR", apperror.Reason(err))
	require.Contains(t, apperror.Message(err), `probe model "grok-4.5"`)
}

// TestGrokQuotaServiceProbeUsageRedactsUpstreamErrorBodyFromErrorAndLogs
// injects secret
// text into a failed active response. It checks public error and captured logs
// retain model/category diagnostics while excluding the body text and secret.
func TestGrokQuotaServiceProbeUsageRedactsUpstreamErrorBodyFromErrorAndLogs(t *testing.T) {
	const upstreamSecret = "upstream-secret-refresh-token"
	provider := healthyGrokQuotaOAuthProvider(49)
	repo := &grokQuotaProviderRepo{
		grokQuotaReadStore: &grokQuotaReadStore{
			providersByID: map[int64]*providercore.Record{49: provider},
		},
	}
	upstream := &grokQuotaHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{},
		Body: io.NopCloser(strings.NewReader(
			`{"error":"` + upstreamSecret + `","detail":"credential rejected"}`,
		)),
	}}
	svc := newGrokQuotaFixture(
		repo,
		nil,
		&providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()},
		upstream,
		nil,
	)

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	// Restore the process logger after the serial redaction observation so
	// subsequent
	// quota queries do not write through this test-owned capture buffer and
	// handler.
	defer slog.SetDefault(previousLogger)

	_, err := svc.ProbeUsage(context.Background(), provider.ID)
	require.Error(t, err)
	require.Equal(t, "GROK_QUOTA_PROBE_UPSTREAM_ERROR", apperror.Reason(err))
	require.Contains(t, apperror.Message(err), `probe model "grok-4.5"`)
	require.NotContains(t, err.Error(), upstreamSecret)
	require.NotContains(t, apperror.Message(err), upstreamSecret)
	require.Contains(t, logs.String(), "GROK_QUOTA_PROBE_UPSTREAM_ERROR")
	require.NotContains(t, logs.String(), upstreamSecret)
	require.NotContains(t, logs.String(), "credential rejected")
	require.Equal(t, xai.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
}

// TestGrokQuotaServiceProbeUsageLoadsProxyWhenProviderEdgeMissing provides a
// proxy ID
// without a loaded proxy object. It checks one proxy repository lookup and the
// resolved proxy URL supplied to the active transport request.
func TestGrokQuotaServiceProbeUsageLoadsProxyWhenProviderEdgeMissing(t *testing.T) {
	t.Parallel()

	proxyID := int64(7)
	provider := healthyGrokQuotaOAuthProvider(46)
	provider.ProxyID = &proxyID
	repo := &grokQuotaProviderRepo{
		grokQuotaReadStore: &grokQuotaReadStore{
			providersByID: map[int64]*providercore.Record{46: provider},
		},
	}
	proxyRepo := &grokQuotaProxyRepo{
		proxies: map[int64]*egress.Proxy{
			proxyID: {
				ID:       proxyID,
				Protocol: "http",
				Host:     "proxy.test",
				Port:     3128,
			},
		},
	}
	upstream := &grokQuotaHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_probe"}`)),
	}}
	svc := newGrokQuotaFixture(repo, proxyRepo, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	_, err := svc.ProbeUsage(context.Background(), 46)
	require.NoError(t, err)
	require.Equal(t, 1, proxyRepo.calls)
	require.Equal(t, "http://proxy.test:3128", upstream.lastProxyURL)
}

// TestGrokQuotaServiceProbeUsageStoresNoHeadersState uses a successful headerless
// probe after an observed provider limit. It checks a stored no-header observation
// and recovery cleanup receives the original limit timestamp pair.
func TestGrokQuotaServiceProbeUsageStoresNoHeadersState(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(45)
	observedResetAt := time.Now().Add(-time.Second).UTC().Truncate(time.Second)
	observedLimitedAt := observedResetAt.Add(-(10 * time.Minute))
	provider.RateLimitedAt = &observedLimitedAt
	provider.RateLimitResetAt = &observedResetAt
	repo := &grokQuotaProviderRepo{
		grokQuotaReadStore: &grokQuotaReadStore{
			providersByID: map[int64]*providercore.Record{45: provider},
		},
		recoveryClearResult: true,
	}
	upstream := &grokQuotaHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_probe"}`)),
	}}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	result, err := svc.ProbeUsage(context.Background(), 45)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, result.StatusCode)
	require.False(t, result.HeadersObserved)
	require.NotNil(t, result.Snapshot)
	require.False(t, result.Snapshot.HeadersObserved)
	require.Equal(t, "active_probe", result.Snapshot.ObservationSource)
	require.NotEmpty(t, result.Snapshot.LastProbeAt)
	require.Empty(t, result.Snapshot.LastHeadersSeenAt)

	stored, ok := repo.updates[45]["grok_usage_snapshot"].(*xai.QuotaSnapshot)
	require.True(t, ok)
	require.False(t, stored.HeadersObserved)
	require.Equal(t, http.StatusOK, stored.StatusCode)
	require.Equal(t, 1, repo.recoveryClearCalls)
	require.Equal(t, observedLimitedAt, repo.recoveryObservedAt)
	require.Equal(t, observedResetAt, repo.recoveryObservedReset)
}

// TestGrokQuotaServiceProbeUsageDoesNotOverwriteSnapshotOnUnauthorized starts
// with a
// successful snapshot before a 401 query. It checks query failure, zero snapshot
// writes and the same previous snapshot retained in the provider record.
func TestGrokQuotaServiceProbeUsageDoesNotOverwriteSnapshotOnUnauthorized(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(44)
	previous := &xai.QuotaSnapshot{StatusCode: http.StatusOK, HeadersObserved: true}
	provider.Extra = map[string]any{"grok_usage_snapshot": previous}
	repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}}
	upstream := &grokQuotaHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)),
	}}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	_, err := svc.ProbeUsage(context.Background(), provider.ID)
	require.Error(t, err)
	require.Equal(t, 0, repo.updateCalls)
	require.Same(t, previous, provider.Extra["grok_usage_snapshot"])
}

// TestGrokQuotaServiceProbeUsageReturnsRateLimitedSnapshot supplies a 429 with
// Retry-After. It checks the query result retains quota timing, writes one
// provider limit and issues no temporary-unschedulable write.
func TestGrokQuotaServiceProbeUsageReturnsRateLimitedSnapshot(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(43)
	repo := &grokQuotaProviderRepo{
		grokQuotaReadStore: &grokQuotaReadStore{
			providersByID: map[int64]*providercore.Record{43: provider},
		},
	}
	upstream := &grokQuotaHTTPRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"45"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	result, err := svc.ProbeUsage(context.Background(), 43)
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, result.StatusCode)
	require.NotNil(t, result.Snapshot)
	require.NotNil(t, result.Snapshot.RetryAfterSeconds)
	require.Equal(t, 45, *result.Snapshot.RetryAfterSeconds)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Equal(t, provider.ID, repo.lastRateLimitedID)
	// Allow one second between header observation and this reset-time assertion.
	require.WithinDuration(t, time.Now().Add(45*time.Second), repo.lastRateLimitResetAt, time.Second)
	require.Zero(t, repo.tempUnschedCalls)
}

// TestGrokQuotaServiceQueryQuotaFreeFallsBackToGrok45 uses planless billing
// and local
// token statistics. It checks hybrid source, retained billing/local usage and
// exactly one minimal grok-4.5 active request after the two billing queries.
func TestGrokQuotaServiceQueryQuotaFreeFallsBackToGrok45(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(51)
	// A fresh model cache keeps background catalogue sync out of the billing
	// and active-probe request counts.
	provider.Extra = map[string]any{providercore.GrokObservedModelsExtraKey: map[string]any{
		"models": []string{"grok-4.5"}, "fetched_at": time.Now().UTC().Format(time.RFC3339),
	}}
	repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}}
	upstream := &grokHybridUpstream{}
	usageRepo := &grokQuotaUsageLogRepo{stats: &providercore.WindowStats{Tokens: 1_000_000}}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil, usageRepo)

	result, err := svc.QueryQuota(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, "hybrid_probe", result.Source)
	require.Equal(t, "grok-4.5", result.Model)
	require.NotNil(t, result.Billing)
	require.Nil(t, result.Billing.UsagePercent)
	require.NotNil(t, result.LocalUsage24h)
	require.EqualValues(t, 1_000_000, result.LocalUsage24h.Tokens)
	require.Equal(t, 1, usageRepo.calls)
	// Allow one second for the query and assertion clocks to observe the window.
	require.WithinDuration(t, time.Now().UTC().Add(-24*time.Hour), usageRepo.startTimes[0], time.Second)
	require.NotNil(t, result.Snapshot)
	require.NotNil(t, result.Snapshot.Tokens)
	require.EqualValues(t, 2_000_000, *result.Snapshot.Tokens.Limit)
	require.True(t, result.HeadersObserved)

	requests, bodies := upstream.snapshot()
	require.Len(t, requests, 3)
	responseCalls := 0
	for i, req := range requests {
		if req.URL.Path != "/v1/responses" {
			continue
		}
		responseCalls++
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, "application/json, text/event-stream", req.Header.Get("Accept"))
		require.Equal(t, "grok-4.5", gjson.GetBytes(bodies[i], "model").String())
		require.Equal(t, "hi", gjson.GetBytes(bodies[i], "input").String())
		require.True(t, gjson.GetBytes(bodies[i], "stream").Bool())
		require.False(t, gjson.GetBytes(bodies[i], "max_output_tokens").Exists())
		require.False(t, gjson.GetBytes(bodies[i], "store").Exists())
	}
	require.Equal(t, 1, responseCalls)
}

// TestGrokQuotaServiceQueryQuotaPaidBillingSkipsActiveProbe provides
// authoritative paid
// billing utilization. It checks billing source and percentage, absent active
// snapshot/model/local usage, and exactly two billing requests after shutdown.
func TestGrokQuotaServiceQueryQuotaPaidBillingSkipsActiveProbe(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(52)
	// A fresh model cache keeps background catalogue sync out of the billing
	// and active-probe request counts.
	provider.Extra = map[string]any{providercore.GrokObservedModelsExtraKey: map[string]any{
		"models": []string{"grok-4.5"}, "fetched_at": time.Now().UTC().Format(time.RFC3339),
	}}
	repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}}
	usagePercent := 25.0
	upstream := &grokHybridUpstream{weeklyUsagePercent: &usagePercent}
	usageRepo := &grokQuotaUsageLogRepo{stats: &providercore.WindowStats{Tokens: 1_000_000}}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil, usageRepo)

	result, err := svc.QueryQuota(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, "billing_probe", result.Source)
	require.NotNil(t, result.Billing)
	// Compare decoded utilization with a narrow absolute percentage tolerance.
	require.InDelta(t, usagePercent, *result.Billing.UsagePercent, 1e-9)
	require.Nil(t, result.Snapshot)
	require.Empty(t, result.Model)
	require.Nil(t, result.LocalUsage24h)
	require.NoError(t, svc.StopContext(context.Background()))

	requests, _ := upstream.snapshot()
	require.Len(t, requests, 2)
	for _, req := range requests {
		require.Equal(t, "/v1/billing", req.URL.Path)
	}
}

// TestGrokQuotaServiceQueryQuotaCustomPaidMonthlyLimitSkipsActiveProbe supplies a
// monthly limit and a fresh model cache without the ordinary weekly signal.
// Authoritative billing retains the limit, and the completed service makes
// exactly two billing requests without an active quota probe or model refresh.
func TestGrokQuotaServiceQueryQuotaCustomPaidMonthlyLimitSkipsActiveProbe(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(57)
	// A current model snapshot keeps auxiliary discovery out of the exact
	// billing request count, even when its background task runs before shutdown.
	provider.Extra = map[string]any{providercore.GrokObservedModelsExtraKey: map[string]any{
		"models": []string{"grok-4.5"}, "fetched_at": time.Now().UTC().Format(time.RFC3339),
	}}
	repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}}
	monthlyLimit := 25_000.0
	upstream := &grokHybridUpstream{monthlyLimitCents: &monthlyLimit}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	result, err := svc.QueryQuota(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, "billing_probe", result.Source)
	require.NotNil(t, result.Billing)
	// Compare the observed monthly limit with a narrow absolute cent tolerance.
	require.InDelta(t, monthlyLimit, *result.Billing.MonthlyLimitCents, 1e-9)
	require.Nil(t, result.Snapshot)
	require.NoError(t, svc.StopContext(context.Background()))

	requests, _ := upstream.snapshot()
	for _, req := range requests {
		require.Equal(t, "/v1/billing", req.URL.Path)
	}
	require.Len(t, requests, 2)
}

// TestGrokQuotaServiceProbeFlightsDeduplicateBillingAndSeparateActive holds a
// shared
// billing query while another caller starts. It checks two billing GETs for the
// shared weekly/monthly result and one independent active POST after joining
// both callers.
func TestGrokQuotaServiceProbeFlightsDeduplicateBillingAndSeparateActive(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(55)
	repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}}
	billingStarted := make(chan struct{})
	billingRelease := make(chan struct{})
	upstream := &grokHybridUpstream{billingStarted: billingStarted, billingRelease: billingRelease}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	// probeOutcome returns one joined caller result and error to the main
	// assertion flow.
	type probeOutcome struct {
		result *providercore.GrokQuotaProbeResult // returned billing probe observation
		err    error                              // returned billing probe error
	}
	billingOutcomes := make(chan probeOutcome, 2)
	// Hold the first billing caller at transport so a later caller can join its
	// flight while active quota work uses a different claim key.
	go func() {
		result, err := svc.ProbeBilling(context.Background(), provider.ID)
		billingOutcomes <- probeOutcome{result: result, err: err}
	}()
	<-billingStarted
	secondStarted := make(chan struct{})
	// Announce the second caller before it enters ProbeBilling. Its completion is
	// collected separately after the held transport is released, exposing
	// duplicate billing queries.
	go func() {
		close(secondStarted)
		result, err := svc.ProbeBilling(context.Background(), provider.ID)
		billingOutcomes <- probeOutcome{result: result, err: err}
	}()
	<-secondStarted
	// Give the announced caller a short scheduling interval to reach the shared
	// claim while the first billing request remains blocked; this is not a
	// join barrier.
	time.Sleep(25 * time.Millisecond)

	activeResult, err := svc.ProbeUsage(context.Background(), provider.ID)
	require.NoError(t, err)
	require.NotNil(t, activeResult.Snapshot)
	// Release the held billing exchange only after the independent active result
	// arrives, then join both billing callers before inspecting the transport
	// request count.
	close(billingRelease)
	for range 2 {
		outcome := <-billingOutcomes
		require.NoError(t, outcome.err)
		require.NotNil(t, outcome.result.Billing)
	}

	requests, _ := upstream.snapshot()
	billingCalls := 0
	activeCalls := 0
	for _, req := range requests {
		switch req.URL.Path {
		case "/v1/billing":
			billingCalls++
		case "/v1/responses":
			activeCalls++
		}
	}
	require.Equal(t, 2, billingCalls)
	require.Equal(t, 1, activeCalls)
}

// TestGrokQuotaServiceBilling429DoesNotPauseModelScheduling returns a rate-limited
// billing response with Retry-After. It checks billing query failure and no
// inference-provider rate-limit write from that billing-only observation.
func TestGrokQuotaServiceBilling429DoesNotPauseModelScheduling(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(56)
	repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}}
	upstream := &grokHybridUpstream{
		billingStatus:  http.StatusTooManyRequests,
		billingHeaders: http.Header{"Retry-After": []string{"45"}},
	}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	result, err := svc.ProbeBilling(context.Background(), provider.ID)

	require.Error(t, err)
	require.Nil(t, result)
	require.Zero(t, repo.rateLimitedCalls)
}

// TestGrokQuotaServiceBilling403PersistsMediaEligibilitySignal rejects both billing
// windows. It checks the failed query persists partial forbidden status and
// that the saved observation excludes new media generation.
func TestGrokQuotaServiceBilling403PersistsMediaEligibilitySignal(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(58)
	repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}}
	upstream := &grokHybridUpstream{billingStatus: http.StatusForbidden}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	result, err := svc.ProbeBilling(context.Background(), provider.ID)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, 1, repo.updateCalls)
	raw := repo.updates[provider.ID][providercore.GrokUsageBillingExtraKey]
	billing, ok := raw.(*xai.BillingSummary)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, billing.StatusCode)
	require.Equal(t, http.StatusForbidden, billing.WeeklyStatusCode)
	require.Equal(t, http.StatusForbidden, billing.MonthlyStatusCode)
	require.True(t, billing.Partial)

	provider.Extra = map[string]any{providercore.GrokUsageBillingExtraKey: billing}
	eligible, reason := providercore.GrokMediaGenerationEligibility(provider, GrokTierRules())
	require.False(t, eligible)
	require.Equal(t, "billing_forbidden", reason)
}

// TestGrokQuotaServicePartialBilling403PersistsMediaEligibilitySignal rejects
// weekly
// billing while monthly billing succeeds. It checks partial result and stored
// failed-window metadata still produce deterministic media ineligibility.
func TestGrokQuotaServicePartialBilling403PersistsMediaEligibilitySignal(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(59)
	repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}}
	upstream := &grokHybridUpstream{
		weeklyBillingStatus:  http.StatusForbidden,
		monthlyBillingStatus: http.StatusOK,
	}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	result, err := svc.ProbeBilling(context.Background(), provider.ID)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Billing)
	require.Equal(t, http.StatusOK, result.StatusCode)
	require.Equal(t, http.StatusForbidden, result.Billing.WeeklyStatusCode)
	require.Equal(t, http.StatusOK, result.Billing.MonthlyStatusCode)
	require.True(t, result.Billing.Partial)
	require.Contains(t, result.Billing.FailedWindows, "weekly")
	require.Equal(t, 1, repo.updateCalls)

	provider.Extra = map[string]any{providercore.GrokUsageBillingExtraKey: result.Billing}
	eligible, reason := providercore.GrokMediaGenerationEligibility(provider, GrokTierRules())
	require.False(t, eligible)
	require.Equal(t, "billing_forbidden", reason)
}

// TestGrokQuotaServiceProbeMediaEligibility compares positive paid evidence
// with empty
// and forbidden billing observations. It checks eligible, free-tier and forbidden
// reasons, with forbidden billing treated as a deterministic admission result.
func TestGrokQuotaServiceProbeMediaEligibility(t *testing.T) {
	t.Run("positive paid evidence enables media", func(t *testing.T) {
		usagePercent := 10.0
		monthlyLimit := 15_000.0
		provider := healthyGrokQuotaOAuthProvider(60)
		repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
			providersByID: map[int64]*providercore.Record{provider.ID: provider},
		}}
		upstream := &grokHybridUpstream{weeklyUsagePercent: &usagePercent, monthlyLimitCents: &monthlyLimit}
		svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

		eligible, reason, err := svc.ProbeMediaEligibility(context.Background(), provider.ID)

		require.NoError(t, err)
		require.True(t, eligible)
		require.Equal(t, "eligible", reason)
	})

	t.Run("successful empty billing identifies free provider", func(t *testing.T) {
		provider := healthyGrokQuotaOAuthProvider(61)
		repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
			providersByID: map[int64]*providercore.Record{provider.ID: provider},
		}}
		svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, &grokHybridUpstream{}, nil)

		eligible, reason, err := svc.ProbeMediaEligibility(context.Background(), provider.ID)

		require.NoError(t, err)
		require.False(t, eligible)
		require.Equal(t, "billing_free_tier", reason)
	})

	t.Run("forbidden billing is deterministic ineligibility", func(t *testing.T) {
		provider := healthyGrokQuotaOAuthProvider(62)
		repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
			providersByID: map[int64]*providercore.Record{provider.ID: provider},
		}}
		svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, &grokHybridUpstream{billingStatus: http.StatusForbidden}, nil)

		eligible, reason, err := svc.ProbeMediaEligibility(context.Background(), provider.ID)

		require.NoError(t, err)
		require.False(t, eligible)
		require.Equal(t, "billing_forbidden", reason)
	})
}

// TestGrokQuotaServiceQueryQuotaFree429PersistsLimitAndKeepsBilling combines
// planless
// billing with an active 429 response. It checks retained billing and snapshot
// timing, then one provider limit written to the observed reset deadline.
func TestGrokQuotaServiceQueryQuotaFree429PersistsLimitAndKeepsBilling(t *testing.T) {
	t.Parallel()

	provider := healthyGrokQuotaOAuthProvider(53)
	repo := &grokQuotaProviderRepo{grokQuotaReadStore: &grokQuotaReadStore{
		providersByID: map[int64]*providercore.Record{provider.ID: provider},
	}}
	upstream := &grokHybridUpstream{
		activeStatus:  http.StatusTooManyRequests,
		activeHeaders: http.Header{"Retry-After": []string{"45"}},
	}
	svc := newGrokQuotaFixture(repo, nil, &providercore.GrokTokenSource{Repository: repo, Policy: providercore.GrokProviderRefreshPolicy()}, upstream, nil)

	result, err := svc.QueryQuota(context.Background(), provider.ID)
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, result.StatusCode)
	require.NotNil(t, result.Billing)
	require.NotNil(t, result.Snapshot)
	require.Equal(t, 45, *result.Snapshot.RetryAfterSeconds)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Equal(t, provider.ID, repo.lastRateLimitedID)
	// Allow one second between header observation and this reset-time assertion.
	require.WithinDuration(t, time.Now().Add(45*time.Second), repo.lastRateLimitResetAt, time.Second)
}

// TestGrokQuotaServiceResetQuotaUnsupported constructs the quota service with
// a stored
// Grok OAuth record. It checks reset remains unsupported with HTTP 501 and its
// stable reason even when probe transport is absent.
func TestGrokQuotaServiceResetQuotaUnsupported(t *testing.T) {
	t.Parallel()

	provider := &providercore.Record{
		ID:       44,
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
	}
	repo := &grokQuotaProviderRepo{
		grokQuotaReadStore: &grokQuotaReadStore{
			providersByID: map[int64]*providercore.Record{44: provider},
		},
	}
	svc := newGrokQuotaFixture(repo, nil, nil, nil, nil)

	_, err := svc.ResetQuota(context.Background(), 44)
	require.Error(t, err)
	require.Equal(t, http.StatusNotImplemented, httpx.ErrorCode(err))
	require.Equal(t, "GROK_QUOTA_RESET_UNSUPPORTED", apperror.Reason(err))
}
