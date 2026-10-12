package googleforward_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/gateway/errorpolicy"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/googleforward"
	gatewaytelemetry "github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog/testkit"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

type errorRulesFixtureRepo struct {
	errorpolicy.ErrorPassthroughRepository
	rules []*errorpolicy.ErrorPassthroughRule
}

type geminiErrorPolicyRepo struct {
	gatewaytestkit.ErrorPolicyStore
	setErrorCalls            int
	setRateLimitedCalls      int
	setTempCalls             int
	setModelRateLimitedCalls int
	lastModelScope           string
}

type rateLimit429ProviderRepoStub struct {
	gatewaytestkit.ErrorPolicyStore
	rateLimitCalls     int
	lastRateLimitID    int64
	lastRateLimitReset time.Time
}

func TestParseGeminiRateLimitResetTime_QuotaResetDelay_RoundsUp(t *testing.T) {
	// Avoid flakiness around Unix second boundaries.
	for {
		now := time.Now()
		if now.Nanosecond() < 800*1e6 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	baseUnix := time.Now().Unix()
	ts := parseGeminiReset(buildGeminiRateLimitBody("0.1s"))
	require.NotNil(t, ts)
	require.Equal(t, baseUnix+1, *ts, "fractional seconds should be rounded up to the next second")
}

func TestGeminiForwardNative_PoolModeSkipped400PassthroughRealStatus(t *testing.T) {
	upstreamBody := geminiSkippedTestUpstreamBody()
	svc, _ := newGeminiErrorFixture(http.StatusBadRequest, upstreamBody)
	c, rec := newGeminiNativeTestContext(t)

	result, err := svc.ForwardNative(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, false), geminiPoolModeAPIKeyProvider(), "gemini-2.5-flash", "generateContent", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))

	require.Nil(t, result)
	require.Error(t, err)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "池模式 400 不应换号")
	require.Contains(t, err.Error(), "gemini upstream error: 400")
	require.Equal(t, http.StatusBadRequest, rec.Code, "状态码应保真为上游 400")
	require.Equal(t, upstreamBody, rec.Body.String(), "响应体应原样透传")
}

func TestGeminiForwardNative_PoolModeSkipped503Failover(t *testing.T) {
	svc, _ := newGeminiErrorFixture(http.StatusServiceUnavailable, `{"error":{"message":"Upstream service temporarily unavailable"}}`)
	c, rec := newGeminiNativeTestContext(t)

	result, err := svc.ForwardNative(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, false), geminiPoolModeAPIKeyProvider(), "gemini-2.5-flash", "generateContent", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "池模式 503 应换号")
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
	require.Zero(t, rec.Body.Len(), "换号场景不应写客户端响应")
}

func TestGeminiForwardNative_CustomCodesMiss400HiddenAs500(t *testing.T) {
	svc, _ := newGeminiErrorFixture(http.StatusBadRequest, geminiSkippedTestUpstreamBody())
	c, rec := newGeminiNativeTestContext(t)

	result, err := svc.ForwardNative(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, false), geminiCustomCodesAPIKeyProvider(), "gemini-2.5-flash", "generateContent", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))

	require.Nil(t, result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in custom error codes")
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	errObj, ok := got["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Upstream gateway error", errObj["message"])
	require.NotContains(t, rec.Body.String(), geminiSkippedTestUpstreamMsg, "上游细节不应透传给客户端")
}

func TestGeminiForwardNative_CustomCodesMiss500Failover(t *testing.T) {
	svc, _ := newGeminiErrorFixture(http.StatusInternalServerError, `{"error":{"message":"internal"}}`)
	c, rec := newGeminiNativeTestContext(t)

	result, err := svc.ForwardNative(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, false), geminiCustomCodesAPIKeyProvider(), "gemini-2.5-flash", "generateContent", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "自定义错误码未命中的 500 应换号")
	require.Equal(t, http.StatusInternalServerError, failoverErr.StatusCode)
	require.False(t, failoverErr.RetryableOnSameProvider, "非池模式不应同提供商重试")
	require.Zero(t, rec.Body.Len())
}

func TestWriteGeminiMappedError_400KeepsUpstreamMessage(t *testing.T) {
	svc := newGeminiFixture(geminiDependencies{cfg: &googleforward.Options{}})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	err := gatewayhttp.NewGoogleBoundary(c, svc.Options, false).GeminiMappedError(&gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 702, Platform: capability.PlatformGemini}}, http.StatusBadRequest, "req-1", []byte(geminiSkippedTestUpstreamBody()))

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	errObj, ok := got["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, geminiSkippedTestUpstreamMsg, errObj["message"], "应回传上游 message")
}

