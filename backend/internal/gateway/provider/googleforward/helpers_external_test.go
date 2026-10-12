package googleforward_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/googleforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient"
	"github.com/TokenFlux/TokenRouter/internal/infra/httpclient/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logredact"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

const (
	geminiSkippedTestUpstreamMsg = "antigravity executor: invalid Gemini function call history"
	geminiTestPNG                = "iVBORw0KGgoAAAANSUhEUg=="
)

type antigravityCompatTokenCache struct {
	token string
}

type httpUpstreamStub struct {
	resp *http.Response
	err  error
}

type queuedHTTPUpstreamStub struct {
	responses     []*http.Response
	errors        []error
	requestBodies [][]byte
	callCount     int
	onCall        func(*http.Request, *queuedHTTPUpstreamStub)
}

type antigravitySettingRepoStub struct{}

// geminiDependencies holds token, transport, and health-observation test dependencies.
type geminiDependencies struct {
	cfg                  *googleforward.Options
	providerRepo         gatewayprovider.ExecutionProviderStore
	tokenProvider        *providercore.GeminiTokenSource
	httpUpstream         httpclient.UpstreamTransport
	healthObserver       *provideradapter.UpstreamHealth
	quotaPrecheck        *providercore.GeminiPrecheck
	responseHeaderFilter *egress.CompiledHeaderFilter
}

type antigravityDependencies struct {
	options          googleforward.Options
	settingService   *gatewayprovider.RuntimeReaders
	providerRepo     gatewayprovider.ExecutionProviderStore
	tokenProvider    *providercore.AntigravityTokenSource
	httpUpstream     httpclient.UpstreamTransport
	healthObserver   *provideradapter.UpstreamHealth
	cache            session.GatewayCache
	internal500Cache providercore.Internal500CounterCache
}

type geminiCompatHTTPUpstreamStub struct {
	response *http.Response
	err      error
	calls    int
	lastReq  *http.Request
}

func (c *antigravityCompatTokenCache) GetAccessToken(context.Context, string) (string, error) {
	return c.token, nil
}

func (c *antigravityCompatTokenCache) SetAccessToken(context.Context, string, string, time.Duration) error {
	return nil
}

func (c *antigravityCompatTokenCache) DeleteAccessToken(context.Context, string) error {
	return nil
}

func (c *antigravityCompatTokenCache) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}

func (c *antigravityCompatTokenCache) ReleaseRefreshLock(context.Context, string) error {
	return nil
}

func newAntigravityCompatibilityFixture(cfg googleforward.Options, upstream httpclient.UpstreamTransport) *googleforward.Antigravity {
	tokenProvider := newAntigravityTokenSourceForTest(&antigravityCompatTokenCache{token: "fresh-oauth-token"})
	return newAntigravityFixture(antigravityDependencies{
		tokenProvider:  tokenProvider,
		httpUpstream:   upstream,
		settingService: newExecutionReadersFixture(&antigravitySettingRepoStub{}), options: fixtureOptions(&cfg),
	})
}

func newAntigravityCompatContext(method, path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(body))
	return c, recorder
}

// newAntigravityStreamFixture constructs an Antigravity instance for stream tests.
func newAntigravityStreamFixture(cfg *googleforward.Options) *googleforward.Antigravity {
	return newAntigravityFixture(antigravityDependencies{
		settingService: newExecutionReadersFixture(nil), options: fixtureOptions(cfg),
	})
}

func (s *httpUpstreamStub) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return s.resp, s.err
}

func (s *httpUpstreamStub) DoWithTLS(_ *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.resp, s.err
}

func (s *queuedHTTPUpstreamStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if req != nil && req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		s.requestBodies = append(s.requestBodies, body)
		req.Body = io.NopCloser(bytes.NewReader(body))
	} else {
		s.requestBodies = append(s.requestBodies, nil)
	}

	idx := s.callCount
	s.callCount++
	if s.onCall != nil {
		s.onCall(req, s)
	}

	var resp *http.Response
	if idx < len(s.responses) {
		resp = s.responses[idx]
	}
	var err error
	if idx < len(s.errors) {
		err = s.errors[idx]
	}
	if resp == nil && err == nil {
		return nil, errors.New("unexpected upstream call")
	}
	return resp, err
}

