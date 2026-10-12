package googleforward_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	forwardcore "github.com/TokenFlux/TokenRouter/internal/gateway/forward"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/googleforward"
	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// Compile-time interface check.
var _ gatewayprovider.ExecutionProviderStore = (*stubAntigravityProviderRepo)(nil)

type rateLimitCall struct {
	providerID int64
	resetAt    time.Time
}

type modelRateLimitCall struct {
	providerID int64
	modelKey   string // Stored key uses the canonical model ID, such as claude-sonnet-4-5.
	resetAt    time.Time
}

type extraUpdateCall struct {
	providerID int64
	updates    map[string]any
}

type stubAntigravityProviderRepo struct {
	gatewayprovider.ExecutionProviderStore

	rateCalls           []rateLimitCall
	modelRateLimitCalls []modelRateLimitCall
	extraUpdateCalls    []extraUpdateCall
}

// stubSmartRetryCache records sticky-binding deletion during retry tests.
// Only DeleteSessionProviderID is implemented.
type stubSmartRetryCache struct {
	session.GatewayCache // Unexpected calls to embedded methods panic.
	deleteCalls          []deleteSessionCall
}

type deleteSessionCall struct {
	groupID     int64
	sessionHash string
}

func TestAntigravityGatewayService_ForwardGemini_UsesConfiguredProjectFallback(t *testing.T) {
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)

	body, err := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": "hello"}}},
		},
	})
	require.NoError(t, err)
	c.Request = httptest.NewRequest(http.MethodPost, "/antigravity/v1beta/models/gemini-2.5-flash:streamGenerateContent", bytes.NewReader(body))

	upstreamBody := []byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1}}}\n\n")
	upstream := &queuedHTTPUpstreamStub{
		responses: []*http.Response{
			{
				StatusCode: http.StatusOK,

				Header: http.Header{"Content-Type": []string{"text/event-stream"}},

				Body: io.NopCloser(bytes.NewReader(upstreamBody)),
			},
		},
	}
	svc := newAntigravityFixture(antigravityDependencies{
		settingService: newExecutionReadersFixture(&antigravitySettingRepoStub{}), options: fixtureOptions(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024}),

		tokenProvider: newAntigravityTokenSourceForTest(nil),

		httpUpstream: upstream,
	})

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           101,

			Name: "acc-configured-project",

			Platform: capability.PlatformAntigravity,

			Type: capability.ProviderTypeOAuth,

			Status: billing.StatusActive,

			Concurrency: 1,

			Credentials: map[string]any{
				"access_token": "token",

				"antigravity_project_id": "configured-project",

				"model_mapping": map[string]any{
					"gemini-2.5-flash": "gemini-2.5-flash",
				},
			},
		},
	}

	result, err := svc.ForwardGemini(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, "gemini-2.5-flash", "streamGenerateContent", true, body, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requestBodies, 1)

	var wrapped map[string]any
	require.NoError(t, json.Unmarshal(upstream.requestBodies[0], &wrapped))
	require.Equal(t, "configured-project", wrapped["project"])
}