func TestShouldFailoverGeminiUpstreamError(t *testing.T) {
	svc := newGeminiFixture(geminiDependencies{})

	tests := []struct {
		name       string
		statusCode int
		expected   bool
	}{
		{"401_failover", 401, true},

		{"403_failover", 403, true},

		{"429_failover", 429, true},

		{"529_failover", 529, true},

		{"500_failover", 500, true},

		{"502_failover", 502, true},

		{"503_failover", 503, true},

		{"400_no_failover", 400, false},

		{"404_no_failover", 404, false},

		{"422_no_failover", 422, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := googleforward.GeminiFailoverForTest(svc, tt.statusCode)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestCheckErrorPolicy_GeminiProviders(t *testing.T) {
	tests := []struct {
		name       string
		provider   *gatewayprovider.ExecutionProvider
		statusCode int
		body       []byte
		expected   providercore.ErrorPolicyResult
	}{
		{
			name: "gemini_apikey_custom_codes_hit",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           100,

					Type: capability.ProviderTypeAPIKey,

					Platform: capability.PlatformGemini,

					Credentials: map[string]any{
						"custom_error_codes_enabled": true,
						"custom_error_codes":         []any{float64(429), float64(500)},
					},
				},
			},

			statusCode: 429,

			body: []byte(`{"error":"rate limited"}`),

			expected: providercore.ErrorPolicyCustomMatched,
		},

		{
			name: "gemini_apikey_custom_codes_miss",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           101,

					Type: capability.ProviderTypeAPIKey,

					Platform: capability.PlatformGemini,

					Credentials: map[string]any{
						"custom_error_codes_enabled": true,
						"custom_error_codes":         []any{float64(429)},
					},
				},
			},

			statusCode: 500,

			body: []byte(`{"error":"internal"}`),

			expected: providercore.ErrorPolicyCustomSkipped,
		},

		{
			name: "gemini_apikey_no_custom_codes_returns_none",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           102,

					Type: capability.ProviderTypeAPIKey,

					Platform: capability.PlatformGemini,
				},
			},

			statusCode: 500,

			body: []byte(`{"error":"internal"}`),

			expected: providercore.ErrorPolicyNone,
		},

		{
			name: "gemini_apikey_temp_unschedulable_hit",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           103,

					Type: capability.ProviderTypeAPIKey,

					Platform: capability.PlatformGemini,

					Credentials: map[string]any{
						"temp_unschedulable_enabled": true,

						"temp_unschedulable_rules": []any{
							map[string]any{
								"error_code": float64(503),

								"keywords": []any{"overloaded"},

								"duration_minutes": float64(10),
							},
						},
					},
				},
			},

			statusCode: 503,

			body: []byte(`overloaded service`),

			expected: providercore.ErrorPolicyTempUnscheduled,
		},

		{
			name: "gemini_apikey_temp_unschedulable_401_second_hit_returns_none",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           105,

					Type: capability.ProviderTypeAPIKey,

					Platform: capability.PlatformGemini,

					TempUnschedulableReason: `{"status_code":401,"until_unix":1735689600}`,

					Credentials: map[string]any{
						"temp_unschedulable_enabled": true,

						"temp_unschedulable_rules": []any{
							map[string]any{
								"error_code": float64(401),

								"keywords": []any{"unauthorized"},

								"duration_minutes": float64(10),
							},
						},
					},
				},
			},

			statusCode: 401,

			body: []byte(`unauthorized`),

			expected: providercore.ErrorPolicyNone,
		},

		{
			name: "gemini_custom_codes_override_temp_unschedulable",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           104,

					Type: capability.ProviderTypeAPIKey,

					Platform: capability.PlatformGemini,

					Credentials: map[string]any{
						"custom_error_codes_enabled": true,

						"custom_error_codes": []any{float64(503)},

						"temp_unschedulable_enabled": true,

						"temp_unschedulable_rules": []any{
							map[string]any{
								"error_code": float64(503),

								"keywords": []any{"overloaded"},

								"duration_minutes": float64(10),
							},
						},
					},
				},
			},

			statusCode: 503,

			body: []byte(`overloaded`),

			expected: providercore.ErrorPolicyCustomMatched, // custom codes take precedence

		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &gatewaytestkit.ErrorPolicyStore{}
			svc := newUpstreamHealthForTest(repo, &googleforward.Options{}, nil, providercore.HealthOptions{}, nil)

			result := svc.CheckErrorPolicy(context.Background(), gatewayprovider.ExecutionRecord(tt.provider), gatewayprovider.HealthObservationFromContext(context.Background(), tt.statusCode, nil, tt.body, nil))
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestGeminiErrorPolicyIntegration(t *testing.T) {
	tests := []struct {
		name                 string
		provider             *gatewayprovider.ExecutionProvider
		statusCode           int
		respBody             []byte
		expectFailover       bool // expect UpstreamFailoverError
		expectHandleError    bool // expect handleGeminiUpstreamError to be called
		expectShouldFailover bool // for None path, whether shouldFailover triggers
		expectModelScope     string
	}{
		{
			name: "custom_codes_matched_429_failover",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           200,

					Type: capability.ProviderTypeAPIKey,

					Platform: capability.PlatformGemini,

					Credentials: map[string]any{
						"custom_error_codes_enabled": true,
						"custom_error_codes":         []any{float64(429)},
					},
				},
			},

			statusCode: 429,

			respBody: []byte(`{"error":"rate limited"}`),

			expectFailover: true,

			expectHandleError: true,
		},

		{
			name: "custom_codes_skipped_500_no_failover",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           201,

					Type: capability.ProviderTypeAPIKey,

					Platform: capability.PlatformGemini,

					Credentials: map[string]any{
						"custom_error_codes_enabled": true,
						"custom_error_codes":         []any{float64(429)},
					},
				},
			},

			statusCode: 500,

			respBody: []byte(`{"error":"internal"}`),

			expectFailover: false,

			expectHandleError: false,
		},

		{
			name: "temp_unschedulable_matched_failover",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           202,

					Type: capability.ProviderTypeAPIKey,

					Platform: capability.PlatformGemini,

					Credentials: map[string]any{
						"temp_unschedulable_enabled": true,

						"temp_unschedulable_rules": []any{
							map[string]any{
								"error_code": float64(503),

								"keywords": []any{"overloaded"},

								"duration_minutes": float64(10),
							},
						},
					},
				},
			},

			statusCode: 503,

			respBody: []byte(`overloaded`),

			expectFailover: true,

			expectHandleError: false,

			expectModelScope: "gemini-2.5-pro",
		},

		{
			name: "no_policy_429_failover_via_shouldFailover",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           203,

					Type: capability.ProviderTypeAPIKey,

					Platform: capability.PlatformGemini,
				},
			},

			statusCode: 429,

			respBody: []byte(`{"error":"rate limited"}`),

			expectFailover: true,

			expectHandleError: true,

			expectShouldFailover: true,
		},

		{
			name: "no_policy_400_no_failover",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           204,

					Type: capability.ProviderTypeAPIKey,

					Platform: capability.PlatformGemini,
				},
			},

			statusCode: 400,

			respBody: []byte(`{"error":"bad request"}`),

			expectFailover: false,

			expectHandleError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &geminiErrorPolicyRepo{}
			rlSvc := newUpstreamHealthForTest(repo, &googleforward.Options{}, nil, providercore.HealthOptions{}, nil)

			svc := newGeminiFixture(geminiDependencies{
				providerRepo:   repo,
				healthObserver: rlSvc,
			})

			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			// Simulate the Claude compat error handling path (same logic as native).
			// This mirrors the inline switch in handleClaudeCompat.
			var handleErrorCalled bool
			var gotFailover bool

			ctx := context.Background()
			statusCode := tt.statusCode
			respBody := tt.respBody
			provider := tt.provider
			headers := http.Header{}

			if svc.Health != nil {
				policy := svc.Health.CheckErrorPolicy(ctx, gatewayprovider.ExecutionRecord(provider), gatewayprovider.HealthObservationFromContext(ctx, statusCode, nil, respBody, []string{"gemini-2.5-pro"}))
				switch policy {
				case providercore.ErrorPolicyCustomSkipped:
					// Skipped → return error directly (no handleGeminiUpstreamError, no failover)
					gotFailover = false
					handleErrorCalled = false
					goto verify
				case providercore.ErrorPolicyCustomMatched:
					svc.Errors.Observe(ctx, gatewayprovider.ExecutionRecord(provider), statusCode, headers, respBody, gatewayprovider.HealthObservationFromContext(ctx, statusCode, headers, respBody, nil))
					handleErrorCalled = true
					gotFailover = true
					goto verify
				case providercore.ErrorPolicyTempUnscheduled:
					handleErrorCalled = false
					gotFailover = true
					goto verify
				}
			}

			// ErrorPolicyNone → original logic
			svc.Errors.Observe(ctx, gatewayprovider.ExecutionRecord(provider), statusCode, headers, respBody, gatewayprovider.HealthObservationFromContext(ctx, statusCode, headers, respBody, nil))
			handleErrorCalled = true
			if googleforward.GeminiFailoverForTest(svc, statusCode) {
				gotFailover = true
			}

		verify:
			require.Equal(t, tt.expectFailover, gotFailover, "failover mismatch")
			require.Equal(t, tt.expectHandleError, handleErrorCalled, "handleGeminiUpstreamError call mismatch")
			if tt.expectModelScope != "" {
				require.Equal(t, 1, repo.setModelRateLimitedCalls)
				require.Equal(t, tt.expectModelScope, repo.lastModelScope)
				require.Zero(t, repo.setTempCalls)
				require.Zero(t, repo.setRateLimitedCalls, "model temp rule must not be widened into a provider rate limit")
			}

			if tt.expectShouldFailover {
				require.True(t, googleforward.GeminiFailoverForTest(svc, statusCode),
					"shouldFailoverGeminiUpstreamError should return true for status %d", statusCode)
			}
		})
	}
}

