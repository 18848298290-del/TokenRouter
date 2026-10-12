package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	providerpolicy "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	upstreamcore "github.com/TokenFlux/TokenRouter/internal/upstream"
)

type requestClientDetectorFixture struct {
	result providerpolicy.CodexClientRestrictionDetectionResult
}

// tlsProfileTestStore supplies fixed policy through the production lookup interface.
type tlsProfileTestStore struct {
	egress.TLSFingerprintProfileRepository
	profiles []*egress.TLSFingerprintProfile
}

func TestLogCodexCLIOnlyDetection_NilSafety(t *testing.T) {
	// Nil inputs must not panic; log contents are outside this assertion.
	require.NotPanics(t, func() {
		LogCodexCLIOnlyDetection(context.TODO(), nil, nil, 0, providerpolicy.CodexClientRestrictionDetectionResult{Enabled: true, Matched: false, Reason: "test"}, nil)
		LogCodexCLIOnlyDetection(context.Background(), nil, nil, 0, providerpolicy.CodexClientRestrictionDetectionResult{Enabled: false, Matched: false, Reason: "disabled"}, nil)
	})
}

func TestLogCodexCLIOnlyDetection_OnlyLogsRejected(t *testing.T) {
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	provider := &gatewayprovider.ExecutionProvider{Record: providerpolicy.Record{LoadLocation: time.LoadLocation, ID: 1001}}
	LogCodexCLIOnlyDetection(context.Background(), nil, provider, 2002, providerpolicy.CodexClientRestrictionDetectionResult{
		Enabled: true,
		Matched: true,
		Reason:  providerpolicy.CodexClientRestrictionReasonMatchedUA,
	}, nil)
	LogCodexCLIOnlyDetection(context.Background(), nil, provider, 2002, providerpolicy.CodexClientRestrictionDetectionResult{
		Enabled: true,
		Matched: false,
		Reason:  providerpolicy.CodexClientRestrictionReasonNotMatchedUA,
	}, nil)

	require.False(t, logSink.ContainsMessage("OpenAI codex_cli_only 允许官方客户端请求"))
	require.True(t, logSink.ContainsMessage("OpenAI codex_cli_only 拒绝非官方客户端请求"))
}

func TestLogCodexCLIOnlyDetection_RejectedIncludesRequestDetails(t *testing.T) {
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses?trace=1", bytes.NewReader(nil))
	c.Request.RemoteAddr = "172.18.0.1:54321"
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.98.0 (Windows 10.0.19045; x86_64) unknown")
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Real-IP", "203.0.113.42")
	c.Request.Header.Set("OpenAI-Beta", "assistants=v2")

	body := []byte(`{"model":"gpt-5.2","stream":false,"prompt_cache_key":"pc-123","access_token":"secret-token","input":[{"type":"text","text":"hello"}]}`)
	provider := &gatewayprovider.ExecutionProvider{Record: providerpolicy.Record{LoadLocation: time.LoadLocation, ID: 1001}}
	LogCodexCLIOnlyDetection(context.Background(), c, provider, 2002, providerpolicy.CodexClientRestrictionDetectionResult{
		Enabled: true,
		Matched: false,
		Reason:  providerpolicy.CodexClientRestrictionReasonNotMatchedUA,
	}, body)

	require.True(t, logSink.ContainsFieldValue("request_user_agent", "codex_cli_rs/0.98.0 (Windows 10.0.19045; x86_64) unknown"))
	require.True(t, logSink.ContainsFieldValue("request_model", "gpt-5.2"))
	require.True(t, logSink.ContainsFieldValue("request_query", "trace=1"))
	require.True(t, logSink.ContainsFieldValue("request_client_ip", "203.0.113.42"))
	require.True(t, logSink.ContainsFieldValue("request_remote_addr", "172.18.0.1:54321"))
	require.True(t, logSink.ContainsFieldValue("request_prompt_cache_key_sha256", upstreamcore.HashSensitiveValueForLog("pc-123")))
	require.True(t, logSink.ContainsFieldValue("request_headers", "openai-beta"))
	require.True(t, logSink.ContainsFieldValue("request_body_size", ""))
	require.False(t, logSink.ContainsFieldValue("request_body_preview", ""))
}

