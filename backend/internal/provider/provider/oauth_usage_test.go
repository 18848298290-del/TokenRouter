package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
)

// providerUsageHTTPUpstreamStub captures transport inputs and supplies quota
// headers on a 429 response.
type providerUsageHTTPUpstreamStub struct {
	// tlsProfile and req retain the supplied pointers after synchronous capture.
	// Tests inspect those same objects after the probe returns and keep their
	// contents stable while transport executes.
	tlsProfile *tlsfingerprint.Profile // captured selected TLS fingerprint
	req        *http.Request           // captured prepared usage request
	proxyURL   string                  // captured request proxy URL
	providerID int64                   // captured provider record identifier
}

// openAIObservationIdentityRepo detects snapshot writes after a usage probe's
// credential identity changes.
type openAIObservationIdentityRepo struct {
	qoderObservationIdentityRepo               // embedded conditional observation repository
	written                      chan struct{} // stale write attempt signal
}

// openAIObservationIdentityUpstream changes repository state before returning a
// captured quota response.
type openAIObservationIdentityUpstream struct {
	providerUsageHTTPUpstreamStub // captured quota response transport
	// beforeReturn is required by this stub. It changes test state after transport
	// capture and before the usage service receives the response.
	beforeReturn func()
}

// TestProviderUsageService_ShouldProbeOpenAICodexSnapshot_ForceBypassesCache
// records a first probe and repeats inside its cache lifetime. Ordinary
// admission must skip the repeat while a forced probe bypasses that gate.
func TestProviderUsageService_ShouldProbeOpenAICodexSnapshot_ForceBypassesCache(t *testing.T) {
	t.Parallel()

	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{cache: providercore.NewOAuthUsageCache()})
	now := time.Now()
	providerID := int64(123)

	if !svc.ShouldProbeOpenAICodexSnapshot(providerID, now) {
		t.Fatal("首次探测应该写入缓存并允许执行")
	}
	if svc.ShouldProbeOpenAICodexSnapshot(providerID, now.Add(time.Minute)) {
		t.Fatal("缓存有效期内的普通探测应该被跳过")
	}
	if !svc.ShouldProbeOpenAICodexSnapshot(providerID, now.Add(2*time.Minute), true) {
		t.Fatal("强制刷新应该绕过探测缓存")
	}
}

// TestProviderUsageService_ProbeOpenAICodexSnapshotUsesHTTPUpstreamTLSProfile
// enables provider TLS fingerprinting on an OAuth probe. The transport must
// receive a profile, the provider ID, and the OpenAI request context tag while
// returning quota updates.
func TestProviderUsageService_ProbeOpenAICodexSnapshotUsesHTTPUpstreamTLSProfile(t *testing.T) {
	t.Parallel()

	upstream := &providerUsageHTTPUpstreamStub{}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{
		httpUpstream:        upstream,
		tlsFPProfileService: &egressadapter.TLSProfiles{},
	})
	provider := &providercore.Record{
		ID:          456,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Concurrency: 9,
		Credentials: map[string]any{"access_token": "token"},
		Extra:       map[string]any{"enable_tls_fingerprint": true},
	}

	updates, err := svc.ProbeOpenAICodexSnapshot(context.Background(), provider)
	if err != nil {
		t.Fatalf("probeOpenAICodexSnapshot() error = %v", err)
	}
	if len(updates) == 0 {
		t.Fatal("expected codex usage updates")
	}
	if upstream.tlsProfile == nil {
		t.Fatal("expected non-nil TLS profile")
	}
	if upstream.req == nil || upstreamcore.HTTPUpstreamProfileFromContext(upstream.req.Context()) != upstreamcore.HTTPUpstreamProfileOpenAI {
		t.Fatal("expected OpenAI upstream profile on probe request")
	}
	if upstream.providerID != provider.ID {
		t.Fatalf("providerID = %d, want %d", upstream.providerID, provider.ID)
	}
}