func TestGeminiErrorPolicy_NilRateLimitService(t *testing.T) {
	svc := newGeminiFixture(geminiDependencies{
		healthObserver: nil,
	})

	// When healthObserver is nil, error policy is skipped → falls through to
	// shouldFailoverGeminiUpstreamError (original logic).
	// Verify this doesn't panic and follows expected behavior.

	ctx := context.Background()
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           300,

			Type: capability.ProviderTypeAPIKey,

			Platform: capability.PlatformGemini,

			Credentials: map[string]any{
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(429)},
			},
		},
	}

	// The nil check should prevent CheckErrorPolicy from being called
	if svc.Health != nil {
		t.Fatal("healthObserver should be nil for this test")
	}

	// shouldFailoverGeminiUpstreamError still works
	require.True(t, googleforward.GeminiFailoverForTest(svc, 429))
	require.False(t, googleforward.GeminiFailoverForTest(svc, 400))

	// handleGeminiUpstreamError should not panic with nil healthObserver
	require.NotPanics(t, func() {
		svc.Errors.Observe(ctx, gatewayprovider.ExecutionRecord(provider), 500, http.Header{}, []byte(`error`), gatewayprovider.HealthObservationFromContext(ctx, 500, http.Header{}, []byte(`error`), nil))
	})
}