func (s *queuedHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, providerID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, providerID, concurrency)
}

func (s *antigravitySettingRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

func (s *antigravitySettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	return "", settingscore.ErrSettingNotFound
}

func (s *antigravitySettingRepoStub) Set(ctx context.Context, key, value string) error {
	panic("unexpected Set call")
}

func (s *antigravitySettingRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

func (s *antigravitySettingRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *antigravitySettingRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *antigravitySettingRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

func fixtureOptions(cfg *googleforward.Options) googleforward.Options {
	if cfg == nil {
		return googleforward.Options{ResponseReadLimit: 128 << 20}
	}
	out := *cfg
	out.Configured = true
	if out.ResponseReadLimit <= 0 {
		out.ResponseReadLimit = 128 << 20
	}
	return out
}

func geminiQuotaLocation() *time.Location {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return time.FixedZone("PST", -8*3600)
	}
	return loc
}

func nextGeminiDailyResetUnix() *int64 {
	value := providercore.GeminiDailyResetTime(time.Now(), geminiQuotaLocation()).Unix()
	return &value
}

func parseGeminiReset(body []byte) *int64 {
	return gemininative.ParseGeminiRateLimitResetTime(body, nextGeminiDailyResetUnix)
}

func newGeminiFixture(d geminiDependencies) *googleforward.Gemini {
	health := &provideradapter.GeminiErrorObserver{
		Other:      d.healthObserver,
		Precheck:   d.quotaPrecheck,
		DailyReset: nextGeminiDailyResetUnix,
		ResetTime:  parseGeminiReset,
	}
	if d.providerRepo != nil {
		health.SetRateLimited = d.providerRepo.SetRateLimited
	}
	return &googleforward.Gemini{
		Options:      fixtureOptions(d.cfg),
		Tokens:       d.tokenProvider,
		Health:       d.healthObserver,
		Errors:       health,
		Transport:    d.httpUpstream,
		HeaderFilter: d.responseHeaderFilter,
	}
}

func newAntigravityFixture(d antigravityDependencies) *googleforward.Antigravity {
	o := d.options
	health := &providercore.AntigravityHealth{
		Store:     d.providerRepo,
		Counter:   d.internal500Cache,
		ModelKeys: provideradapter.AntigravityModelLimitKeys,
		Error:     slog.Error,
		Warn:      slog.Warn,
		Info:      slog.Info,
		Logf:      func(format string, args ...any) { logging.LegacyPrintf("service.antigravity_gateway", format, args...) },
	}
	retry := &provideradapter.AntigravityRetry{
		Health: health,
		BaseURL: func(value *providercore.Record) string {
			return antigravity.ResolveAntigravityForwardBaseURL(os.Getenv("GATEWAY_ANTIGRAVITY_FORWARD_BASE_URL"), provideradapter.AntigravityPaidTier(value))
		},
		BodyLimit: func() int64 {
			limit := int64(512 << 10)
			if o.LogErrorBody && o.LogErrorBodyMaxBytes > int(limit) {
				limit = int64(o.LogErrorBodyMaxBytes)
			}
			return limit
		},
		TruncateString: logredact.TruncateUTF8,
		SafeURL:        logredact.SafeUpstreamURL,
	}
	if d.healthObserver != nil {
		retry.Policy = d.healthObserver.Core
	}
	observer := &provideradapter.AntigravityErrorObserver{
		Health:         health,
		Other:          d.healthObserver,
		LogConfig:      o.LogConfig,
		TruncateString: logredact.TruncateUTF8,
		ResetTime:      parseGeminiReset,
		DefaultDuration: func() time.Duration {
			minutes := 0
			return provideradapter.AntigravityFallbackDuration(minutes, os.Getenv("GATEWAY_ANTIGRAVITY_FALLBACK_COOLDOWN_SECONDS"))
		},
	}
	if d.providerRepo != nil {
		observer.SetRateLimited = d.providerRepo.SetRateLimited
	}
	r := &googleforward.Antigravity{
		Options:   o,
		Tokens:    d.tokenProvider,
		Retry:     retry,
		Errors:    observer,
		Store:     d.providerRepo,
		Transport: d.httpUpstream,
		Sticky:    d.cache,
	}
	if d.settingService != nil {
		r.Gateway = d.settingService.Gateway
		r.Routing = d.settingService.Routing
	}
	return r
}

// newExecutionReadersFixture binds test settings to execution-time readers.
func newExecutionReadersFixture(repo settingscore.Repository) *gatewayprovider.RuntimeReaders {
	if repo != nil {
		repo = settingscore.New(repo)
	}
	value := gatewaytestkit.RuntimeReaders(repo)
	return value
}

func geminiSkippedTestUpstreamBody() string {
	return `{"error":{"code":null,"message":"` + geminiSkippedTestUpstreamMsg + `","param":"","type":"invalid_request_error"}}`
}

func newGeminiErrorFixture(status int, body string) (*googleforward.Gemini, *geminiCompatHTTPUpstreamStub) {
	httpStub := &geminiCompatHTTPUpstreamStub{
		response: &http.Response{
			StatusCode: status,

			Header: http.Header{"Content-Type": []string{"application/json"}},

			Body: io.NopCloser(strings.NewReader(body)),
		},
	}
	svc := newGeminiFixture(geminiDependencies{
		httpUpstream: httpStub,

		cfg: &googleforward.Options{},

		healthObserver: newUpstreamHealthForTest(&gatewaytestkit.ErrorPolicyStore{}, &googleforward.Options{}, nil, providercore.HealthOptions{}, nil),
	})
	return svc, httpStub
}

func geminiPoolModeAPIKeyProvider() *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           700,

			Platform: capability.PlatformGemini,

			Type: capability.ProviderTypeAPIKey,

			Credentials: map[string]any{
				"api_key":   "test-key",
				"pool_mode": true,
			},
		},
	}
}

