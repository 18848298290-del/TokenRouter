package httpapi

// These scenarios cover request fields in management_write.go, management_bulk.go, management_create.go, archive.go, codex_import.go, openai_oauth.go, and grok_oauth.go.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProviderAdminBoundariesDiscardDeprecatedLongContextBillingExtra(t *testing.T) {
	const malformedExtra = `"extra":{"openai_long_context_billing_enabled":{"malformed":true},"preserved":"value"}`

	tests := []struct {
		name          string
		method        string
		path          string
		body          string
		mount         func(*gin.Engine, *ManagementHandler)
		capturedExtra func(*managementMutationFixture) map[string]any
	}{
		{
			name:   "create",
			method: http.MethodPost,
			path:   "/providers",
			body:   `{"name":"provider","platform":"openai","type":"apikey","credentials":{"api_key":"test"},` + malformedExtra + `}`,
			mount:  func(router *gin.Engine, handler *ManagementHandler) { router.POST("/providers", handler.Create) },
			capturedExtra: func(stub *managementMutationFixture) map[string]any {
				require.Len(t, stub.createdProviders, 1)
				return stub.createdProviders[0].Extra
			},
		},
		{
			name:   "update",
			method: http.MethodPut,
			path:   "/providers/1",
			body:   `{` + malformedExtra + `}`,
			mount:  func(router *gin.Engine, handler *ManagementHandler) { router.PUT("/providers/:id", handler.Update) },
			capturedExtra: func(stub *managementMutationFixture) map[string]any {
				require.NotNil(t, stub.updateProviderInput)
				return stub.updateProviderInput.Extra
			},
		},
		{
			name:   "bulk update",
			method: http.MethodPost,
			path:   "/providers/bulk-update",
			body:   `{"provider_ids":[1],` + malformedExtra + `}`,
			mount: func(router *gin.Engine, handler *ManagementHandler) {
				router.POST("/providers/bulk-update", handler.BulkUpdate)
			},
			capturedExtra: func(stub *managementMutationFixture) map[string]any {
				require.NotNil(t, stub.lastBulkUpdateProviderInput)
				return stub.lastBulkUpdateProviderInput.Extra
			},
		},
		{
			name:   "batch create",
			method: http.MethodPost,
			path:   "/providers/batch",
			body:   `{"providers":[{"name":"provider","platform":"openai","type":"apikey","credentials":{"api_key":"test"},` + malformedExtra + `}]}`,
			mount: func(router *gin.Engine, handler *ManagementHandler) {
				router.POST("/providers/batch", handler.BatchCreate)
			},
			capturedExtra: func(stub *managementMutationFixture) map[string]any {
				require.Len(t, stub.createdProviders, 1)
				return stub.createdProviders[0].Extra
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newManagementMutationFixture()
			handler := newMutationHandler(stub, nil)
			router := gin.New()
			tt.mount(router, handler)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.body))
			request.Header.Set("Content-Type", "application/json")

			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			extra := tt.capturedExtra(stub)
			require.NotContains(t, extra, deprecatedLongContextBillingExtraKey)
			require.Equal(t, "value", extra["preserved"])
		})
	}
}

// TestProviderManagementRejectsRetiredGroupInputs checks retired routes and rejects retired confirmation and default-group fields before writes.
func TestProviderManagementRejectsRetiredGroupInputs(t *testing.T) {
	for _, endpoint := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/admin/providers", `{"name":"mixed","platform":"openai","type":"apikey","credentials":{"api_key":"test"}}`},
		{http.MethodPut, "/api/v1/admin/providers/3", `{"group_ids":[27]}`},
		{http.MethodPost, "/api/v1/admin/providers/bulk-update", `{"provider_ids":[1],"group_ids":[27]}`},
	} {
		for _, field := range []string{"confirm_mixed_channel_risk", "skip_default_group_bind"} {
			t.Run(endpoint.method+endpoint.path+field, func(t *testing.T) {
				source := newManagementMutationFixture()
				router := setupProviderMutationContractRouter(source)
				var payload map[string]any
				require.NoError(t, json.Unmarshal([]byte(endpoint.body), &payload))
				payload[field] = false
				body, err := json.Marshal(payload)
				require.NoError(t, err)
				recorder := httptest.NewRecorder()
				req := httptest.NewRequest(endpoint.method, endpoint.path, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(recorder, req)
				require.Equal(t, http.StatusBadRequest, recorder.Code)
				require.Contains(t, recorder.Body.String(), field)
			})
		}
	}
	router := setupProviderMutationContractRouter(newManagementMutationFixture())
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers/check-mixed-channel", nil))
	require.Equal(t, http.StatusNotFound, recorder.Code)
}

// TestProviderImportsRejectRetiredGroupInputs checks that OAuth and import handlers reject retired fields before creation or token exchange.
func TestProviderImportsRejectRetiredGroupInputs(t *testing.T) {
	router := gin.New()
	router.POST("/batch", (&ManagementHandler{}).BatchCreate)
	router.POST("/archive", (&ArchiveHandler{}).ImportData)
	router.POST("/codex", (&CodexImportHandler{}).ImportCodexSession)
	router.POST("/pat", (&OpenAIOAuthHandler{}).CreateProviderFromCodexPAT)
	router.POST("/oauth", (&OpenAIOAuthHandler{}).CreateProviderFromOAuth)
	router.POST("/shadow/:id", (&OpenAIOAuthHandler{}).CreateShadow)
	router.POST("/grok/oauth", (&GrokOAuthHandler{}).CreateProviderFromOAuth)
	router.POST("/grok/sso", (&GrokOAuthHandler{}).CreateProvidersFromSSO)
	for _, field := range []string{"confirm_mixed_channel_risk", "skip_default_group_bind"} {
		for _, path := range []string{"/archive", "/codex", "/pat", "/oauth", "/shadow/1", "/grok/oauth", "/grok/sso", "/batch"} {
			t.Run(path+field, func(t *testing.T) {
				body := `{"` + field + `":null}`
				if path == "/batch" {
					body = `{"providers":[{"name":"a","platform":"openai","type":"apikey","credentials":{},"` + field + `":false}]}`
				}
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, req)
				require.Equal(t, http.StatusBadRequest, recorder.Code)
				require.Contains(t, recorder.Body.String(), field)
			})
		}
	}
}