func TestHandleGeminiUpstreamError_GoogleOneCapacityExhaustedUsesTierCooldown(t *testing.T) {
	repo := &rateLimit429ProviderRepoStub{}
	quotaSvc := providercore.NewGeminiQuotaService(providercore.GeminiQuotaOptions{})
	rlSvc := newUpstreamHealthForTest(repo, &googleforward.Options{}, nil, providercore.HealthOptions{}, nil)

	svc := newGeminiFixture(geminiDependencies{
		quotaPrecheck: providercore.NewGeminiPrecheck(quotaSvc, nil, providercore.GeminiPrecheckOptions{Now: time.Now, Location: geminiQuotaLocation()}),

		providerRepo: repo,

		healthObserver: rlSvc,
	})

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           511,

			Platform: capability.PlatformGemini,

			Type: capability.ProviderTypeOAuth,

			Credentials: map[string]any{
				"oauth_type": "google_one",
				"tier_id":    "google_ai_pro",
			},
		},
	}
	body := []byte(`{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","domain":"cloudcode-pa.googleapis.com","metadata":{"model":"gemini-3.1-pro-preview"},"reason":"MODEL_CAPACITY_EXHAUSTED"}],"message":"No capacity available for model gemini-3.1-pro-preview on the server","status":"RESOURCE_EXHAUSTED"}}`)

	before := time.Now()
	svc.Errors.Observe(context.Background(), gatewayprovider.ExecutionRecord(provider), http.StatusTooManyRequests, http.Header{}, body, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusTooManyRequests, http.Header{}, body, nil))
	after := time.Now()

	require.Equal(t, 1, repo.rateLimitCalls)
	require.Equal(t, int64(511), repo.lastRateLimitID)
	require.WithinDuration(t, before.Add(5*time.Minute), repo.lastRateLimitReset, 2*time.Second)
	require.True(t, repo.lastRateLimitReset.After(before))
	require.True(t, repo.lastRateLimitReset.Before(after.Add(5*time.Minute).Add(2*time.Second)))
}

func TestHandleGeminiUpstreamError_ThirdPartyAPIKeyIgnoresOfficialQuotaMessage(t *testing.T) {
	repo := &rateLimit429ProviderRepoStub{}
	quotaSvc := providercore.NewGeminiQuotaService(providercore.GeminiQuotaOptions{})
	rlSvc := newUpstreamHealthForTest(repo, &googleforward.Options{}, nil, providercore.HealthOptions{}, nil)

	svc := newGeminiFixture(geminiDependencies{
		quotaPrecheck: providercore.NewGeminiPrecheck(quotaSvc, nil, providercore.GeminiPrecheckOptions{Now: time.Now, Location: geminiQuotaLocation()}),

		providerRepo: repo,

		healthObserver: rlSvc,
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           512,

			Platform: capability.PlatformGemini,

			Type: capability.ProviderTypeAPIKey,

			Credentials: map[string]any{
				providercore.GeminiProviderTypeCredentialKey: providercore.GeminiProviderTypeThirdParty,
			},
		},
	}

	before := time.Now()
	svc.Errors.Observe(context.Background(), gatewayprovider.ExecutionRecord(provider), http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"code":429,"message":"Quota exceeded: 20 requests per day"}}`), gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"code":429,"message":"Quota exceeded: 20 requests per day"}}`), nil))
	after := time.Now()

	require.Equal(t, 1, repo.rateLimitCalls)
	require.Equal(t, int64(512), repo.lastRateLimitID)
	require.WithinDuration(t, before.Add(5*time.Minute), repo.lastRateLimitReset, 2*time.Second)
	require.True(t, repo.lastRateLimitReset.After(before))
	require.True(t, repo.lastRateLimitReset.Before(after.Add(5*time.Minute).Add(2*time.Second)))
}