func TestOpenAIGatewayService_GetCodexClientRestrictionDetector(t *testing.T) {
	t.Run("使用注入的 detector", func(t *testing.T) {
		expected := &requestClientDetectorFixture{
			result: providerpolicy.CodexClientRestrictionDetectionResult{Enabled: true, Matched: true, Reason: "stub"},
		}
		svc := &OpenAIRequests{Detector: expected}

		got := svc.clientDetector()
		require.Same(t, expected, got)
	})

	t.Run("service 为 nil 时返回默认 detector", func(t *testing.T) {
		var svc *OpenAIRequests
		got := svc.clientDetector()
		require.NotNil(t, got)
	})

	t.Run("service 未注入 detector 时返回默认 detector", func(t *testing.T) {
		svc := &OpenAIRequests{Options: OpenAIRequestOptions{ForceCLI: true}}
		got := svc.clientDetector()
		require.NotNil(t, got)

		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.Header.Set("User-Agent", "curl/8.0")
		provider := &gatewayprovider.ExecutionProvider{Record: providerpolicy.Record{LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Extra: map[string]any{"codex_cli_only": true}}}

		result := got.DetectClient(func() (string, string) { return c.GetHeader("User-Agent"), c.GetHeader("originator") }, gatewayprovider.ExecutionRecord(provider), nil, false)
		require.True(t, result.Enabled)
		require.True(t, result.Matched)
		require.Equal(t, providerpolicy.CodexClientRestrictionReasonForceCodexCLI, result.Reason)
	})
}

func (s *requestClientDetectorFixture) DetectClient(read func() (string, string), record *providerpolicy.Record, allowed []string, matched bool) providerpolicy.CodexClientRestrictionDetectionResult {
	return s.result
}

func TestTLSFingerprintProfileService_ResolveTLSProfileOpenAI(t *testing.T) {
	svc := &egressadapter.TLSProfiles{}

	openAIOAuth := &gatewayprovider.ExecutionProvider{
		Record: providerpolicy.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type:  capability.ProviderTypeOAuth,
			Extra: map[string]any{"enable_tls_fingerprint": true},
		},
	}
	require.NotNil(t, svc.ResolveRequestTLS(gatewayprovider.ExecutionTLSSelection(openAIOAuth, nil)), "OpenAI OAuth 开启后应返回内置默认 profile")

	openAIAPIKey := &gatewayprovider.ExecutionProvider{
		Record: providerpolicy.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type:  capability.ProviderTypeAPIKey,
			Extra: map[string]any{"enable_tls_fingerprint": true},
		},
	}
	require.Nil(t, svc.ResolveRequestTLS(gatewayprovider.ExecutionTLSSelection(openAIAPIKey, nil)), "OpenAI API Key 不应启用 TLS 指纹伪装")
}

func TestTLSFingerprintProfileService_ResolveTLSProfileQoderCosy(t *testing.T) {
	svc := &egressadapter.TLSProfiles{}

	qoderCosy := &gatewayprovider.ExecutionProvider{
		Record: providerpolicy.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformQoder,
			Type:  capability.ProviderTypeCosy,
			Extra: map[string]any{"enable_tls_fingerprint": true},
		},
	}
	require.NotNil(t, svc.ResolveRequestTLS(gatewayprovider.ExecutionTLSSelection(qoderCosy, nil)), "Qoder COSY 开启后应返回内置默认 profile")

	qoderOtherType := &gatewayprovider.ExecutionProvider{
		Record: providerpolicy.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformQoder,
			Type:  capability.ProviderTypeOAuth,
			Extra: map[string]any{"enable_tls_fingerprint": true},
		},
	}
	require.Nil(t, svc.ResolveRequestTLS(gatewayprovider.ExecutionTLSSelection(qoderOtherType, nil)), "非 COSY Qoder 提供商不应启用 TLS 指纹伪装")
}

func TestOpenAIGatewayService_ResolveTLSProfileRouterFallback(t *testing.T) {
	provider := &gatewayprovider.ExecutionProvider{
		Record: providerpolicy.Record{
			LoadLocation: time.LoadLocation, Platform: capability.PlatformOpenAI,
			Type: capability.ProviderTypeOAuth,
			Extra: map[string]any{
				"enable_tls_fingerprint":     true,
				"tls_fingerprint_profile_id": int64(10),
			},
		},
	}
	profileSvc := egressadapter.NewTLSProfiles(egress.NewTLSFingerprintProfileService(&tlsProfileTestStore{profiles: []*egress.TLSFingerprintProfile{{ID: 10, Name: "fixed"}, {ID: 20, Name: "router"}}}, nil))
	profileSvc.Start()

	svc := newResponsesFixture(responsesFixtureInputs{profiles: profileSvc})

	// A matching router rule takes precedence over the provider profile.
	routerProfile := svc.Requests.TLSProfile(provider, egress.TLSFingerprintRouterMatchResult{
		Matched:                 true,
		TLSFingerprintProfileID: 20,
	})
	require.NotNil(t, routerProfile)
	require.Equal(t, "router", routerProfile.Name)

	// An unavailable rule profile falls back to the fixed provider profile.
	fallbackProfile := svc.Requests.TLSProfile(provider, egress.TLSFingerprintRouterMatchResult{
		Matched:                 true,
		TLSFingerprintProfileID: 404,
	})
	require.NotNil(t, fallbackProfile)
	require.Equal(t, "fixed", fallbackProfile.Name)
}

func (s *tlsProfileTestStore) List(context.Context) ([]*egress.TLSFingerprintProfile, error) {
	return s.profiles, nil
}