func geminiCustomCodesAPIKeyProvider() *gatewayprovider.ExecutionProvider {
	return &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           701,

			Platform: capability.PlatformGemini,

			Type: capability.ProviderTypeAPIKey,

			Credentials: map[string]any{
				"api_key": "test-key",

				"custom_error_codes_enabled": true,

				"custom_error_codes": []any{float64(429)},
			},
		},
	}
}

func newGeminiImageTestContext(t *testing.T) *googleforward.AttemptForTest {
	t.Helper()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost,
		"/v1beta/models/nana-banana-2:generateContent", strings.NewReader("{}"))
	return &googleforward.AttemptForTest{Output: gatewayhttp.NewGoogleBoundary(c, googleforward.Options{ResponseReadLimit: 128 << 20}, false)}
}

func geminiImageResponse(parts string) string {
	return `{"candidates":[{"content":{"role":"model","parts":[` + parts + `]},"finishReason":"STOP"}]}`
}

func (s *geminiCompatHTTPUpstreamStub) Do(req *http.Request, proxyURL string, providerID int64, providerConcurrency int) (*http.Response, error) {
	s.calls++
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	if s.response == nil {
		return nil, fmt.Errorf("missing stub response")
	}
	resp := *s.response
	return &resp, nil
}

func (s *geminiCompatHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, providerID int64, providerConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, providerID, providerConcurrency)
}

// newGeminiTokenSourceForTest constructs a token source with default test options.
func newGeminiTokenSourceForTest() *providercore.GeminiTokenSource {
	return &providercore.GeminiTokenSource{Options: providercore.GeminiTokenOptions{
		Debug: slog.Debug,
		Warn:  slog.Warn,

		Vertex: func(ctx context.Context, value *providercore.Record) (string, error) {
			return provideradapter.VertexServiceAccountAccessToken(ctx, nil, value)
		},
	}}
}

func newAntigravityTokenSourceForTest(cache providercore.AccessTokenCache) *providercore.AntigravityTokenSource {
	return &providercore.AntigravityTokenSource{Options: providercore.AntigravityTokenOptions{
		Cache: cache, Debug: slog.Debug, Warn: slog.Warn,
	}}
}

// newUpstreamHealthForTest constructs a health observer from test dependencies.
func newUpstreamHealthForTest(store gatewayprovider.ExecutionProviderStore, _ *googleforward.Options, cache providercore.TempUnschedCache, options providercore.HealthOptions, readers *gatewayprovider.RuntimeReaders) *provideradapter.UpstreamHealth {
	return gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Store: store, Cache: cache, Options: options, Readers: readers})
}
