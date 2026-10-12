package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

var (
	errCooldownSettingMissing                                 = errors.New("setting missing")
	_                         providercore.SessionWindowStore = (*sessionWindowMockRepo)(nil)
)

type codexInviteResetAdminServiceStub struct {
	provider *providercore.Record
	proxy    *egress.Proxy
}

type codexInviteResetHTTPUpstreamStub struct {
	responses []*http.Response
	requests  []*http.Request
	bodies    []string
	profiles  []*tlsfingerprint.Profile
}

// tlsProfileTestStore supplies fixed profiles through the production repository interface.
type tlsProfileTestStore struct {
	egress.TLSFingerprintProfileRepository
	profiles []*egress.TLSFingerprintProfile
}

// cooldownSettingsStore supplies settings values to the production runtime cache.
type cooldownSettingsStore struct{ data map[string]string }

type grokQuotaProviderRepo struct {
	*grokQuotaReadStore
	updates               map[int64]map[string]any
	updateCalls           int
	rateLimitedCalls      int
	lastRateLimitedID     int64
	lastRateLimitResetAt  time.Time
	tempUnschedCalls      int
	lastTempUnschedID     int64
	lastTempUnschedUntil  time.Time
	lastTempUnschedReason string
	recoveryClearCalls    int
	recoveryObservedAt    time.Time
	recoveryObservedReset time.Time
	recoveryClearResult   bool
}

type grokQuotaProxyRepo struct {
	proxies map[int64]*egress.Proxy
	calls   int
}

type grokQuotaUsageLogRepo struct {
	stats      *providercore.WindowStats
	err        error
	calls      int
	startTimes []time.Time
}

type grokHybridUpstream struct {
	mu                   sync.Mutex
	requests             []*http.Request
	bodies               [][]byte
	weeklyUsagePercent   *float64
	monthlyLimitCents    *float64
	activeStatus         int
	activeHeaders        http.Header
	billingStarted       chan struct{}
	billingRelease       <-chan struct{}
	billingStartOnce     sync.Once
	billingStatus        int
	weeklyBillingStatus  int
	monthlyBillingStatus int
	billingHeaders       http.Header
}

// grokQuotaReadStore returns independent record copies and counts lookups.
type grokQuotaReadStore struct {
	providersByID map[int64]*providercore.Record
	getByIDCalls  int
}

// grokQuotaHTTPRecorder captures requests and bodies for assertions.
type grokQuotaHTTPRecorder struct {
	lastReq      *http.Request
	lastBody     []byte
	lastProxyURL string
	requests     []*http.Request
	bodies       [][]byte
	resp         *http.Response
	responses    []*http.Response
	err          error
}

type grokQuotaUpstreamStep struct {
	status int
	body   string
	err    error
}

type grokQuotaSequenceUpstream struct {
	mu       sync.Mutex
	steps    []grokQuotaUpstreamStep
	requests []*http.Request
}

type providerUsageCodexProbeRepo struct {
	usageRecordFixture
	updateExtraCh chan map[string]any
	rateLimitCh   chan time.Time
	clearLimitCh  chan int64
	clearErrorCh  chan int64
}

// oauthUsageFixtureOptions injects dependencies into production usage querying and state management.
type oauthUsageFixtureOptions struct {
	providerRepo         providercore.OAuthUsageReader
	cache                *providercore.OAuthUsageCache
	httpUpstream         QoderTransport
	tlsFPProfileService  *egressadapter.TLSProfiles
	qoderSessionProvider *QoderTokenProvider
}

// usageRecordFixture returns independent records by ID and errors for missing records.
type usageRecordFixture struct{ providers []providercore.Record }

type openAIOAuthTokenRouterReaderStub struct {
	routers map[int64]*egress.TLSFingerprintRouter
}

// qoderObservationIdentityRepo rejects results from credentials replaced while the request was in flight.
type qoderObservationIdentityRepo struct {
	providercore.OAuthUsageReader
	current *providercore.Record
	writes  int
}

type tokenRefreshProviderRepo struct {
	refreshRecordFixture
	updateCalls                  int
	fullUpdateCalls              int
	updateCredentialsCalls       int
	setErrorCalls                int
	clearTempCalls               int
	setTempUnschedCalls          int
	updateExtraCalls             int
	lastErrorMessage             string
	lastTempUnschedReason        string
	lastExtraUpdates             map[string]any
	lastProvider                 *providercore.Record
	updateErr                    error
	cancelOnUpdate               context.CancelFunc
	conditionalErrorCalls        int
	conditionalTempCalls         int
	conditionalSuccessCalls      int
	conditionalErrorErr          error
	conditionalTempErr           error
	conditionalSuccessErr        error
	snapshotReads                bool
	respectReadContext           bool
	getByIDCalls                 int
	durableReadDelay             time.Duration
	mutateSchedulingOnSuccessCAS bool
	reauthorizeOnErrorCAS        bool
	reauthorizeOnTempCAS         bool
	repairProxyOnErrorCAS        bool
	repairProxyOnTempCAS         bool
	setErrorErr                  error
	setTempUnschedErr            error
	beforeConditionalState       func()
}

