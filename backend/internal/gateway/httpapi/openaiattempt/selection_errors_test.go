package openaiattempt

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// A group lacking configured model support needs a different client error from
// an eligible group whose providers are unavailable. The fake diagnoser records
// routing and display identities, while tests check missing-input defaults,
// model-not-found attribution, and precedence over rate-limit evidence from
// selection.

// fakeDiagnoser records availability lookups and returns configured
// model-support evidence.
type fakeDiagnoser struct {
	calls []fakeDiagnoseCall                 // recorded availability lookup filters
	resp  routing.ModelAvailabilityDiagnosis // configured pool-support evidence
}

// fakeDiagnoseCall retains group and model filters passed to availability
// diagnosis.
type fakeDiagnoseCall struct {
	// GroupID retains the supplied group pointer in recorded calls. Tests
	// keep its value stable until assertions read the effective group
	// identifier.
	GroupID  *int64
	Model    string // queried provider-routing model name
	Platform string // optional platform lookup filter
}

// DiagnoseModelAvailabilityForPlatform records the requested filters and
// returns fixture availability. Recorded calls retain the supplied group
// pointer, so callers keep its value stable until reading the captured lookup
// arguments.
func (f *fakeDiagnoser) DiagnoseModelAvailabilityForPlatform(
	_ context.Context,
	groupID *int64,
	model, platform string,
) routing.ModelAvailabilityDiagnosis {
	f.calls = append(f.calls, fakeDiagnoseCall{
		GroupID:  groupID,
		Model:    model,
		Platform: platform,
	})
	return f.resp
}

// ptrInt64 allocates an addressable group identifier for selection-error
// fixtures.
func ptrInt64(v int64) *int64 { return &v }

// newTestGinContextWithRequest creates an HTTP context usable by request-scoped
// classification.
func newTestGinContextWithRequest() *gin.Context {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
	return c
}

// TestClassifyNoProviderError_NilDiagnoser_Falls503 omits model-support
// diagnosis for a grouped key. It checks the client response stays 503
// api_error and does not gain model-not-found attribution from absent evidence.
func TestClassifyNoProviderError_NilDiagnoser_Falls503(t *testing.T) {
	c := newTestGinContextWithRequest()
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := ClassifyNoProviderErrorFromGin(c, nil, apiKey, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.Equal(t, "api_error", cls.ErrType)
	require.False(t, cls.ModelNotFound)
}

// TestClassifySelectionFailureError_RateLimitedPool supplies cooling-model
// counts in scheduler error text. It checks positive counts yield 429
// rate_limit_error, while zero or absent cooling evidence retains the supplied
// fallback response.
func TestClassifySelectionFailureError_RateLimitedPool(t *testing.T) {
	fallback := noProviderErrorClassification{Status: http.StatusServiceUnavailable, ErrType: "api_error", Message: "Service temporarily unavailable"}

	got := classifySelectionFailureError(
		fmt.Errorf("no available providers supporting model: gpt-5.6-sol (total=3 eligible=0 model_rate_limited=3)"),
		fallback,
	)

	require.Equal(t, http.StatusTooManyRequests, got.Status)
	require.Equal(t, "rate_limit_error", got.ErrType)
	require.Contains(t, got.Message, "rate-limited")
	require.Equal(t, fallback, classifySelectionFailureError(fmt.Errorf("model_rate_limited=0"), fallback))
	require.Equal(t, fallback, classifySelectionFailureError(fmt.Errorf("no available providers"), fallback))
}

// TestClassifyNoProviderError_NilAPIKey_Falls503 supplies a diagnoser but no
// effective key. It checks a 503 availability response, false model
// attribution, and no lookup using an unknown request group.
func TestClassifyNoProviderError_NilAPIKey_Falls503(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}

	cls := ClassifyNoProviderErrorFromGin(c, fd, nil, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.False(t, cls.ModelNotFound)
	require.Empty(t, fd.calls, "diagnoser must not be consulted when apiKey missing")
}

// TestClassifyNoProviderError_NilGroupID_Falls503 supplies a key without group
// identity. It checks diagnosis is skipped and the fallback remains a 503
// response without model-not-found attribution for that unbound request.
func TestClassifyNoProviderError_NilGroupID_Falls503(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}
	apiKey := &apikey.APIKey{GroupID: nil}

	cls := ClassifyNoProviderErrorFromGin(c, fd, apiKey, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.False(t, cls.ModelNotFound)
	require.Empty(t, fd.calls, "diagnoser must not be consulted when group not bound")
}

// TestClassifyNoProviderError_EmptyModel_Falls503 supplies whitespace routing
// input for a grouped key. It checks no availability lookup occurs and the
// response retains service-unavailable status with false model attribution.
func TestClassifyNoProviderError_EmptyModel_Falls503(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := ClassifyNoProviderErrorFromGin(c, fd, apiKey, "   ", "", capability.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.False(t, cls.ModelNotFound)
	require.Empty(t, fd.calls)
}

// TestClassifyNoProviderError_ModelNotSupported_Returns404 supplies a
// configured pool with no support for the routed model. It checks lookup
// filters, client 404 text, and the local model-configuration mark published to
// Gin.
func TestClassifyNoProviderError_ModelNotSupported_Returns404(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(42)}

	cls := ClassifyNoProviderErrorFromGin(c, fd, apiKey, "gpt-5.1-codex-mini", "gpt-5.1-codex-mini", capability.PlatformOpenAI)

	require.Equal(t, http.StatusNotFound, cls.Status)
	require.Equal(t, "model_not_found", cls.ErrType)
	require.True(t, cls.ModelNotFound)
	require.Contains(t, cls.Message, "gpt-5.1-codex-mini", "message must surface the requested model")

	require.Len(t, fd.calls, 1)
	require.Equal(t, "gpt-5.1-codex-mini", fd.calls[0].Model)
	require.Equal(t, capability.PlatformOpenAI, fd.calls[0].Platform)
	require.NotNil(t, fd.calls[0].GroupID)
	require.Equal(t, int64(42), *fd.calls[0].GroupID)
	require.True(t, gatewayhttp.HasOpsClientBusinessLimited(c))
	require.Equal(t, gatewayhttp.OpsClientBusinessLimitedReasonLocalModelConfiguration, gatewayhttp.OpsClientBusinessLimitedReason(c))
}

// TestClassifyOpenAICompatibleNoProviderErrorUsesMixedPool diagnoses an
// unforced compatible group containing a Grok model. It checks an empty
// platform filter, model-configuration attribution, and Grok label rewriting in
// diagnostic error text.
func TestClassifyOpenAICompatibleNoProviderErrorUsesMixedPool(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}
	groupID := int64(43)
	apiKey := &apikey.APIKey{
		GroupID: &groupID,
		Group: &routing.Group{
			ID: groupID,
		},
	}

	cls := ClassifyOpenAICompatibleNoProviderErrorFromGin(c, fd, apiKey, "grok-4.5", "grok-4.5")

	require.Equal(t, http.StatusNotFound, cls.Status)
	require.Equal(t, "model_not_found", cls.ErrType)
	require.True(t, cls.ModelNotFound)
	require.Len(t, fd.calls, 1)
	require.Empty(t, fd.calls[0].Platform)
	require.True(t, gatewayhttp.HasOpsClientBusinessLimited(c))
	require.Equal(t, gatewayhttp.OpsClientBusinessLimitedReasonLocalModelConfiguration, gatewayhttp.OpsClientBusinessLimitedReason(c))

	logErr := gatewayhttp.OpenAICompatibleSelectionErrorForLog(
		fmt.Errorf("no available OpenAI providers supporting model: grok-4.5"),
		capability.PlatformGrok,
	)
	require.EqualError(t, logErr, "no available Grok providers supporting model: grok-4.5")
}

