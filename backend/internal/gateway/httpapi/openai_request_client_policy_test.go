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

// OAuth rejection diagnostics record filtered request metadata and distinguish
// policy evaluation from TLS selection. The fixtures also exercise injected and
// default client detectors, platform eligibility for built-in TLS profiles, and
// router-profile precedence with a configured provider profile.

// requestClientDetectorFixture returns a fixed client-policy decision for
// injection assertions.
type requestClientDetectorFixture struct {
	result providerpolicy.CodexClientRestrictionDetectionResult // injected client-policy decision
}

// tlsProfileTestStore supplies configured profile records to the production TLS
// reader.
type tlsProfileTestStore struct {
	egress.TLSFingerprintProfileRepository                                 // unused embedded profile-repository methods
	profiles                               []*egress.TLSFingerprintProfile // configured TLS profile records
}

// TestLogCodexCLIOnlyDetection_NilSafety supplies enabled and disabled
// decisions with missing HTTP and provider inputs. It checks that diagnostic
// calls finish without a panic, leaving log contents outside this assertion.
func TestLogCodexCLIOnlyDetection_NilSafety(t *testing.T) {
	// Nil inputs must not panic; log contents are outside this assertion.
	require.NotPanics(t, func() {
		LogCodexCLIOnlyDetection(context.TODO(), nil, nil, 0, providerpolicy.CodexClientRestrictionDetectionResult{Enabled: true, Matched: false, Reason: "test"}, nil)
		LogCodexCLIOnlyDetection(context.Background(), nil, nil, 0, providerpolicy.CodexClientRestrictionDetectionResult{Enabled: false, Matched: false, Reason: "disabled"}, nil)
	})
}

// TestLogCodexCLIOnlyDetection_LogsDecisionLevels records matched and rejected
// client decisions. It checks that acceptance is logged at info level and
// rejection at warning level using the corresponding English diagnostic messages.
func TestLogCodexCLIOnlyDetection_LogsDecisionLevels(t *testing.T) {
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

	require.True(t, logSink.ContainsMessageAtLevel("OpenAI codex_cli_only allowed request", "info"))
	require.True(t, logSink.ContainsMessageAtLevel("OpenAI codex_cli_only rejected unofficial client request", "warn"))
}

// TestLogCodexCLIOnlyDetection_RejectedIncludesRequestDetails rejects a request
// containing client headers and a prompt-cache key. It checks selected
// metadata, hashed cache identity, header names, and absence of a body preview.
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

// TestOpenAIGatewayService_GetCodexClientRestrictionDetector compares injected,
// missing-service, and ForceCLI defaults. It checks injected pointer identity
// and the default detector's enabled, matched emulation decision for a
// nonofficial UA.
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

// DetectClient returns the fixed policy result for each fixture evaluation.
func (s *requestClientDetectorFixture) DetectClient(read func() (string, string), record *providerpolicy.Record, allowed []string, matched bool) providerpolicy.CodexClientRestrictionDetectionResult {
	return s.result
}

// TestTLSFingerprintProfileService_ResolveTLSProfileOpenAI enables
// fingerprinting for OAuth and API-key OpenAI providers. It checks the default
// profile exists for OAuth and remains unavailable for the API-key fixture.
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

// TestTLSFingerprintProfileService_ResolveTLSProfileQoderCosy enables
// fingerprinting on two Qoder credential types. It checks the default profile
// for COSY and nil for the OAuth fixture under the same enable flag.
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

// TestOpenAIGatewayService_ResolveTLSProfileRouterFallback supplies fixed and
// router TLS profiles through production lookup. It checks the router profile
// wins when available and the fixed profile is used for a missing
// router-profile ID.
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

	// Select the available router profile to exercise precedence over the
	// fixed provider profile supplied through the same production profile
	// lookup path.
	routerProfile := svc.Requests.TLSProfile(provider, egress.TLSFingerprintRouterMatchResult{
		Matched:                 true,
		TLSFingerprintProfileID: 20,
	})
	require.NotNil(t, routerProfile)
	require.Equal(t, "router", routerProfile.Name)

	// Request an absent router-profile ID so the production reader recovers
	// with the configured provider profile, keeping a usable selection for
	// this fixture.
	fallbackProfile := svc.Requests.TLSProfile(provider, egress.TLSFingerprintRouterMatchResult{
		Matched:                 true,
		TLSFingerprintProfileID: 404,
	})
	require.NotNil(t, fallbackProfile)
	require.Equal(t, "fixed", fallbackProfile.Name)
}

// List returns the fixture's configured profile slice for TLS cache
// initialization. The caller receives the stored slice and pointers directly.
// This fixture performs no copy or error injection for profile-list reads.
func (s *tlsProfileTestStore) List(context.Context) ([]*egress.TLSFingerprintProfile, error) {
	return s.profiles, nil
}