// TestGeminiPoolMode429BypassesLocalRateLimit 验证池模式不再被当作自定义未命中，
// 也不会继续执行 Gemini 默认 429 限流写入。
func TestGeminiPoolMode429BypassesLocalRateLimit(t *testing.T) {
	repo := &geminiErrorPolicyRepo{}
	healthObserver := newUpstreamHealthForTest(repo, &googleforward.Options{}, nil, providercore.HealthOptions{}, nil)

	svc := newGeminiFixture(geminiDependencies{providerRepo: repo, healthObserver: healthObserver})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           520,

			Type: capability.ProviderTypeAPIKey,

			Platform: capability.PlatformGemini,

			Credentials: map[string]any{
				"pool_mode": true,
			},
		},
	}

	decision := googleforward.GeminiPolicyForTest(svc, context.Background(), provider, http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"message":"rate limited"}}`), "gemini-2.5-pro")

	require.Equal(t, providercore.ErrorPolicyPoolBypassed, decision.Policy)
	require.True(t, decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), http.StatusTooManyRequests))
	require.Zero(t, repo.setRateLimitedCalls)
	require.Zero(t, repo.setTempCalls)
	require.Zero(t, repo.setErrorCalls)
}

func TestHandleGeminiUpstreamError_PoolMode429SkipsProviderLimit(t *testing.T) {
	body := []byte(`{"error":{"code":429,"message":"capacity exhausted"}}`)
	tests := []struct {
		name      string
		provider  *gatewayprovider.ExecutionProvider
		wantCalls int
	}{
		{
			name: "池模式跳过默认提供商限流",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           530,
					Type:         capability.ProviderTypeAPIKey,
					Platform:     capability.PlatformGemini,

					Credentials: map[string]any{"pool_mode": true},
				},
			},
		},

		{
			name: "自定义错误码命中优先于池模式",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           531,
					Type:         capability.ProviderTypeAPIKey,
					Platform:     capability.PlatformGemini,

					Credentials: map[string]any{
						"pool_mode": true,

						"custom_error_codes_enabled": true,

						"custom_error_codes": []any{float64(http.StatusTooManyRequests)},
					},
				},
			},

			wantCalls: 1,
		},

		{
			name: "自定义错误码未命中跳过提供商限流",

			provider: &gatewayprovider.ExecutionProvider{
				Record: providercore.Record{
					LoadLocation: time.LoadLocation,
					ID:           532,
					Type:         capability.ProviderTypeAPIKey,
					Platform:     capability.PlatformGemini,

					Credentials: map[string]any{
						"pool_mode": true,

						"custom_error_codes_enabled": true,

						"custom_error_codes": []any{float64(http.StatusInternalServerError)},
					},
				},
			},
		},

		{
			name: "普通提供商保留默认限流",

			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 533, Type: capability.ProviderTypeAPIKey, Platform: capability.PlatformGemini}},

			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &rateLimit429ProviderRepoStub{}
			svc := newGeminiFixture(geminiDependencies{providerRepo: repo})

			svc.Errors.Observe(context.Background(), gatewayprovider.ExecutionRecord(tt.provider), http.StatusTooManyRequests, http.Header{}, body, gatewayprovider.HealthObservationFromContext(context.Background(), http.StatusTooManyRequests, http.Header{}, body, nil))

			require.Equal(t, tt.wantCalls, repo.rateLimitCalls)
		})
	}
}

// TestGeminiCustomNonFailoverStatusStopsScheduling 验证非默认故障转移状态也会执行
// 管理员配置的策略会写入提供商错误。
func TestGeminiCustomNonFailoverStatusStopsScheduling(t *testing.T) {
	repo := &geminiErrorPolicyRepo{}
	healthObserver := newUpstreamHealthForTest(repo, &googleforward.Options{}, nil, providercore.HealthOptions{}, nil)

	svc := newGeminiFixture(geminiDependencies{providerRepo: repo, healthObserver: healthObserver})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           521,

			Type: capability.ProviderTypeAPIKey,

			Platform: capability.PlatformGemini,

			Credentials: map[string]any{
				"pool_mode": true,

				"custom_error_codes_enabled": true,

				"custom_error_codes": []any{float64(http.StatusUnprocessableEntity)},
			},
		},
	}

	decision := googleforward.GeminiPolicyForTest(svc, context.Background(), provider, http.StatusUnprocessableEntity, http.Header{}, []byte(`{"error":{"message":"configured"}}`), "gemini-2.5-pro")

	require.Equal(t, providercore.ErrorPolicyCustomMatched, decision.Policy)
	require.True(t, decision.StopScheduling)
	require.False(t, decision.RetryableOnSameProvider(gatewayprovider.ExecutionErrorPolicy(provider), http.StatusUnprocessableEntity))
	require.Equal(t, 1, repo.setErrorCalls)
	require.Zero(t, repo.setRateLimitedCalls)
}

func TestGeminiWriteGeminiMappedError_NoRuleKeepsDefault(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := newGeminiFixture(geminiDependencies{})
	respBody := []byte(`{"error":{"code":422,"message":"Invalid schema for field messages","status":"INVALID_ARGUMENT"}}`)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 13, Platform: capability.PlatformGemini, Type: capability.ProviderTypeAPIKey}}

	err := gatewayhttp.NewGoogleBoundary(c, svc.Options, false).GeminiMappedError(provider, http.StatusUnprocessableEntity, "req-2", respBody)
	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "invalid_request_error", errField["type"])
	assert.Equal(t, "Upstream request failed", errField["message"])
}

func TestGeminiWriteGeminiMappedError_AppliesRuleFor422(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	ruleSvc := newErrorRulesTestService([]*errorpolicy.ErrorPassthroughRule{newNonFailoverPassthroughRule(http.StatusUnprocessableEntity, "invalid schema", http.StatusTeapot, "Gemini上游失败")})
	gatewayhttp.BindErrorPassthroughService(c, ruleSvc)

	svc := newGeminiFixture(geminiDependencies{})
	respBody := []byte(`{"error":{"code":422,"message":"Invalid schema for field messages","status":"INVALID_ARGUMENT"}}`)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 3, Platform: capability.PlatformGemini, Type: capability.ProviderTypeAPIKey}}

	err := gatewayhttp.NewGoogleBoundary(c, svc.Options, false).GeminiMappedError(provider, http.StatusUnprocessableEntity, "req-1", respBody)
	require.Error(t, err)
	assert.Equal(t, http.StatusTeapot, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errField["type"])
	assert.Equal(t, "Gemini上游失败", errField["message"])
}

func TestGeminiWriteGeminiMappedError_SetsResponseCommitted(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := newGeminiFixture(geminiDependencies{})
	body := []byte(`{"error":{"message":"invalid field"}}`)
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, ID: 102, Platform: capability.PlatformGemini, Type: capability.ProviderTypeAPIKey}}

	err := gatewayhttp.NewGoogleBoundary(c, svc.Options, false).GeminiMappedError(provider, http.StatusBadRequest, "req-99", body)
	require.Error(t, err)
	assert.True(t, gatewayhttp.IsResponseCommitted(c), "Gemini path must mark response committed")
}

// TestGeminiMessagesCompatServiceForward_OAuthAppliesProviderModelMapping 验证 Messages 兼容入口的 OAuth 提供商也执行 C -> U。
func TestGeminiMessagesCompatServiceForward_OAuthAppliesProviderModelMapping(t *testing.T) {
	upstreamBody := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]} ,"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2}}}` + "\n\n" +
		"data: [DONE]\n\n"
	httpStub := &geminiCompatHTTPUpstreamStub{response: &http.Response{
		StatusCode: http.StatusOK,

		Header: http.Header{"Content-Type": []string{"text/event-stream"}},

		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := newGeminiFixture(geminiDependencies{
		tokenProvider: newGeminiTokenSourceForTest(),

		httpUpstream: httpStub,

		cfg: &googleforward.Options{},
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           103,

			Platform: capability.PlatformGemini,

			Type: capability.ProviderTypeOAuth,

			Credentials: map[string]any{
				"access_token": "ya29.test-token",

				"project_id": "project-1",

				"model_mapping": map[string]any{
					"group-model": "oauth-upstream-model",
				},
			},

			Concurrency: 1,
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"group-model","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	result, err := svc.Forward(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, false), provider, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "group-model", result.Model)
	require.Equal(t, "oauth-upstream-model", result.UpstreamModel)
	require.NotNil(t, httpStub.lastReq)
	sentBody, err := io.ReadAll(httpStub.lastReq.Body)
	require.NoError(t, err)
	require.Equal(t, "oauth-upstream-model", gjson.GetBytes(sentBody, "model").String())
}

// TestGeminiMessagesCompatServiceForwardNative_OAuthAppliesProviderModelMapping 验证原生 Gemini 入口的 OAuth 提供商执行 C -> U。
func TestGeminiMessagesCompatServiceForwardNative_OAuthAppliesProviderModelMapping(t *testing.T) {
	upstreamBody := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2}}}` + "\n\n" +
		"data: [DONE]\n\n"
	httpStub := &geminiCompatHTTPUpstreamStub{response: &http.Response{
		StatusCode: http.StatusOK,

		Header: http.Header{"Content-Type": []string{"text/event-stream"}},

		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := newGeminiFixture(geminiDependencies{
		tokenProvider: newGeminiTokenSourceForTest(),

		httpUpstream: httpStub,

		cfg: &googleforward.Options{},
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           104,

			Platform: capability.PlatformGemini,

			Type: capability.ProviderTypeOAuth,

			Credentials: map[string]any{
				"access_token": "ya29.test-token",

				"project_id": "project-1",

				"model_mapping": map[string]any{
					"group-model": "oauth-upstream-model",
				},
			},

			Concurrency: 1,
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/group-model:generateContent", bytes.NewReader(body))

	result, err := svc.ForwardNative(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, false), provider, "group-model", "generateContent", false, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "group-model", result.Model)
	require.Equal(t, "oauth-upstream-model", result.UpstreamModel)
	require.NotNil(t, httpStub.lastReq)
	sentBody, err := io.ReadAll(httpStub.lastReq.Body)
	require.NoError(t, err)
	require.Equal(t, "oauth-upstream-model", gjson.GetBytes(sentBody, "model").String())
}

func TestGeminiMessagesCompatServiceForward_StreamingClosesToolUseBeforeText(t *testing.T) {
	upstreamBody := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"query":"weather"}}}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}}` + "\n\n" +
		`data: {"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":2}}` + "\n\n" +
		"data: [DONE]\n\n"
	httpStub := &geminiCompatHTTPUpstreamStub{
		response: &http.Response{
			StatusCode: http.StatusOK,

			Header: http.Header{"Content-Type": []string{"text/event-stream"}},

			Body: io.NopCloser(strings.NewReader(upstreamBody)),
		},
	}
	svc := newGeminiFixture(geminiDependencies{
		httpUpstream: httpStub,
		cfg:          &googleforward.Options{},
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           103,

			Platform: capability.PlatformGemini,

			Type: capability.ProviderTypeAPIKey,

			Credentials: map[string]any{
				"api_key": "gemini-api-key",
			},

			Concurrency: 1,
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gemini-2.5-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	result, err := svc.Forward(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, false), provider, body)
	require.NoError(t, err)
	require.NotNil(t, result)

	events := parseSSEEventsForTest(t, rec.Body.String())
	toolStart := findSSEEventForTest(events, "content_block_start", 0, "tool_use")
	toolStop := findSSEEventForTest(events, "content_block_stop", 0, "")
	textStart := findSSEEventForTest(events, "content_block_start", 1, "text")
	require.GreaterOrEqual(t, toolStart, 0, "应先开始 tool_use 块")
	require.Greater(t, toolStop, toolStart, "tool_use 块应被关闭")
	require.Greater(t, textStart, toolStop, "文本块必须在 tool_use 块关闭后开始")
}

func TestGeminiMessagesCompatServiceForward_PreservesRequestedModelAndMappedUpstreamModel(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	httpStub := &geminiCompatHTTPUpstreamStub{
		response: &http.Response{
			StatusCode: http.StatusOK,

			Header: http.Header{"x-request-id": []string{"gemini-req-1"}},

			Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`)),
		},
	}
	svc := newGeminiFixture(geminiDependencies{httpUpstream: httpStub, cfg: &googleforward.Options{}})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           1,

			Type: capability.ProviderTypeAPIKey,

			Credentials: map[string]any{
				"api_key": "test-key",
				"model_mapping": map[string]any{
					"claude-sonnet-4": "claude-sonnet-4-20250514",
				},
			},
		},
	}
	body := []byte(`{"model":"claude-sonnet-4","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)

	result, err := svc.Forward(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, false), provider, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "claude-sonnet-4", result.Model)
	require.Equal(t, "claude-sonnet-4-20250514", result.UpstreamModel)
	require.Equal(t, "hello", gjson.GetBytes(w.Body.Bytes(), "content.0.text").String())
	require.Equal(t, 1, httpStub.calls)
	require.NotNil(t, httpStub.lastReq)
	require.Contains(t, httpStub.lastReq.URL.String(), "/models/claude-sonnet-4-20250514:")
}

func TestGeminiMessagesCompatServiceForward_NormalizesWebSearchToolForAIStudio(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	httpStub := &geminiCompatHTTPUpstreamStub{
		response: &http.Response{
			StatusCode: http.StatusOK,

			Header: http.Header{"x-request-id": []string{"gemini-req-2"}},

			Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`)),
		},
	}
	svc := newGeminiFixture(geminiDependencies{httpUpstream: httpStub, cfg: &googleforward.Options{}})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           1,

			Type: capability.ProviderTypeAPIKey,

			Credentials: map[string]any{
				"api_key": "test-key",
			},
		},
	}
	body := []byte(`{"model":"claude-sonnet-4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"get_weather","description":"Get weather info","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"}]}`)

	result, err := svc.Forward(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, false), provider, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, httpStub.lastReq)

	postedBody, err := io.ReadAll(httpStub.lastReq.Body)
	require.NoError(t, err)

	var posted map[string]any
	require.NoError(t, json.Unmarshal(postedBody, &posted))
	tools, ok := posted["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 2)

	searchTool, ok := tools[1].(map[string]any)
	require.True(t, ok)
	_, hasSnake := searchTool["google_search"]
	_, hasCamel := searchTool["googleSearch"]
	require.True(t, hasSnake)
	require.False(t, hasCamel)
	_, hasFuncDecl := searchTool["functionDeclarations"]
	require.False(t, hasFuncDecl)
}

func TestConvertClaudeMessagesToGeminiGenerateContent_AddsThoughtSignatureForToolUse(t *testing.T) {
	claudeReq := map[string]any{
		"model": "claude-haiku-4-5-20251001",

		"max_tokens": 10,

		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "hi"},
				},
			},

			map[string]any{
				"role": "assistant",

				"content": []any{
					map[string]any{"type": "text", "text": "ok"},

					map[string]any{
						"type": "tool_use",

						"id": "toolu_123",

						"name": "default_api:write_file",

						"input": map[string]any{"path": "a.txt", "content": "x"},
						// no signature on purpose

					},
				},
			},
		},

		"tools": []any{
			map[string]any{
				"name": "default_api:write_file",

				"description": "write file",

				"input_schema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string"}},
				},
			},
		},
	}
	b, _ := json.Marshal(claudeReq)

	out, err := googleforward.ConvertClaudeForTest(b)
	if err != nil {
		t.Fatalf("convert failed: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "\"functionCall\"") {
		t.Fatalf("expected functionCall in output, got: %s", s)
	}
	if !strings.Contains(s, "\"thoughtSignature\":\""+"skip_thought_signature_validator"+"\"") {
		t.Fatalf("expected injected thoughtSignature %q, got: %s", "skip_thought_signature_validator", s)
	}
}

