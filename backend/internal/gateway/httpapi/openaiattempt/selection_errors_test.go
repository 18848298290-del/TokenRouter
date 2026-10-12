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

type fakeDiagnoser struct {
	calls []fakeDiagnoseCall
	resp  routing.ModelAvailabilityDiagnosis
}

type fakeDiagnoseCall struct {
	GroupID  *int64
	Model    string
	Platform string
}

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

func ptrInt64(v int64) *int64 { return &v }

// newTestGinContextWithRequest adds a request so classifiers can read its context.
func newTestGinContextWithRequest() *gin.Context {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
	return c
}

func TestClassifyNoProviderError_NilDiagnoser_Falls503(t *testing.T) {
	c := newTestGinContextWithRequest()
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := ClassifyNoProviderErrorFromGin(c, nil, apiKey, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.Equal(t, "api_error", cls.ErrType)
	require.False(t, cls.ModelNotFound)
}

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

func TestClassifyNoProviderError_NilAPIKey_Falls503(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}

	cls := ClassifyNoProviderErrorFromGin(c, fd, nil, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.False(t, cls.ModelNotFound)
	require.Empty(t, fd.calls, "diagnoser must not be consulted when apiKey missing")
}

func TestClassifyNoProviderError_NilGroupID_Falls503(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}
	apiKey := &apikey.APIKey{GroupID: nil}

	cls := ClassifyNoProviderErrorFromGin(c, fd, apiKey, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.False(t, cls.ModelNotFound)
	require.Empty(t, fd.calls, "diagnoser must not be consulted when group not bound")
}

func TestClassifyNoProviderError_EmptyModel_Falls503(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := ClassifyNoProviderErrorFromGin(c, fd, apiKey, "   ", "", capability.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.False(t, cls.ModelNotFound)
	require.Empty(t, fd.calls)
}

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

func TestClassifyNoProviderError_PureClassifierDoesNotMarkGinContext(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := classifyNoProviderError(c.Request.Context(), fd, apiKey, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.True(t, cls.ModelNotFound)
	require.False(t, gatewayhttp.HasOpsClientBusinessLimited(c))
	require.Empty(t, gatewayhttp.OpsClientBusinessLimitedReason(c))
}

func TestClassifyNoProviderError_HasModelSupport_KeepsRoutingMessageGenerationToCaller(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := ClassifyNoProviderErrorFromGin(c, fd, apiKey, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status, "model exists somewhere — caller stays on 503")
	require.Equal(t, "api_error", cls.ErrType)
	require.False(t, cls.ModelNotFound)
}

func TestClassifyNoProviderError_ModelSupportedOnlyByRateLimitedProvider_Returns503(t *testing.T) {
	c := newTestGinContextWithRequest()
	// Persistent diagnostics must still see model support when scheduling filters the provider during cooldown.
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: true}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := ClassifyNoProviderErrorFromGin(c, fd, apiKey, "claude-opus-4-8", "claude-opus-4-8", capability.PlatformAnthropic)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.Equal(t, "api_error", cls.ErrType)
	require.False(t, cls.ModelNotFound, "temporary provider cooldown must remain retryable")
}

func TestClassifyNoProviderError_NoProvidersInPool_Stays503(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: false, HasModelSupport: false}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := ClassifyNoProviderErrorFromGin(c, fd, apiKey, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status, "empty pool is a service-availability issue, not a model issue")
	require.False(t, cls.ModelNotFound)
}

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

func TestClassifyNoProviderError_FromGin_NilContextStillSafe(t *testing.T) {
	fd := &fakeDiagnoser{resp: routing.ModelAvailabilityDiagnosis{HasProvidersInPool: true, HasModelSupport: false}}
	apiKey := &apikey.APIKey{GroupID: ptrInt64(7)}

	cls := ClassifyNoProviderErrorFromGin(nil, fd, apiKey, "gpt-5", "gpt-5", capability.PlatformOpenAI)

	require.Equal(t, http.StatusNotFound, cls.Status, "even with a nil gin context the classifier must still run and yield a coherent response")
	require.True(t, cls.ModelNotFound)
	require.False(t, gatewayhttp.HasOpsClientBusinessLimited(nil))
	require.Empty(t, gatewayhttp.OpsClientBusinessLimitedReason(nil))
}

// TestClassifySelectionFailureError_ModelNotFoundIsNotOverriddenByRateLimited checks that persistent
// model_not_found takes precedence over rate-limit filtering.
// For example, a pool can report model_not_supported=8 and model_rate_limited=1 together.
// ModelNotFound means the group cannot serve the model; returning 429 would cause futile client retries.
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

// TestClassifySelectionFailureError_CallSiteChainKeepsModelNotFoundAttribution checks
// ClassifyNoProviderErrorFromGin followed by classifySelectionFailureError.
// Callers use ModelNotFound to distinguish model configuration errors from Ops routing capacity limits.
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

// TestClassifySelectionFailureError_StillUpgradesNonModelNotFoundFallback checks 429 when capable providers
// are all cooling down; the initial 503 has HasModelSupport=true, so retry can succeed.
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