func TestAntigravityGatewayService_ForwardGemini_ImageUsesDefaultMappingAndOAuth(t *testing.T) {
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"}]}],"generationConfig":{"responseModalities":["TEXT","IMAGE"],"imageConfig":{"aspectRatio":"1:1"}}}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.1-flash-image:generateContent", bytes.NewReader(body))

	upstream := &queuedHTTPUpstreamStub{
		responses: []*http.Response{{
			StatusCode: http.StatusOK,

			Header: http.Header{"Content-Type": []string{"text/event-stream"}},

			Body: io.NopCloser(strings.NewReader(
				"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"aGVsbG8=\"}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1}}}\n\n",
			)),
		}},

		onCall: func(req *http.Request, _ *queuedHTTPUpstreamStub) {
			require.Equal(t, "Bearer test-access-token", req.Header.Get("Authorization"))
			require.Equal(t, "application/json", req.Header.Get("Content-Type"))
			require.Contains(t, req.URL.String(), "/v1internal:streamGenerateContent?alt=sse")
		},
	}
	svc := newAntigravityFixture(antigravityDependencies{
		settingService: newExecutionReadersFixture(&antigravitySettingRepoStub{}), options: fixtureOptions(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024}),

		tokenProvider: newAntigravityTokenSourceForTest(nil),

		httpUpstream: upstream,
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           104,

			Name: "antigravity-image",

			Platform: capability.PlatformAntigravity,

			Type: capability.ProviderTypeOAuth,

			Status: billing.StatusActive,

			Concurrency: 1,

			Credentials: map[string]any{
				"access_token": "test-access-token",
				"project_id":   "test-project",
			},
		},
	}

	result, err := svc.ForwardGemini(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, "gemini-3.1-flash-image", "generateContent", true, body, false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gemini-3.1-flash-image", result.Model)
	require.Equal(t, "gemini-3.1-flash-image", result.UpstreamModel)
	require.Equal(t, 1, result.ImageCount)
	require.Len(t, upstream.requestBodies, 1)

	var wrapped map[string]any
	require.NoError(t, json.Unmarshal(upstream.requestBodies[0], &wrapped))
	require.Equal(t, "test-project", wrapped["project"])
	require.Equal(t, "gemini-3.1-flash-image", wrapped["model"])
	request, ok := wrapped["request"].(map[string]any)
	require.True(t, ok)
	generationConfig, ok := request["generationConfig"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []any{"TEXT", "IMAGE"}, generationConfig["responseModalities"])
}