func TestEnsureGeminiFunctionCallThoughtSignatures_InsertsWhenMissing(t *testing.T) {
	geminiReq := map[string]any{
		"contents": []any{
			map[string]any{
				"role": "user",

				"parts": []any{
					map[string]any{
						"functionCall": map[string]any{
							"name": "default_api:write_file",
							"args": map[string]any{"path": "a.txt"},
						},
					},
				},
			},
		},
	}
	b, _ := json.Marshal(geminiReq)
	out := googleforward.EnsureSignatureForTest(b)
	s := string(out)
	if !strings.Contains(s, "\"thoughtSignature\":\""+"skip_thought_signature_validator"+"\"") {
		t.Fatalf("expected injected thoughtSignature %q, got: %s", "skip_thought_signature_validator", s)
	}
}

func TestEstimateGeminiCountTokens(t *testing.T) {
	zero := 0
	tests := []struct {
		name      string
		input     string
		wantGt0   bool // 期望结果 > 0
		wantExact *int // 如果非 nil，期望精确匹配
	}{
		{
			name: "含 systemInstruction 和 contents",

			input: `{
				"systemInstruction":{"parts":[{"text":"You are a helpful assistant."}]},
				"contents":[{"parts":[{"text":"Hello, how are you?"}]}]
			}`,

			wantGt0: true,
		},

		{
			name: "仅 contents，无 systemInstruction",

			input: `{
				"contents":[{"parts":[{"text":"Hello, how are you?"}]}]
			}`,

			wantGt0: true,
		},

		{
			name:      "空 parts",
			input:     `{"contents":[{"parts":[]}]}`,
			wantGt0:   false,
			wantExact: &zero,
		},

		{
			name: "非文本 parts（inlineData）",

			input: `{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png"}}]}]}`,

			wantGt0: false,

			wantExact: &zero,
		},

		{
			name:      "空白文本",
			input:     `{"contents":[{"parts":[{"text":"   "}]}]}`,
			wantGt0:   false,
			wantExact: &zero,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gemininative.EstimateGeminiCountTokens([]byte(tt.input))
			if tt.wantExact != nil {
				if got != *tt.wantExact {
					t.Errorf("期望精确值 %d，实际 %d", *tt.wantExact, got)
				}
				return
			}
			if tt.wantGt0 && got <= 0 {
				t.Errorf("期望返回 > 0，实际 %d", got)
			}
			if !tt.wantGt0 && got != 0 {
				t.Errorf("期望返回 0，实际 %d", got)
			}
		})
	}
}