type tokenRefresherStub struct {
	credentials map[string]any
	err         error
	calls       int
}

// refreshRecordFixture supplies provider records for refresh tests.
type refreshRecordFixture struct {
	providersByID map[int64]*providercore.Record
}

// sessionWindowMockRepo records window and recovery writes and panics on unexpected error-setting
// operations.
type sessionWindowMockRepo struct {
	sessionWindowCalls []swCall
	updateExtraCalls   []ueCall
	clearRateLimitIDs  []int64
}

type swCall struct {
	ID     int64
	Start  *time.Time
	End    *time.Time
	Status string
}

type ueCall struct {
	ID      int64
	Updates map[string]any
}

// refreshFailureMatchesFixture compares the current row with the observed failure version, including
// nil credentials.
func refreshFailureMatchesFixture(value *providercore.Record, version providercore.RefreshFailureVersion) bool {
	return value != nil && reflect.DeepEqual(providercore.FailureVersion(value), version)
}

func (s codexInviteResetAdminServiceStub) GetProvider(ctx context.Context, id int64) (*providercore.Record, error) {
	return s.provider, nil
}

func (s codexInviteResetAdminServiceStub) GetProxy(ctx context.Context, id int64) (*egress.Proxy, error) {
	return s.proxy, nil
}

func (s *codexInviteResetHTTPUpstreamStub) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, providerID, providerConcurrency, nil)
}

func (s *codexInviteResetHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		payload, _ := io.ReadAll(req.Body)
		body = string(payload)
		req.Body = io.NopCloser(strings.NewReader(body))
	}
	s.requests = append(s.requests, req)
	s.bodies = append(s.bodies, body)
	s.profiles = append(s.profiles, profile)
	if len(s.responses) == 0 {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp, nil
}

func codexInviteResetJSONResponse(body string) *http.Response {
	return codexInviteResetJSONStatusResponse(http.StatusOK, body)
}

func codexInviteResetJSONStatusResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func (s *tlsProfileTestStore) List(context.Context) ([]*egress.TLSFingerprintProfile, error) {
	return s.profiles, nil
}

// newTLSProfileServiceWithCacheForTest starts the production service to populate the TLS profile cache.
func newTLSProfileServiceWithCacheForTest(profiles map[int64]*egress.TLSFingerprintProfile) *egressadapter.TLSProfiles {
	values := make([]*egress.TLSFingerprintProfile, 0, len(profiles))
	for _, profile := range profiles {
		values = append(values, profile)
	}
	service := egressadapter.NewTLSProfiles(egress.NewTLSFingerprintProfileService(&tlsProfileTestStore{profiles: values}, nil))
	service.Start()
	return service
}

func newCooldownSettingsStore() *cooldownSettingsStore {
	return &cooldownSettingsStore{data: map[string]string{}}
}

func (s *cooldownSettingsStore) GetValue(_ context.Context, key string) (string, error) {
	value, ok := s.data[key]
	if !ok {
		return "", errCooldownSettingMissing
	}
	return value, nil
}

func (s *cooldownSettingsStore) Set(_ context.Context, key, value string) error {
	s.data[key] = value
	return nil
}

func makeGrokOAuthJWT(claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func (r *grokQuotaProviderRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.updateCalls++
	if r.updates == nil {
		r.updates = make(map[int64]map[string]any)
	}
	r.updates[id] = updates
	if r.grokQuotaReadStore != nil {
		provider := r.providersByID[id]
		if provider == nil {
			return nil
		}
		if provider.Extra == nil {
			provider.Extra = make(map[string]any)
		}
		maps.Copy(provider.Extra, updates)
	}
	return nil
}

func (r *grokQuotaProviderRepo) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitedCalls++
	r.lastRateLimitedID = id
	r.lastRateLimitResetAt = resetAt
	return nil
}

func (r *grokQuotaProviderRepo) SetRateLimitedIfLater(ctx context.Context, id int64, resetAt time.Time) error {
	return r.SetRateLimited(ctx, id, resetAt)
}