func TestAntigravityGatewayService_ForwardGemini_PreservesServerSideToolInvocationConfig(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"tools":[{"functionDeclarations":[{"name":"get_weather","parameters":{"type":"object","additionalProperties":false}}]},{"googleSearch":{}}],"toolConfig":{"includeServerSideToolInvocations":true}}`)
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	body = bytes.ReplaceAll(body, []byte{92}, nil)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewReader(body))

	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
		StatusCode: http.StatusOK,

		Header: http.Header{"Content-Type": []string{"text/event-stream"}},

		Body: io.NopCloser(strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{}}}\n\n")),
	}}}
	svc := newAntigravityFixture(antigravityDependencies{
		settingService: newExecutionReadersFixture(&antigravitySettingRepoStub{}), options: fixtureOptions(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024}),

		tokenProvider: newAntigravityTokenSourceForTest(nil),

		httpUpstream: upstream,
	})
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           103,
			Name:         "native-gemini",
			Platform:     capability.PlatformAntigravity,
			Type:         capability.ProviderTypeOAuth,
			Status:       billing.StatusActive,
			Concurrency:  1,

			Credentials: map[string]any{
				"access_token":  "token",
				"project_id":    "project-103",
				"model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"},
			},
		},
	}

	result, err := svc.ForwardGemini(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, "gemini-2.5-flash", "generateContent", false, body, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requestBodies, 1)

	var wrapped map[string]any
	require.NoError(t, json.Unmarshal(upstream.requestBodies[0], &wrapped))
	request, ok := wrapped["request"].(map[string]any)
	require.True(t, ok)
	toolConfig, ok := request["toolConfig"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, toolConfig["includeServerSideToolInvocations"])
	require.NotContains(t, toolConfig, "include_server_side_tool_invocations")
}

func TestAntigravityGatewayService_ForwardGemini_MissingProjectReturnsLocalError(t *testing.T) {
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)

	body, err := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": "hello"}}},
		},
	})
	require.NoError(t, err)
	c.Request = httptest.NewRequest(http.MethodPost, "/antigravity/v1beta/models/gemini-2.5-flash:streamGenerateContent", bytes.NewReader(body))

	upstream := &queuedHTTPUpstreamStub{}
	svc := newAntigravityFixture(antigravityDependencies{
		tokenProvider: newAntigravityTokenSourceForTest(nil),
		httpUpstream:  upstream,
	})

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           102,

			Name: "acc-missing-project",

			Platform: capability.PlatformAntigravity,

			Type: capability.ProviderTypeOAuth,

			Status: billing.StatusActive,

			Concurrency: 1,

			Credentials: map[string]any{
				"access_token": "token",
				"model_mapping": map[string]any{
					"gemini-2.5-flash": "gemini-2.5-flash",
				},
			},
		},
	}

	result, err := svc.ForwardGemini(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, "gemini-2.5-flash", "streamGenerateContent", true, body, false)
	require.Nil(t, result)
	require.ErrorIs(t, err, antigravity.ErrProjectIDRequired)
	require.Equal(t, http.StatusBadRequest, writer.Code)
	require.Empty(t, upstream.requestBodies)
	require.Contains(t, writer.Body.String(), "project_id")
	require.NotContains(t, writer.Body.String(), `"project":""`)
}

// TestAntigravityGatewayService_ForwardGemini_ModelRateLimitTriggersFailover
// checks conversion of provider-switch signals into UpstreamFailoverError.
func TestAntigravityGatewayService_ForwardGemini_ModelRateLimitTriggersFailover(t *testing.T) {
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)

	body, err := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": "hi"}}},
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewReader(body))
	c.Request = req

	// The rate-limit precheck requests a switch before upstream transport.
	svc := newAntigravityFixture(antigravityDependencies{
		tokenProvider: newAntigravityTokenSourceForTest(nil),

		httpUpstream: &httpUpstreamStub{resp: nil, err: nil},
	})

	// A 30-second cooldown exceeds the seven-second retry threshold.
	futureResetAt := time.Now().Add(30 * time.Second).Format(time.RFC3339)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           2,

			Name: "acc-gemini-rate-limited",

			Platform: capability.PlatformAntigravity,

			Type: capability.ProviderTypeOAuth,

			Status: billing.StatusActive,

			Concurrency: 1,

			Credentials: map[string]any{
				"access_token": "token",
				"project_id":   "proj",
			},

			Extra: map[string]any{
				"model_rate_limits": map[string]any{
					"gemini-2.5-flash": map[string]any{
						"rate_limit_reset_at": futureResetAt,
					},
				},
			},
		},
	}

	result, err := svc.ForwardGemini(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, "gemini-2.5-flash", "generateContent", false, body, false)
	require.Nil(t, result, "ForwardGemini should not return result when model rate limited")
	require.NotNil(t, err, "ForwardGemini should return error")

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr, "error should be UpstreamFailoverError to trigger provider switch")
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
	// A non-sticky switch does not force cache billing.
	require.False(t, failoverErr.ForceCacheBilling, "ForceCacheBilling should be false for non-sticky session")
}

// TestAntigravityGatewayService_ForwardGemini_StickySessionForceCacheBilling verifies
// that ForwardGemini sets ForceCacheBilling=true for sticky session switch.
func TestAntigravityGatewayService_ForwardGemini_StickySessionForceCacheBilling(t *testing.T) {
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)

	body, err := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": "hi"}}},
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewReader(body))
	c.Request = req

	svc := newAntigravityFixture(antigravityDependencies{
		tokenProvider: newAntigravityTokenSourceForTest(nil),

		httpUpstream: &httpUpstreamStub{resp: nil, err: nil},
	})

	// A 30-second cooldown exceeds the seven-second retry threshold.
	futureResetAt := time.Now().Add(30 * time.Second).Format(time.RFC3339)
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           4,

			Name: "acc-gemini-sticky-rate-limited",

			Platform: capability.PlatformAntigravity,

			Type: capability.ProviderTypeOAuth,

			Status: billing.StatusActive,

			Concurrency: 1,

			Credentials: map[string]any{
				"access_token": "token",
				"project_id":   "proj",
			},

			Extra: map[string]any{
				"model_rate_limits": map[string]any{
					"gemini-2.5-flash": map[string]any{
						"rate_limit_reset_at": futureResetAt,
					},
				},
			},
		},
	}

	// Mark this request as sticky.
	result, err := svc.ForwardGemini(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, "gemini-2.5-flash", "generateContent", false, body, true)
	require.Nil(t, result, "ForwardGemini should not return result when model rate limited")
	require.NotNil(t, err, "ForwardGemini should return error")

	// A sticky-provider switch forces cache billing.
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr, "error should be UpstreamFailoverError to trigger provider switch")
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
	require.True(t, failoverErr.ForceCacheBilling, "ForceCacheBilling should be true for sticky session switch")
}

func TestAntigravityGatewayService_ForwardGemini_ClearsStickySessionOnGeminiRateLimit(t *testing.T) {
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)

	body, err := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": "hi"}}},
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-flash:generateContent", bytes.NewReader(body))
	c.Request = req

	respBody := []byte(`{
		"error": {
			"status": "RESOURCE_EXHAUSTED",
			"details": [
				{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "gemini-3-flash"}, "reason": "RATE_LIMIT_EXCEEDED"},
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "15s"}
			]
		}
	}`)
	upstream := &httpUpstreamStub{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,

		Header: http.Header{},

		Body: io.NopCloser(bytes.NewReader(respBody)),
	}}
	repo := &stubAntigravityProviderRepo{}
	cache := &stubSmartRetryCache{}
	svc := newAntigravityFixture(antigravityDependencies{
		tokenProvider: newAntigravityTokenSourceForTest(nil),

		httpUpstream: upstream,

		providerRepo: repo,

		cache: cache,
	})

	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           44,

			Name: "acc-gemini-runtime-rate-limited",

			Platform: capability.PlatformAntigravity,

			Type: capability.ProviderTypeOAuth,

			Status: billing.StatusActive,

			Schedulable: true,

			Concurrency: 1,

			Credentials: map[string]any{
				"access_token": "token",

				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),

				"project_id": "proj",
			},

			Extra: map[string]any{
				"mixed_scheduling": true,
			},
		},
	}

	result, err := svc.ForwardGemini(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, "gemini-3-flash", "generateContent", false, body, true, forwardcore.WithGeminiSession(77, "gemini:sticky-runtime"))

	require.Nil(t, result)
	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
	require.Len(t, repo.modelRateLimitCalls, 2)
	require.Equal(t, "gemini-3-flash", repo.modelRateLimitCalls[0].modelKey)
	require.Equal(t, "antigravity:gemini", repo.modelRateLimitCalls[1].modelKey)
	require.Len(t, cache.deleteCalls, 1)
	require.Equal(t, int64(77), cache.deleteCalls[0].groupID)
	require.Equal(t, "gemini:sticky-runtime", cache.deleteCalls[0].sessionHash)
}

// TestAntigravityGatewayService_ForwardGemini_BillsWithMappedModel
// checks that Gemini forwarding returns the mapped billing model.
func TestAntigravityGatewayService_ForwardGemini_BillsWithMappedModel(t *testing.T) {
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)

	body, err := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": "hello"}}},
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewReader(body))
	c.Request = req

	upstreamBody := []byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":3}}}\n\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,

		Header: http.Header{"X-Request-Id": []string{"req-bill-2"}},

		Body: io.NopCloser(bytes.NewReader(upstreamBody)),
	}

	svc := newAntigravityFixture(antigravityDependencies{
		settingService: newExecutionReadersFixture(&antigravitySettingRepoStub{}), options: fixtureOptions(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024}),

		tokenProvider: newAntigravityTokenSourceForTest(nil),

		httpUpstream: &httpUpstreamStub{resp: resp},
	})

	const mappedModel = "gemini-3-pro-high"
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           6,

			Name: "acc-gemini-billing",

			Platform: capability.PlatformAntigravity,

			Type: capability.ProviderTypeOAuth,

			Status: billing.StatusActive,

			Concurrency: 1,

			Credentials: map[string]any{
				"access_token": "token",

				"project_id": "proj",

				"model_mapping": map[string]any{
					"gemini-2.5-flash": mappedModel,
				},
			},
		},
	}

	result, err := svc.ForwardGemini(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, "gemini-2.5-flash", "generateContent", true, body, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gemini-2.5-flash", result.Model)
	require.Equal(t, mappedModel, result.UpstreamModel)
}

func TestAntigravityGatewayService_ForwardGemini_RetriesCorruptedThoughtSignature(t *testing.T) {
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)

	body, err := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": "hello"}}},

			{"role": "model", "parts": []map[string]any{{"text": "thinking", "thought": true, "thoughtSignature": "sig_bad_1"}}},

			{
				"role":  "model",
				"parts": []map[string]any{{"functionCall": map[string]any{"name": "toolA", "args": map[string]any{"x": 1}}, "thoughtSignature": "sig_bad_2"}},
			},
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/antigravity/v1beta/models/gemini-3.1-pro-preview:streamGenerateContent", bytes.NewReader(body))
	c.Request = req

	firstRespBody := []byte(`{"response":{"error":{"code":400,"message":"Corrupted thought signature.","status":"INVALID_ARGUMENT"}}}`)
	secondRespBody := []byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":3}}}\n\n")

	upstream := &queuedHTTPUpstreamStub{
		responses: []*http.Response{
			{
				StatusCode: http.StatusBadRequest,

				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"X-Request-Id": []string{"req-sig-1"},
				},

				Body: io.NopCloser(bytes.NewReader(firstRespBody)),
			},

			{
				StatusCode: http.StatusOK,

				Header: http.Header{
					"Content-Type": []string{"text/event-stream"},
					"X-Request-Id": []string{"req-sig-2"},
				},

				Body: io.NopCloser(bytes.NewReader(secondRespBody)),
			},
		},
	}

	svc := newAntigravityFixture(antigravityDependencies{
		settingService: newExecutionReadersFixture(&antigravitySettingRepoStub{}), options: fixtureOptions(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024}),

		tokenProvider: newAntigravityTokenSourceForTest(nil),

		httpUpstream: upstream,
	})

	const originalModel = "gemini-3.1-pro-preview"
	const mappedModel = "gemini-3.1-pro-high"
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           7,

			Name: "acc-gemini-signature",

			Platform: capability.PlatformAntigravity,

			Type: capability.ProviderTypeOAuth,

			Status: billing.StatusActive,

			Concurrency: 1,

			Credentials: map[string]any{
				"access_token": "token",

				"project_id": "proj",

				"model_mapping": map[string]any{
					originalModel: mappedModel,
				},
			},
		},
	}

	result, err := svc.ForwardGemini(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, originalModel, "streamGenerateContent", true, body, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, originalModel, result.Model)
	require.Equal(t, mappedModel, result.UpstreamModel)
	require.Len(t, upstream.requestBodies, 2, "signature error should trigger exactly one retry")

	firstReq := string(upstream.requestBodies[0])
	secondReq := string(upstream.requestBodies[1])
	require.Contains(t, firstReq, `"thoughtSignature":"sig_bad_1"`)
	require.Contains(t, firstReq, `"thoughtSignature":"sig_bad_2"`)
	require.Contains(t, secondReq, `"thoughtSignature":"skip_thought_signature_validator"`)
	require.NotContains(t, secondReq, `"thoughtSignature":"sig_bad_1"`)
	require.NotContains(t, secondReq, `"thoughtSignature":"sig_bad_2"`)

	raw, ok := c.Get(gatewayhttp.OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*ops.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.NotEmpty(t, events)
	require.Equal(t, "signature_error", events[0].Kind)
}

func TestAntigravityGatewayService_ForwardGemini_SignatureRetryPropagatesFailover(t *testing.T) {
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)

	body, err := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": "hello"}}},

			{"role": "model", "parts": []map[string]any{{"text": "thinking", "thought": true, "thoughtSignature": "sig_bad_1"}}},
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/antigravity/v1beta/models/gemini-3.1-pro-preview:streamGenerateContent", bytes.NewReader(body))
	c.Request = req

	firstRespBody := []byte(`{"response":{"error":{"code":400,"message":"Corrupted thought signature.","status":"INVALID_ARGUMENT"}}}`)

	const originalModel = "gemini-3.1-pro-preview"
	const mappedModel = "gemini-3.1-pro-high"
	provider := &gatewayprovider.ExecutionProvider{
		Record: providercore.Record{
			LoadLocation: time.LoadLocation,
			ID:           8,

			Name: "acc-gemini-signature-failover",

			Platform: capability.PlatformAntigravity,

			Type: capability.ProviderTypeOAuth,

			Status: billing.StatusActive,

			Concurrency: 1,

			Credentials: map[string]any{
				"access_token": "token",

				"project_id": "proj",

				"model_mapping": map[string]any{
					originalModel: mappedModel,
				},
			},
		},
	}

	upstream := &queuedHTTPUpstreamStub{
		responses: []*http.Response{
			{
				StatusCode: http.StatusBadRequest,

				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"X-Request-Id": []string{"req-sig-failover-1"},
				},

				Body: io.NopCloser(bytes.NewReader(firstRespBody)),
			},
		},

		onCall: func(_ *http.Request, stub *queuedHTTPUpstreamStub) {
			if stub.callCount != 1 {
				return
			}
			futureResetAt := time.Now().Add(30 * time.Second).Format(time.RFC3339)
			provider.Record.Extra = map[string]any{
				"model_rate_limits": map[string]any{
					mappedModel: map[string]any{
						"rate_limit_reset_at": futureResetAt,
					},
				},
			}
		},
	}

	svc := newAntigravityFixture(antigravityDependencies{
		settingService: newExecutionReadersFixture(&antigravitySettingRepoStub{}), options: fixtureOptions(&googleforward.Options{MaxLineSize: 500 * 1024 * 1024}),

		tokenProvider: newAntigravityTokenSourceForTest(nil),

		httpUpstream: upstream,
	})

	result, err := svc.ForwardGemini(context.Background(), gatewayhttp.NewGoogleBoundary(c, svc.Options, true), provider, originalModel, "streamGenerateContent", true, body, true)
	require.Nil(t, result)

	var failoverErr *forwardcore.UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr, "signature retry should propagate failover instead of falling back to the original 400")
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
	require.True(t, failoverErr.ForceCacheBilling)
	require.Len(t, upstream.requestBodies, 1, "retry should stop at preflight failover and not issue a second upstream request")

	raw, ok := c.Get(gatewayhttp.OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*ops.OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 2)
	require.Equal(t, "signature_error", events[0].Kind)
	require.Equal(t, "failover", events[1].Kind)
}

func (s *stubAntigravityProviderRepo) SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error {
	s.rateCalls = append(s.rateCalls, rateLimitCall{providerID: id, resetAt: resetAt})
	return nil
}

func (s *stubAntigravityProviderRepo) SetModelRateLimit(ctx context.Context, id int64, modelKey string, resetAt time.Time, reason ...string) error {
	s.modelRateLimitCalls = append(s.modelRateLimitCalls, modelRateLimitCall{providerID: id, modelKey: modelKey, resetAt: resetAt})
	return nil
}

func (s *stubAntigravityProviderRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	s.extraUpdateCalls = append(s.extraUpdateCalls, extraUpdateCall{providerID: id, updates: updates})
	return nil
}

func (c *stubSmartRetryCache) DeleteSessionProviderID(_ context.Context, groupID int64, sessionHash string) error {
	c.deleteCalls = append(c.deleteCalls, deleteSessionCall{groupID: groupID, sessionHash: sessionHash})
	return nil
}