// TestClassifyNoProviderError_PureClassifierDoesNotMarkGinContext invokes the
// context-only classifier with unsupported-model evidence. It checks
// ModelNotFound in the result while Gin observation fields remain unset until
// the HTTP wrapper owns them.
func TestClassifyNoProviderError_PureClassifierDoesNotMarkGinContext(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := classifyNoProviderError(c.Request.Context(), fd, apiKey, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.True(t, cls.ModelNotFound)
	require.False(t, gatewayhttp.HasOpsClientBusinessLimited(c))
	require.Empty(t, gatewayhttp.OpsClientBusinessLimitedReason(c))
}

// TestClassifyNoProviderError_HasModelSupport_KeepsRoutingMessageGenerationToCaller
// supplies positive configured model support. It checks the classifier retains
// 503 api_error and false model-not-found attribution for unavailable execution
// capacity.
func TestClassifyNoProviderError_HasModelSupport_KeepsRoutingMessageGenerationToCaller(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := ClassifyNoProviderErrorFromGin(c, fd, apiKey, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status, "model exists somewhere — caller stays on 503")
	require.Equal(t, "api_error", cls.ErrType)
	require.False(t, cls.ModelNotFound)
}

// TestClassifyNoProviderError_ModelSupportedOnlyByRateLimitedProvider_Returns503
// supplies persistent support despite a temporarily unavailable candidate. It
// checks that diagnosis retains service-unavailable status and false
// model-configuration attribution.
func TestClassifyNoProviderError_ModelSupportedOnlyByRateLimitedProvider_Returns503(t *testing.T) {
	c := newTestGinContextWithRequest()
	// Supply positive persistent support to isolate temporary scheduling
	// unavailability, so classification cannot confuse a cooling provider
	// with absent model configuration for the group.
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := ClassifyNoProviderErrorFromGin(c, fd, apiKey, "claude-opus-4-8", "claude-opus-4-8", capability.PlatformAnthropic)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.Equal(t, "api_error", cls.ErrType)
	require.False(t, cls.ModelNotFound, "temporary provider cooldown must remain retryable")
}

// TestClassifyNoProviderError_NoProvidersInPool_Stays503 supplies an empty
// configured pool with no support evidence. It checks the absence remains an
// availability 503, avoiding unsupported-model attribution for the unconfigured
// pool.
func TestClassifyNoProviderError_NoProvidersInPool_Stays503(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: false, HasModelSupport: false}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := ClassifyNoProviderErrorFromGin(c, fd, apiKey, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status, "empty pool is a service-availability issue, not a model issue")
	require.False(t, cls.ModelNotFound)
}