func (r *grokQuotaProviderRepo) ClearRateLimitIfObserved(_ context.Context, _ int64, observedLimitedAt, observedResetAt time.Time) (bool, error) {
	r.recoveryClearCalls++
	r.recoveryObservedAt = observedLimitedAt
	r.recoveryObservedReset = observedResetAt
	return r.recoveryClearResult, nil
}

func (r *grokQuotaProviderRepo) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.tempUnschedCalls++
	r.lastTempUnschedID = id
	r.lastTempUnschedUntil = until
	r.lastTempUnschedReason = reason
	return nil
}

func (r *grokQuotaUsageLogRepo) GetProviderWindowStats(_ context.Context, _ int64, start time.Time) (*providercore.WindowStats, error) {
	r.calls++
	r.startTimes = append(r.startTimes, start)
	return r.stats, r.err
}

func (r *grokQuotaUsageLogRepo) GetProviderTodayStats(context.Context, int64) (*providercore.WindowStats, error) {
	return nil, nil
}

func (u *grokHybridUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	var body []byte
	if req != nil && req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	u.mu.Lock()
	u.requests = append(u.requests, req)
	u.bodies = append(u.bodies, body)
	u.mu.Unlock()

	if req.URL.Path == "/v1/responses" {
		status := u.activeStatus
		if status == 0 {
			status = http.StatusOK
		}
		headers := u.activeHeaders
		if headers == nil {
			headers = http.Header{
				"X-Ratelimit-Limit-Tokens":     []string{"2000000"},
				"X-Ratelimit-Remaining-Tokens": []string{"1500000"},
			}
		}
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(`{"id":"resp_probe"}`))}, nil
	}
	if u.billingStarted != nil {
		u.billingStartOnce.Do(func() { close(u.billingStarted) })
	}
	if u.billingRelease != nil {
		select {
		case <-u.billingRelease:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	billingStatus := u.billingStatus
	if req.URL.RawQuery == "format=credits" && u.weeklyBillingStatus != 0 {
		billingStatus = u.weeklyBillingStatus
	}
	if req.URL.RawQuery != "format=credits" && u.monthlyBillingStatus != 0 {
		billingStatus = u.monthlyBillingStatus
	}
	if billingStatus != 0 && billingStatus != http.StatusOK {
		return &http.Response{
			StatusCode: billingStatus,
			Header:     u.billingHeaders,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"billing limited"}}`)),
		}, nil
	}

	if req.URL.RawQuery == "format=credits" {
		usage := ""
		if u.weeklyUsagePercent != nil {
			usage = `,"creditUsagePercent":` + strconv.FormatFloat(*u.weeklyUsagePercent, 'f', -1, 64)
		}
		payload := `{"config":{"currentPeriod":{"type":"WEEKLY","start":"2026-07-09T03:25:00Z","end":"2026-07-16T03:25:00Z"}` + usage + `}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
	}
	monthlyLimit := ""
	if u.monthlyLimitCents != nil {
		monthlyLimit = `,"monthlyLimit":{"val":` + strconv.FormatFloat(*u.monthlyLimitCents, 'f', -1, 64) + `}`
	}
	monthlyPayload := `{"config":{"billingPeriodStart":"2026-07-01T00:00:00Z","billingPeriodEnd":"2026-08-01T00:00:00Z"` + monthlyLimit + `}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(monthlyPayload)),
	}, nil
}

func (u *grokHybridUpstream) snapshot() ([]*http.Request, [][]byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	requests := append([]*http.Request(nil), u.requests...)
	bodies := make([][]byte, len(u.bodies))
	for i := range u.bodies {
		bodies[i] = append([]byte(nil), u.bodies[i]...)
	}
	return requests, bodies
}

func (r *grokQuotaProxyRepo) GetByID(_ context.Context, id int64) (*egress.Proxy, error) {
	r.calls++
	return r.proxies[id], nil
}

// newGrokQuotaFixture uses the production constructor with stub storage and transport.
func newGrokQuotaFixture(store GrokQuotaStore, proxy *grokQuotaProxyRepo, token *providercore.GrokTokenSource, transport interface {
	Do(*http.Request, string, int64, int) (*http.Response, error)
}, validator xai.BaseURLValidator, readers ...providercore.LocalUsageStats,
) *providercore.GrokQuotaService {
	requests := &GrokQuotaTransport{OperatorValidator: validator, MapStatus: forward.MapStatus}
	if proxy != nil {
		requests.Proxy = proxy.GetByID
	}
	if transport != nil {
		requests.Do = transport.Do
	}
	var stats providercore.LocalUsageStats
	if len(readers) > 0 {
		stats = readers[0]
	}
	return NewGrokQuota(store, token, requests, stats)
}

func (r *grokQuotaReadStore) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	r.getByIDCalls++
	if value, ok := r.providersByID[id]; ok {
		return providercore.CloneRecord(value), nil
	}
	return nil, errors.New("provider not found")
}

func (u *grokQuotaHTTPRecorder) Do(req *http.Request, proxyURL string, _ int64, _ int) (*http.Response, error) {
	u.lastReq = req
	u.lastProxyURL = proxyURL
	if req != nil && req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		u.lastBody = body
		u.bodies = append(u.bodies, append([]byte(nil), body...))
		if err := req.Body.Close(); err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	u.requests = append(u.requests, req)
	if u.err != nil {
		return nil, u.err
	}
	if len(u.responses) > 0 {
		response := u.responses[0]
		u.responses = u.responses[1:]
		return response, nil
	}
	return u.resp, nil
}

func (u *grokQuotaSequenceUpstream) snapshotRequests() []*http.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]*http.Request(nil), u.requests...)
}

func healthyGrokQuotaOAuthProvider(id int64) *providercore.Record {
	return &providercore.Record{
		ID:          id,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      providercore.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(2 * providercore.GrokTokenRefreshSkew).UTC().Format(time.RFC3339),
		},
	}
}

// Do returns the next scripted response; the platform client owns retries.
func (u *grokQuotaSequenceUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.requests = append(u.requests, req)
	index := len(u.requests) - 1
	if index >= len(u.steps) {
		return nil, errors.New("unexpected upstream request")
	}
	step := u.steps[index]
	if step.err != nil {
		return nil, step.err
	}
	return &http.Response{StatusCode: step.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(step.body))}, nil
}

func (r *providerUsageCodexProbeRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	if r.updateExtraCh != nil {
		copied := make(map[string]any, len(updates))
		maps.Copy(copied, updates)
		r.updateExtraCh <- copied
	}
	return nil
}

func (r *providerUsageCodexProbeRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	if r.rateLimitCh != nil {
		r.rateLimitCh <- resetAt
	}
	return nil
}

func (r *providerUsageCodexProbeRepo) ClearRateLimit(_ context.Context, id int64) error {
	if r.clearLimitCh != nil {
		r.clearLimitCh <- id
	}
	return nil
}

func (r *providerUsageCodexProbeRepo) ClearError(_ context.Context, id int64) error {
	if r.clearErrorCh != nil {
		r.clearErrorCh <- id
	}
	return nil
}

func newOAuthUsageFixture(options oauthUsageFixtureOptions) *providercore.OAuthUsageService {
	sessions := options.qoderSessionProvider
	if sessions == nil {
		sessions = NewQoderTokenProvider(qoder.SessionBuilder{})
		sessions.SetHTTPUpstream(options.httpUpstream, options.tlsFPProfileService)
	}
	qoderQuery := &QoderUsage{Sessions: sessions, Transport: options.httpUpstream, Profiles: options.tlsFPProfileService}
	qoderOptions := qoderQuery.Options()
	qoderOptions.Enrich = EnrichUsageWithProviderError
	requests := &OAuthUsageTransport{Transport: options.httpUpstream, Profiles: options.tlsFPProfileService}
	return providercore.NewOAuthUsageService(options.providerRepo, options.cache, nil, providercore.OAuthUsageOptions{
		Now: time.Now, Log: log.Printf, Warn: slog.Warn,
		Qoder:  qoderOptions,
		OpenAI: providercore.OpenAIUsageOptions{Probe: requests.ProbeOpenAI},
		OpenAIQuotaPause: func(_ context.Context, value *providercore.Record, info *providercore.UsageInfo) {
			if value != nil && info != nil {
				info.QuotaAutoPaused, _ = providercore.EvaluateQuotaAutoPause(value.Platform, value.Extra, providercore.QuotaAutoPauseSettings{}, time.Now())
			}
		},
	})
}

func (r usageRecordFixture) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	for index := range r.providers {
		if r.providers[index].ID == id {
			return providercore.CloneRecord(&r.providers[index]), nil
		}
	}
	return nil, errors.New("provider not found")
}

func (r usageRecordFixture) GetByIDs(_ context.Context, ids []int64) ([]*providercore.Record, error) {
	result := make([]*providercore.Record, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		for index := range r.providers {
			if r.providers[index].ID == id {
				result = append(result, providercore.CloneRecord(&r.providers[index]))
				break
			}
		}
	}
	return result, nil
}

// UpdateUsageExtraIfUnchanged records Extra writes; PostgreSQL integration tests cover comparisons and
// transactions.
func (r *providerUsageCodexProbeRepo) UpdateUsageExtraIfUnchanged(ctx context.Context, version providercore.UsageObservationVersion, updates map[string]any) (bool, error) {
	return true, r.UpdateExtra(ctx, version.ID, updates)
}

func (r *providerUsageCodexProbeRepo) SetUsageRateLimitIfUnchanged(ctx context.Context, version providercore.UsageObservationVersion, reset time.Time) (bool, error) {
	return true, r.SetRateLimited(ctx, version.ID, reset)
}

func (r *providerUsageCodexProbeRepo) ClearUsageRateLimitIfUnchanged(ctx context.Context, version providercore.UsageObservationVersion) (bool, error) {
	return true, r.ClearRateLimit(ctx, version.ID)
}

// newOpenAIAuthorizationForTest configures authorization and registers session cleanup.
func newOpenAIAuthorizationForTest(t *testing.T, proxies egress.ProxyRepository, client OpenAIOAuthClient, dependencies ...*OpenAIAuthorizationDependencies) *providercore.OpenAIAuthorization {
	t.Helper()
	deps := &OpenAIAuthorizationDependencies{}
	if len(dependencies) > 0 {
		deps = dependencies[0]
	}
	deps.Proxies = proxies
	deps.Client = client
	authorization := providercore.NewOpenAIAuthorization(providercore.NewOpenAISessionStore(), OpenAIAuthorizationOptions(deps))
	t.Cleanup(func() { stopOpenAIAuthorizationForTest(t, authorization) })
	return authorization
}

func stopOpenAIAuthorizationForTest(t *testing.T, authorization *providercore.OpenAIAuthorization) {
	t.Helper()
	require.NoError(t, authorization.StopContext(context.Background()))
}

func (s *openAIOAuthTokenRouterReaderStub) GetRuntimeRouter(routerID int64) *egress.TLSFingerprintRouter {
	if s == nil {
		return nil
	}
	return s.routers[routerID]
}

func (r *qoderObservationIdentityRepo) GetByID(context.Context, int64) (*providercore.Record, error) {
	v := *r.current
	return &v, nil
}

func (r *qoderObservationIdentityRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	r.writes++
	return nil
}

func (r *qoderObservationIdentityRepo) SetRateLimited(context.Context, int64, time.Time) error {
	r.writes++
	return nil
}

func (r *qoderObservationIdentityRepo) ClearRateLimit(context.Context, int64) error {
	r.writes++
	return nil
}

func (r *tokenRefreshProviderRepo) Update(ctx context.Context, provider *providercore.Record) error {
	r.updateCalls++
	r.fullUpdateCalls++
	r.lastProvider = provider
	return r.updateErr
}

func (r *tokenRefreshProviderRepo) UpdateCredentials(ctx context.Context, id int64, credentials map[string]any) error {
	r.updateCalls++
	r.updateCredentialsCalls++
	if r.updateErr != nil {
		return r.updateErr
	}
	cloned := maps.Clone(credentials)
	if r.providersByID != nil {
		if acc, ok := r.providersByID[id]; ok && acc != nil {
			acc.Credentials = cloned
			r.lastProvider = acc
			if r.cancelOnUpdate != nil {
				r.cancelOnUpdate()
			}
			return nil
		}
	}
	r.lastProvider = &providercore.Record{ID: id, Credentials: cloned}
	if r.cancelOnUpdate != nil {
		r.cancelOnUpdate()
	}
	return nil
}

func (r *tokenRefreshProviderRepo) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	if r.respectReadContext && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	r.getByIDCalls++
	if r.getByIDCalls > 1 && r.durableReadDelay > 0 {
		timer := time.NewTimer(r.durableReadDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	provider, err := r.refreshRecordFixture.GetByID(ctx, id)
	if err != nil || !r.snapshotReads {
		return provider, err
	}
	return providercore.CloneRecord(provider), nil
}

func (r *tokenRefreshProviderRepo) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.setErrorCalls++
	r.lastErrorMessage = errorMsg
	return r.setErrorErr
}

func (r *tokenRefreshProviderRepo) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.clearTempCalls++
	return nil
}

func (r *tokenRefreshProviderRepo) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.setTempUnschedCalls++
	r.lastTempUnschedReason = reason
	return r.setTempUnschedErr
}

func (r *tokenRefreshProviderRepo) SetGrokCredentialErrorIfMatch(
	_ context.Context,
	id int64,
	snapshot providercore.CredentialMutationSnapshot,
	errorMsg string,
) (bool, error) {
	if r.beforeConditionalState != nil {
		hook := r.beforeConditionalState
		r.beforeConditionalState = nil
		hook()
	}
	provider := r.providersByID[id]
	if !grokCredentialSnapshotMatchesProvider(provider, snapshot) ||
		(errorMsg == "grok_oauth_proxy_invalid" && provider.Proxy != nil) {
		return false, nil
	}
	r.setErrorCalls++
	r.lastErrorMessage = errorMsg
	if r.setErrorErr != nil {
		return false, r.setErrorErr
	}
	provider.Status = providercore.StatusError
	provider.Schedulable = false
	provider.ErrorMessage = errorMsg
	return true, nil
}

func (r *tokenRefreshProviderRepo) SetGrokCredentialTempUnschedulableIfMatch(
	_ context.Context,
	id int64,
	snapshot providercore.CredentialMutationSnapshot,
	until time.Time,
	reason string,
) (bool, error) {
	if r.beforeConditionalState != nil {
		hook := r.beforeConditionalState
		r.beforeConditionalState = nil
		hook()
	}
	provider := r.providersByID[id]
	if !grokCredentialSnapshotMatchesProvider(provider, snapshot) {
		return false, nil
	}
	r.setTempUnschedCalls++
	r.lastTempUnschedReason = reason
	if r.setTempUnschedErr != nil {
		return false, r.setTempUnschedErr
	}
	value := until
	provider.TempUnschedulableUntil = &value
	return true, nil
}

func grokCredentialSnapshotMatchesProvider(provider *providercore.Record, snapshot providercore.CredentialMutationSnapshot) bool {
	return provider != nil && provider.IsGrokOAuth() && provider.IsSchedulable() &&
		providercore.GrokCredentialMutationSnapshot(provider).CredentialsJSON == snapshot.CredentialsJSON &&
		providercore.GrokCredentialProxyIDsEqual(provider.ProxyID, snapshot.ProxyID)
}

func (r *tokenRefreshProviderRepo) SetGrokOAuthRefreshErrorIfCredentialsUnchanged(
	_ context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	errorMsg string,
) (bool, error) {
	r.conditionalErrorCalls++
	if r.conditionalErrorErr != nil {
		return false, r.conditionalErrorErr
	}
	provider := r.providersByID[id]
	if provider == nil {
		return false, nil
	}
	if r.reauthorizeOnErrorCAS {
		r.reauthorizeOnErrorCAS = false
		provider.Credentials = map[string]any{
			"access_token":   "fresh-access",
			"refresh_token":  "fresh-refresh",
			"_token_version": int64(2),
		}
		provider.Status = providercore.StatusActive
		provider.Schedulable = true
	}
	if r.repairProxyOnErrorCAS {
		r.repairProxyOnErrorCAS = false
		proxyID := int64(902)
		provider.ProxyID = &proxyID
	}
	if provider.Status != providercore.StatusActive || provider.Platform != capability.PlatformGrok || provider.Type != capability.ProviderTypeOAuth ||
		!reflect.DeepEqual(provider.Credentials, expectedCredentials) || !reflect.DeepEqual(provider.ProxyID, expectedProxyID) {
		return false, nil
	}
	r.setErrorCalls++
	r.lastErrorMessage = errorMsg
	provider.Status = providercore.StatusError
	provider.Schedulable = false
	provider.ErrorMessage = errorMsg
	return true, nil
}

func (r *tokenRefreshProviderRepo) UpdateGrokOAuthCredentialsIfUnchanged(
	_ context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	credentials map[string]any,
) (bool, error) {
	r.conditionalSuccessCalls++
	if r.conditionalSuccessErr != nil {
		return false, r.conditionalSuccessErr
	}
	provider := r.providersByID[id]
	if provider != nil && r.mutateSchedulingOnSuccessCAS {
		r.mutateSchedulingOnSuccessCAS = false
		provider.Status = providercore.StatusDisabled
		provider.Schedulable = false
		resetAt := time.Now().Add(30 * time.Minute)
		provider.RateLimitResetAt = &resetAt
	}
	if provider == nil || provider.Platform != capability.PlatformGrok ||
		provider.Type != capability.ProviderTypeOAuth || !reflect.DeepEqual(provider.Credentials, expectedCredentials) ||
		!reflect.DeepEqual(provider.ProxyID, expectedProxyID) {
		return false, nil
	}
	r.updateCalls++
	r.updateCredentialsCalls++
	provider.Credentials = maps.Clone(credentials)
	r.lastProvider = provider
	if r.cancelOnUpdate != nil {
		r.cancelOnUpdate()
	}
	return true, nil
}

func (r *tokenRefreshProviderRepo) SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged(
	_ context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	until time.Time,
	reason string,
) (bool, error) {
	r.conditionalTempCalls++
	if r.conditionalTempErr != nil {
		return false, r.conditionalTempErr
	}
	provider := r.providersByID[id]
	if provider == nil {
		return false, nil
	}
	if r.reauthorizeOnTempCAS {
		r.reauthorizeOnTempCAS = false
		provider.Credentials = map[string]any{
			"access_token":   "fresh-access",
			"refresh_token":  "fresh-refresh",
			"_token_version": int64(2),
		}
		provider.Status = providercore.StatusActive
		provider.Schedulable = true
	}
	if r.repairProxyOnTempCAS {
		r.repairProxyOnTempCAS = false
		proxyID := int64(902)
		provider.ProxyID = &proxyID
	}
	if provider.Status != providercore.StatusActive || provider.Platform != capability.PlatformGrok || provider.Type != capability.ProviderTypeOAuth ||
		!reflect.DeepEqual(provider.Credentials, expectedCredentials) || !reflect.DeepEqual(provider.ProxyID, expectedProxyID) {
		return false, nil
	}
	r.setTempUnschedCalls++
	r.lastTempUnschedReason = reason
	provider.TempUnschedulableUntil = &until
	provider.TempUnschedulableReason = reason
	return true, nil
}

func (r *tokenRefreshProviderRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	r.updateExtraCalls++
	r.lastExtraUpdates = maps.Clone(updates)
	if r.providersByID != nil {
		if acc, ok := r.providersByID[id]; ok && acc != nil {
			if acc.Extra == nil {
				acc.Extra = make(map[string]any, len(updates))
			}
			maps.Copy(acc.Extra, updates)
		}
	}
	return nil
}

func (r *tokenRefresherStub) CanRefresh(provider *providercore.Record) bool {
	return true
}

func (r *tokenRefresherStub) NeedsRefresh(provider *providercore.Record, refreshWindowDuration time.Duration) bool {
	return true
}

func (r *tokenRefresherStub) Refresh(ctx context.Context, provider *providercore.Record) (map[string]any, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.credentials, nil
}

func (r *tokenRefresherStub) CacheKey(provider *providercore.Record) string {
	return "test:stub:" + provider.Platform
}

func newRefreshAPI(repo providercore.RefreshRepository, cache providercore.RefreshCache) *providercore.OAuthRefreshAPI {
	return providercore.NewOAuthRefreshAPI(repo, cache, providercore.RefreshOptions{Platform: providercore.ProviderRefreshPlatformPolicy()})
}

func (r *refreshRecordFixture) GetByID(_ context.Context, id int64) (*providercore.Record, error) {
	if value := r.providersByID[id]; value != nil {
		return value, nil
	}
	return nil, errors.New("provider not found")
}

func (m *sessionWindowMockRepo) UpdateSessionWindow(_ context.Context, id int64, start, end *time.Time, status string) error {
	m.sessionWindowCalls = append(m.sessionWindowCalls, swCall{ID: id, Start: start, End: end, Status: status})
	return nil
}

func (m *sessionWindowMockRepo) UpdateSessionWindowEnd(_ context.Context, _ int64, _ time.Time) error {
	return nil
}

func (m *sessionWindowMockRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	m.updateExtraCalls = append(m.updateExtraCalls, ueCall{ID: id, Updates: updates})
	return nil
}

func (m *sessionWindowMockRepo) ClearRateLimit(_ context.Context, id int64) error {
	m.clearRateLimitIDs = append(m.clearRateLimitIDs, id)
	return nil
}

func (m *sessionWindowMockRepo) ClearAntigravityQuotaScopes(_ context.Context, _ int64) error {
	return nil
}

func (m *sessionWindowMockRepo) ClearModelRateLimits(_ context.Context, _ int64) error {
	return nil
}

func (m *sessionWindowMockRepo) ClearTempUnschedulable(_ context.Context, _ int64) error {
	return nil
}

func (m *sessionWindowMockRepo) GetByID(context.Context, int64) (*providercore.Record, error) {
	panic("unexpected")
}

func (m *sessionWindowMockRepo) ClearError(context.Context, int64) error { panic("unexpected") }

func (m *sessionWindowMockRepo) SetError(context.Context, int64, string) error { panic("unexpected") }

func (m *sessionWindowMockRepo) SetOverloaded(context.Context, int64, time.Time) error {
	panic("unexpected")
}

func (m *sessionWindowMockRepo) SetRateLimited(context.Context, int64, time.Time) error {
	panic("unexpected")
}

func (m *sessionWindowMockRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	panic("unexpected")
}

func (m *sessionWindowMockRepo) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	panic("unexpected")
}

// UpdateOAuthCredentialsIfUnchanged checks the observed credentials before invoking the configurable
// write stub.
func (r *tokenRefreshProviderRepo) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version providercore.CredentialVersion, credentials map[string]any) (bool, error) {
	current := r.providersByID[version.ID]
	if current == nil {
		return false, nil
	}
	expected := current.Credentials
	if expected == nil {
		expected = map[string]any{}
	}
	if current.ID != version.ID || current.Platform != version.Platform || current.Type != version.Type || current.Status != version.Status || !reflect.DeepEqual(current.ProxyID, version.ProxyID) || !reflect.DeepEqual(expected, version.Credentials) {
		return false, nil
	}
	err := r.UpdateCredentials(ctx, version.ID, credentials)
	return err == nil, err
}

// ClearRefreshCooldownIfUnchanged checks the observed window when a row exists and records the clear call.
func (r *tokenRefreshProviderRepo) ClearRefreshCooldownIfUnchanged(ctx context.Context, v providercore.RefreshCooldownVersion) (bool, error) {
	if current := r.providersByID[v.ID]; current != nil && !reflect.DeepEqual(providercore.ObserveRefreshCooldown(current), v) {
		return false, nil
	}
	return true, r.ClearTempUnschedulable(ctx, v.ID)
}

// ApplyOAuthRefreshFailure checks the current version when records are supplied, then records the
// requested health write.
func (r *tokenRefreshProviderRepo) ApplyOAuthRefreshFailure(ctx context.Context, version providercore.RefreshFailureVersion, failure providercore.RefreshFailure) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if r.providersByID != nil && !refreshFailureMatchesFixture(r.providersByID[version.ID], version) {
		return false, nil
	}
	var err error
	if failure.Kind == providercore.RefreshFailurePermanent {
		err = r.SetError(ctx, version.ID, failure.Message)
	} else {
		err = r.SetTempUnschedulable(ctx, version.ID, failure.Until, failure.Message)
	}
	if err == nil && failure.Kind == providercore.RefreshFailurePermanent && r.providersByID != nil {
		if value := r.providersByID[version.ID]; value != nil {
			value.Status = providercore.StatusError
			value.Schedulable = false
			value.ErrorMessage = failure.Message
		}
	}
	return err == nil, err
}

func (r *tokenRefreshProviderRepo) ClearAntigravityRefreshRequest(ctx context.Context, version providercore.CredentialVersion) (bool, error) {
	if r.providersByID != nil {
		value := r.providersByID[version.ID]
		if value == nil || !refreshFailureMatchesFixture(value, providercore.RefreshFailureVersion{CredentialVersion: version, Schedulable: value.Schedulable}) {
			return false, nil
		}
	}
	err := r.UpdateExtra(ctx, version.ID, providercore.ClearedAntigravityRefreshRequest())
	return err == nil, err
}

// matches rejects observations whose identity differs from the current record.
func (r *qoderObservationIdentityRepo) matches(value providercore.UsageObservationVersion) bool {
	current := r.current
	return current.ID == value.ID && current.Platform == value.Platform && current.Type == value.Type && current.Status == value.Status && reflect.DeepEqual(current.Credentials, value.Credentials) && reflect.DeepEqual(current.ProxyID, value.ProxyID)
}

func (r *qoderObservationIdentityRepo) UpdateUsageExtraIfUnchanged(ctx context.Context, value providercore.UsageObservationVersion, updates map[string]any) (bool, error) {
	if !r.matches(value) {
		return false, nil
	}
	return true, r.UpdateExtra(ctx, value.ID, updates)
}

func (r *qoderObservationIdentityRepo) SetUsageRateLimitIfUnchanged(ctx context.Context, value providercore.UsageObservationVersion, reset time.Time) (bool, error) {
	if !r.matches(value) {
		return false, nil
	}
	return true, r.SetRateLimited(ctx, value.ID, reset)
}

func (r *qoderObservationIdentityRepo) ClearUsageRateLimitIfUnchanged(ctx context.Context, value providercore.UsageObservationVersion) (bool, error) {
	if !r.matches(value) {
		return false, nil
	}
	return true, r.ClearRateLimit(ctx, value.ID)
}