func TestParseGeminiRateLimitResetTime(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantNil     bool
		approxDelta int64 // 预期的 (返回值 - now) 大约是多少秒
	}{
		{
			name: "正常 quotaResetDelay",

			input: `{"error":{"details":[{"metadata":{"quotaResetDelay":"12.345s"}}]}}`,

			wantNil: false,

			approxDelta: 13, // 向上取整 12.345 -> 13

		},

		{
			name: "daily quota",

			input: `{"error":{"message":"quota per day exceeded"}}`,

			wantNil: false,

			approxDelta: -1, // 不检查精确 delta，仅检查非 nil

		},

		{
			name:    "无 details 且无 regex 匹配",
			input:   `{"error":{"message":"rate limit"}}`,
			wantNil: true,
		},

		{
			name:        "regex 回退匹配",
			input:       `Please retry in 30s`,
			wantNil:     false,
			approxDelta: 30,
		},

		{
			name:    "完全无匹配",
			input:   `{"error":{"code":429}}`,
			wantNil: true,
		},

		{
			name: "非法 JSON 但 regex 回退仍工作",

			input: `not json but Please retry in 10s`,

			wantNil: false,

			approxDelta: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now().Unix()
			got := parseGeminiReset([]byte(tt.input))

			if tt.wantNil {
				if got != nil {
					t.Fatalf("期望返回 nil，实际返回 %d", *got)
				}
				return
			}

			if got == nil {
				t.Fatalf("期望返回非 nil，实际返回 nil")
			}

			// approxDelta == -1 时断言结果为非 nil，适用于 daily quota 等时间不确定的场景。
			if tt.approxDelta == -1 {
				// 仅验证返回的时间戳在合理范围内（未来的某个时间）
				if *got < now {
					t.Errorf("期望返回的时间戳 >= now(%d)，实际 %d", now, *got)
				}
				return
			}

			// 使用 +/-2 秒容差进行范围检查
			delta := *got - now
			if delta < tt.approxDelta-2 || delta > tt.approxDelta+2 {
				t.Errorf("期望 delta 约为 %d 秒（+/-2），实际 delta = %d 秒（返回值=%d, now=%d）",
					tt.approxDelta, delta, *got, now)
			}
		})
	}
}