// TestClassifyNoProviderError_DisplayModelOverridesRoutingForMessage supplies
// distinct client and routed model names. It checks diagnosis uses the routed
// name while unsupported-model text identifies the client's requested display
// model.
func TestClassifyNoProviderError_DisplayModelOverridesRoutingForMessage(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := ClassifyNoProviderErrorFromGin(c, fd, apiKey, "gpt-5", "claude-3-fancy", capability.PlatformOpenAI)

	require.True(t, cls.ModelNotFound)
	require.Contains(t, cls.Message, "claude-3-fancy", "user-facing message must reference the model the user asked for, not the post-mapping routing model")
	require.Len(t, fd.calls, 1)
	require.Equal(t, "gpt-5", fd.calls[0].Model, "diagnosis must run against the routing model (post group dispatch mapping)")
}

// TestClassifyNoProviderError_FromGin_NilContextStillSafe diagnoses unsupported
// input without a Gin context. It checks coherent 404 attribution and safe
// empty observation reads when the HTTP mark has no context to update.
func TestClassifyNoProviderError_FromGin_NilContextStillSafe(t *testing.T) {
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := ClassifyNoProviderErrorFromGin(nil, fd, apiKey, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.Equal(t, http.StatusNotFound, cls.Status, "even with a nil gin context the classifier must still run and yield a coherent response")
	require.True(t, cls.ModelNotFound)
	require.False(t, gatewayhttp.HasOpsClientBusinessLimited(nil))
	require.Empty(t, gatewayhttp.OpsClientBusinessLimitedReason(nil))
}

// TestClassifySelectionFailureError_ModelNotFoundIsNotOverriddenByRateLimited
// combines persistent model-not-found classification with a scheduler cooling
// count. It checks the supplied 404 result remains intact when temporary
// filtering evidence also exists.
func TestClassifySelectionFailureError_ModelNotFoundIsNotOverriddenByRateLimited(t *testing.T) {
	modelNotFound := noProviderErrorClassification{
		Status:        http.StatusNotFound,
		ErrType:       "model_not_found",
		Message:       `Model "gpt-5.3-codex" is not supported by any configured provider in this group`,
		ModelNotFound: true,
	}

	got := classifySelectionFailureError(
		fmt.Errorf("no available OpenAI providers supporting model: gpt-5.3-codex "+
			"(pool=9, filtered: model_not_supported=8 model_rate_limited=1)"),
		modelNotFound,
	)

	require.Equal(t, modelNotFound, got,
		"分组里没有任何提供商能服务该模型时，模型级冷却不该把 404 改判成 429")
}

// TestClassifySelectionFailureError_CallSiteChainKeepsModelNotFoundAttribution
// chains HTTP diagnosis and scheduler-error refinement. It checks status, type,
// display model, and local configuration observations retain unsupported-model
// attribution when the later error also names cooling candidates.
func TestClassifySelectionFailureError_CallSiteChainKeepsModelNotFoundAttribution(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(43)}

	cls := ClassifyNoProviderErrorFromGin(c, fd, apiKey, "gpt-5.3-codex", "gpt-5.3-codex", capability.PlatformOpenAI)
	cls = classifySelectionFailureError(
		fmt.Errorf("no available OpenAI providers supporting model: gpt-5.3-codex "+
			"(pool=9, filtered: model_not_supported=8 model_rate_limited=1)"),
		cls,
	)

	require.Equal(t, http.StatusNotFound, cls.Status)
	require.Equal(t, "model_not_found", cls.ErrType)
	require.True(t, cls.ModelNotFound)
	require.Contains(t, cls.Message, "gpt-5.3-codex")
	require.True(t, gatewayhttp.HasOpsClientBusinessLimited(c))
	require.Equal(t, gatewayhttp.OpsClientBusinessLimitedReasonLocalModelConfiguration, gatewayhttp.OpsClientBusinessLimitedReason(c))
}

// TestClassifySelectionFailureError_StillUpgradesNonModelNotFoundFallback
// starts with an availability 503 and supplies positive cooling evidence. It
// checks refinement returns 429 rate_limit_error while keeping model-not-found
// attribution false.
func TestClassifySelectionFailureError_StillUpgradesNonModelNotFoundFallback(t *testing.T) {
	fallback := noProviderErrorClassification{
		Status:  http.StatusServiceUnavailable,
		ErrType: "api_error",
		Message: "Service temporarily unavailable",
	}

	got := classifySelectionFailureError(
		fmt.Errorf("no available providers supporting model: gpt-5.6-sol (total=3 eligible=0 model_rate_limited=3)"),
		fallback,
	)

	require.Equal(t, http.StatusTooManyRequests, got.Status)
	require.Equal(t, "rate_limit_error", got.ErrType)
	require.False(t, got.ModelNotFound)
}