// TestProviderUsageService_ProbeOpenAICodexSnapshotSkipsTLSProfileWhenDisabled
// disables provider fingerprint selection before an OAuth probe. The probe must
// still produce quota updates while passing nil as its TLS profile.
func TestProviderUsageService_ProbeOpenAICodexSnapshotSkipsTLSProfileWhenDisabled(t *testing.T) {
	t.Parallel()

	upstream := &providerUsageHTTPUpstreamStub{}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{
		httpUpstream:        upstream,
		tlsFPProfileService: &egressadapter.TLSProfiles{},
	})
	provider := &providercore.Record{
		ID:          789,
		Platform:    capability.PlatformOpenAI,
		Type:        capability.ProviderTypeOAuth,
		Credentials: map[string]any{"access_token": "token"},
		Extra:       map[string]any{"enable_tls_fingerprint": false},
	}

	updates, err := svc.ProbeOpenAICodexSnapshot(context.Background(), provider)
	if err != nil {
		t.Fatalf("probeOpenAICodexSnapshot() error = %v", err)
	}
	if len(updates) == 0 {
		t.Fatal("expected codex usage updates")
	}
	if upstream.tlsProfile != nil {
		t.Fatal("关闭 TLS 指纹时不应传入 profile")
	}
}

// TestExtractOpenAICodexProbeUpdatesAccepts429WithCodexHeaders supplies
// exhausted short and weekly quota headers on HTTP 429. Extraction must succeed
// and retain both canonical utilization values despite the error status.
func TestExtractOpenAICodexProbeUpdatesAccepts429WithCodexHeaders(t *testing.T) {
	t.Parallel()

	headers := make(http.Header)
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "604800")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "100")
	headers.Set("x-codex-secondary-reset-after-seconds", "18000")
	headers.Set("x-codex-secondary-window-minutes", "300")

	updates, err := ExtractOpenAIUsageUpdates(&http.Response{StatusCode: http.StatusTooManyRequests, Header: headers}, time.Now())
	if err != nil {
		t.Fatalf("extractOpenAICodexProbeUpdates() error = %v", err)
	}
	if len(updates) == 0 {
		t.Fatal("expected codex probe updates from 429 headers")
	}
	if got := updates["codex_5h_used_percent"]; got != 100.0 {
		t.Fatalf("codex_5h_used_percent = %v, want 100", got)
	}
	if got := updates["codex_7d_used_percent"]; got != 100.0 {
		t.Fatalf("codex_7d_used_percent = %v, want 100", got)
	}
}

