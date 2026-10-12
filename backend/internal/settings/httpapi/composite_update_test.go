package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestRemovedCrossClientModelMappingField checks rejection before settings are read or changed.
func TestRemovedCrossClientModelMappingField(t *testing.T) {
	for _, value := range []string{"true", "false", "null"} {
		writer := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(writer)
		ctx.Request = httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"grok_cross_client_model_map_enabled":`+value+`}`))
		ctx.Request.Header.Set("Content-Type", "application/json")
		(&Handler{}).UpdateSettings(ctx)
		require.Equal(t, http.StatusBadRequest, writer.Code)
		require.Contains(t, writer.Body.String(), "model_mapping")
	}
}

// TestUpdateSettingsRejectsRemovedPlatformQuotas checks rejection, including null, before other settings are
// accessed.
func TestUpdateSettingsRejectsRemovedPlatformQuotas(t *testing.T) {
	for _, name := range []string{"default_platform_quotas", "auth_source_default_email_platform_quotas", "auth_source_default_linuxdo_platform_quotas", "auth_source_default_oidc_platform_quotas", "auth_source_default_wechat_platform_quotas", "auth_source_default_github_platform_quotas", "auth_source_default_google_platform_quotas", "auth_source_default_dingtalk_platform_quotas"} {
		t.Run(name, func(t *testing.T) {
			router := gin.New()
			router.PUT("/settings", (&Handler{}).UpdateSettings)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"`+name+`":null}`))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "REMOVED_SETTING_FIELD")
			require.Contains(t, recorder.Body.String(), name)
		})
	}
}

// TestUpdateSettingsRejectsUngroupedScheduling checks rejection before accessing settings storage.
func TestUpdateSettingsRejectsUngroupedScheduling(t *testing.T) {
	for _, value := range []string{"true", "false", "null"} {
		router := gin.New()
		router.PUT("/settings", (&Handler{}).UpdateSettings)
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"allow_ungrouped_key_scheduling":`+value+`}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
		require.Contains(t, recorder.Body.String(), "REMOVED_SETTING_FIELD")
	}
}