func buildGeminiRateLimitBody(delay string) []byte {
	return []byte(fmt.Sprintf(`{"error":{"message":"too many requests","details":[{"metadata":{"quotaResetDelay":%q}}]}}`, delay))
}

func (r errorRulesFixtureRepo) List(context.Context) ([]*errorpolicy.ErrorPassthroughRule, error) {
	return r.rules, nil
}

func newErrorRulesTestService(rules []*errorpolicy.ErrorPassthroughRule) *errorpolicy.ErrorPassthroughService {
	s := errorpolicy.NewErrorPassthroughService(errorRulesFixtureRepo{rules: rules}, nil, gatewaytelemetry.ErrorRules)
	if err := s.StartContext(context.Background()); err != nil {
		panic(err)
	}
	return s
}

func newGeminiNativeTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", strings.NewReader("{}"))
	return c, rec
}

func (r *geminiErrorPolicyRepo) SetError(_ context.Context, _ int64, _ string) error {
	r.setErrorCalls++
	return nil
}

func (r *geminiErrorPolicyRepo) SetRateLimited(_ context.Context, _ int64, _ time.Time) error {
	r.setRateLimitedCalls++
	return nil
}

func (r *geminiErrorPolicyRepo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, _ string) error {
	r.setTempCalls++
	return nil
}

func (r *geminiErrorPolicyRepo) SetModelRateLimit(_ context.Context, _ int64, scope string, _ time.Time, _ ...string) error {
	r.setModelRateLimitedCalls++
	r.lastModelScope = scope
	return nil
}

func newNonFailoverPassthroughRule(statusCode int, keyword string, respCode int, customMessage string) *errorpolicy.ErrorPassthroughRule {
	return &errorpolicy.ErrorPassthroughRule{
		ID: 1,

		Name: "non-failover-rule",

		Enabled: true,

		Priority: 1,

		ErrorCodes: []int{statusCode},

		Keywords: []string{keyword},

		MatchMode: errorpolicy.MatchModeAll,

		PassthroughCode: false,

		ResponseCode: &respCode,

		PassthroughBody: false,

		CustomMessage: &customMessage,
	}
}

func parseSSEEventsForTest(t *testing.T, stream string) []map[string]any {
	t.Helper()
	chunks := strings.Split(stream, "\n\n")
	events := make([]map[string]any, 0, len(chunks))
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		eventName := ""
		dataLine := ""
		for line := range strings.SplitSeq(chunk, "\n") {
			if after, ok := strings.CutPrefix(line, "event:"); ok {
				eventName = strings.TrimSpace(after)
			}
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				dataLine = strings.TrimSpace(after)
			}
		}
		if dataLine == "" || dataLine == "[DONE]" {
			continue
		}
		var data map[string]any
		require.NoError(t, json.Unmarshal([]byte(dataLine), &data))
		data["_event"] = eventName
		events = append(events, data)
	}
	return events
}

func findSSEEventForTest(events []map[string]any, event string, index int, blockType string) int {
	for i, data := range events {
		if data["_event"] != event {
			continue
		}
		if gotIndex, ok := data["index"].(float64); ok && int(gotIndex) != index {
			continue
		}
		if blockType != "" {
			block, _ := data["content_block"].(map[string]any)
			if block["type"] != blockType {
				continue
			}
		}
		return i
	}
	return -1
}

func (r *rateLimit429ProviderRepoStub) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitCalls++
	r.lastRateLimitID = id
	r.lastRateLimitReset = resetAt
	return nil
}

func TestGeminiMessagesForwardsThinkingBudget(t *testing.T) {
	stub := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}`))}}
	svc := newGeminiFixture(geminiDependencies{httpUpstream: stub, cfg: &googleforward.Options{}})
	svc.Attributes = &routing.ModelAttributeService{Catalog: routing.ModelAttributeCatalog{View: testkit.OfflineView}}
	provider := &gatewayprovider.ExecutionProvider{Record: providercore.Record{ID: 1, Platform: "gemini", Type: "apikey", Credentials: map[string]any{"api_key": "test", "model_mapping": map[string]any{"alias": "gemini-2.5-flash"}}}}
	body := []byte(`{"model":"alias","max_tokens":32000,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":4096}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	_, err := svc.Forward(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, false), provider, body)
	require.NoError(t, err)
	wire, err := io.ReadAll(stub.lastReq.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"includeThoughts":true,"thinkingBudget":4096}`, gjson.GetBytes(wire, "generationConfig.thinkingConfig").Raw)
	require.Contains(t, recorder.Body.String(), "answer")
}