// TestProviderUsageService_PersistOpenAICodexProbeSnapshotOnlyUpdatesExtra
// persists an exhausted weekly observation asynchronously. Extra must receive
// the supplied utilization while the provider-rate-limit capture channel
// remains empty.
func TestProviderUsageService_PersistOpenAICodexProbeSnapshotOnlyUpdatesExtra(t *testing.T) {
	t.Parallel()

	repo := &providerUsageCodexProbeRepo{
		updateExtraCh: make(chan map[string]any, 1),
		rateLimitCh:   make(chan time.Time, 1),
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo})
	svc.PersistOpenAICodexProbeSnapshot(&providercore.Record{ID: 321}, map[string]any{
		"codex_7d_used_percent": 100.0,
		"codex_7d_reset_at":     time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339),
	})

	select {
	case updates := <-repo.updateExtraCh:
		if got := updates["codex_7d_used_percent"]; got != 100.0 {
			t.Fatalf("codex_7d_used_percent = %v, want 100", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待 codex 探测快照写入 extra 超时")
	}

	select {
	case got := <-repo.rateLimitCh:
		t.Fatalf("不应将探测快照写入运行时限流状态: %v", got)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestProviderUsageService_GetOpenAIUsage_DoesNotPromoteCodexExtraToRateLimit
// displays exhausted weekly data already stored in Extra. The usage window must
// remain visible while in-memory and stored provider rate limits stay
// unchanged.
func TestProviderUsageService_GetOpenAIUsage_DoesNotPromoteCodexExtraToRateLimit(t *testing.T) {
	t.Parallel()

	resetAt := time.Now().Add(6 * 24 * time.Hour).UTC().Truncate(time.Second)
	repo := &providerUsageCodexProbeRepo{
		rateLimitCh: make(chan time.Time, 1),
	}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo})
	provider := &providercore.Record{
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Extra: map[string]any{
			"codex_5h_used_percent": 1.0,
			"codex_5h_reset_at":     time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339),
			"codex_7d_used_percent": 100.0,
			"codex_7d_reset_at":     resetAt.Format(time.RFC3339),
		},
	}

	usage, err := svc.GetOpenAIUsage(context.Background(), provider, false)
	if err != nil {
		t.Fatalf("getOpenAIUsage() error = %v", err)
	}
	if usage.SevenDay == nil || usage.SevenDay.Utilization != 100.0 {
		t.Fatalf("预期 7 天用量仍然可见，实际为 %#v", usage.SevenDay)
	}
	if provider.RateLimitResetAt != nil {
		t.Fatalf("不应让已耗尽的 codex extra 改写运行时限流状态: %v", provider.RateLimitResetAt)
	}
	select {
	case got := <-repo.rateLimitCh:
		t.Fatalf("不应将已耗尽的 codex extra 持久化为运行时限流状态: %v", got)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestOpenAIUsageProbeCannotWriteNewAdministratorIdentity replaces credentials
// just before the probe returns. Quota updates may be returned, but their
// asynchronous persistence must not write the administrator's newer identity.
func TestOpenAIUsageProbeCannotWriteNewAdministratorIdentity(t *testing.T) {
	observed := &providercore.Record{ID: 923, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: providercore.StatusActive, Credentials: map[string]any{"access_token": "observed"}}
	current := *observed
	repo := &openAIObservationIdentityRepo{qoderObservationIdentityRepo: qoderObservationIdentityRepo{current: &current}, written: make(chan struct{}, 1)}
	upstream := &openAIObservationIdentityUpstream{beforeReturn: func() { current.Credentials = map[string]any{"access_token": "administrator"} }}
	svc := newOAuthUsageFixture(oauthUsageFixtureOptions{providerRepo: repo, httpUpstream: upstream})
	updates, err := svc.ProbeOpenAICodexSnapshot(context.Background(), observed)
	require.NoError(t, err)
	require.NotEmpty(t, updates)
	select {
	case <-repo.written:
		t.Fatal("迟到的 OpenAI 快照写入了新身份")
	case <-time.After(100 * time.Millisecond):
	}
}

// Do delegates the captured usage request to DoWithTLS with a nil profile.
func (s *providerUsageHTTPUpstreamStub) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, providerID, providerConcurrency, nil)
}

// DoWithTLS captures request identity and transport options, then returns fixed
// quota headers on a 429 response. The caller owns the empty response body and
// closes it after extraction.
func (s *providerUsageHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, providerID int64, _ int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	s.req = req
	s.proxyURL = proxyURL
	s.providerID = providerID
	s.tlsProfile = profile
	headers := make(http.Header)
	headers.Set("x-codex-primary-used-percent", "7")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "3")
	headers.Set("x-codex-secondary-window-minutes", "300")
	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader("")),
	}, nil
}

// UpdateExtra signals an attempted snapshot write without blocking when the
// capture channel is full. Tests treat that signal as evidence that a stale
// identity escaped the conditional writer.
func (r *openAIObservationIdentityRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	// Signal at most one pending write attempt without waiting for the assertion.
	// A full channel already retains the evidence needed to fail the
	// stale-identity test.
	select {
	case r.written <- struct{}{}:
	default:
	}
	return nil
}

// DoWithTLS obtains the quota fixture and invokes beforeReturn before exposing
// it to the probe. Configure that callback to mutate identity at the intended
// response boundary.
func (u *openAIObservationIdentityUpstream) DoWithTLS(req *http.Request, proxy string, id int64, n int, p *tlsfingerprint.Profile) (*http.Response, error) {
	resp, err := u.providerUsageHTTPUpstreamStub.DoWithTLS(req, proxy, id, n, p)
	// Replace identity after the request used its original credentials but before
	// probe persistence runs, reproducing a delayed observation racing with an
	// administrator update.
	u.beforeReturn()
	return resp, err
}
