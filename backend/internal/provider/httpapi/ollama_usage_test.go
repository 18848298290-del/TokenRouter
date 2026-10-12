package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// Dedicated Ollama usage endpoints reject invalid identifiers and missing
// managed-session fields before core work. ollama_usage.go is exercised with an
// unstarted empty service for defaults and validation, plus a nil service for
// the domain unavailable response.

// TestOllamaCloudUsageHandlersValidateRequestsAndDependencies submits invalid
// provider IDs, absent session text, omitted enablement and an unavailable
// service. Dedicated handlers return their expected 400 or 503 responses before
// the controlled core can change managed state.
func TestOllamaCloudUsageHandlersValidateRequestsAndDependencies(t *testing.T) {
	svc := newOllamaCloudUsageHandlerTestService(t)

	t.Run("invalid provider id", func(t *testing.T) {
		ctx, recorder := newOllamaCloudUsageHandlerContext(http.MethodGet, "/admin/providers/not-an-id/ollama-cloud-usage", "", "not-an-id")
		NewOllamaUsageHandler(svc).GetOllamaCloudUsage(ctx)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("empty session", func(t *testing.T) {
		ctx, recorder := newOllamaCloudUsageHandlerContext(http.MethodPut, "/admin/providers/7/ollama-cloud-usage/session", `{"session":""}`, "7")
		NewOllamaUsageHandler(svc).SaveOllamaCloudUsageSession(ctx)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("missing enabled", func(t *testing.T) {
		ctx, recorder := newOllamaCloudUsageHandlerContext(http.MethodPut, "/admin/providers/7/ollama-cloud-usage/auto-refresh", `{}`, "7")
		NewOllamaUsageHandler(svc).SetOllamaCloudUsageAutoRefresh(ctx)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("service unavailable", func(t *testing.T) {
		ctx, recorder := newOllamaCloudUsageHandlerContext(http.MethodGet, "/admin/providers/7/ollama-cloud-usage", "", "7")
		NewOllamaUsageHandler(nil).GetOllamaCloudUsage(ctx)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		require.Contains(t, recorder.Body.String(), "OLLAMA_CLOUD_USAGE_UNAVAILABLE")
	})
}

// TestGetOllamaCloudUsageSettingsHandlerSuccess reads settings from a newly
// constructed service with no backing dependencies. The response exposes
// disabled monitoring and the standard interval and debounce values through the
// dedicated settings endpoint.
func TestGetOllamaCloudUsageSettingsHandlerSuccess(t *testing.T) {
	ctx, recorder := newOllamaCloudUsageHandlerContext(http.MethodGet, "/admin/providers/ollama-cloud-usage/settings", "", "")
	handler := NewOllamaUsageHandler(newOllamaCloudUsageHandlerTestService(t))

	handler.GetOllamaCloudUsageSettings(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"enabled":false`)
	require.Contains(t, recorder.Body.String(), `"interval_minutes":60`)
	require.Contains(t, recorder.Body.String(), `"debounce_minutes":1`)
}

// newOllamaCloudUsageHandlerTestService creates an unstarted Ollama service
// with empty options and registers cleanup. It supports settings and
// early-validation paths, while missing dependencies are intentionally
// unavailable for upstream or storage work.
func newOllamaCloudUsageHandlerTestService(t *testing.T) *providercore.OllamaCloudUsageService {
	t.Helper()
	svc := providercore.NewOllamaCloudUsageService(nil, nil, nil, providercore.OllamaUsageOptions{})
	// Even an unstarted service owns its operation-stop state. Register cleanup
	// so later fixture changes cannot leave admitted usage work after test completion.
	t.Cleanup(svc.Stop)
	return svc
}

// newOllamaCloudUsageHandlerContext builds a JSON request and recorder and
// optionally installs a provider path parameter. The returned Gin context
// borrows the request and recorder for synchronous handler execution.
func newOllamaCloudUsageHandlerContext(method, target, body, id string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = request
	if id != "" {
		ctx.Params = gin.Params{{Key: "id", Value: id}}
	}
	return ctx, recorder
}
