package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	opscore "github.com/TokenFlux/TokenRouter/internal/ops"
	opsprovider "github.com/TokenFlux/TokenRouter/internal/ops/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	middleware2 "github.com/TokenFlux/TokenRouter/internal/server/middleware"
)

func TestOpsErrorLoggerMiddleware_DedicatedCyberSessionBlockRecordsExactlyOnce(t *testing.T) {
	queue := newOpsCaptureQueue(3)

	ops := opscore.NewOpsService(nil, nil, nil, nil, nil, nil, nil, opsprovider.LogControl{})
	h := newGatewayHTTPEndpoints(gatewayHTTPFixtureInput{Ops: ops, Queue: queue})
	apiKey := &apikey.APIKey{ID: 41, Key: "sk-dedicated-test"}
	router := gin.New()
	router.Use(gatewayhttp.OpsErrorLoggerMiddleware(ops, queue, gatewayhttp.OpsObservationAccess{}))
	router.POST("/v1/responses", func(c *gin.Context) {
		h.enqueueCyberSessionBlockedOpsEntry(c, apiKey, "gpt-test", "session-block-hash")
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{
			"type": "permission_error", "code": "session_blocked_by_cyber_policy", "message": "blocked",
		}})
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Equal(t, int64(1), queue.health.Length)
	job := <-queue.jobs
	require.Equal(t, "cyber_policy_session_blocked", job.entry.ErrorType)
	require.Equal(t, http.StatusForbidden, job.entry.StatusCode)
}

func TestOpsErrorLoggerMiddleware_DoesNotBreakOuterMiddlewares(t *testing.T) {
	r := gin.New()
	r.Use(middleware2.Recovery())
	r.Use(middleware2.RequestLogger())
	r.Use(middleware2.Logger())
	r.GET("/v1/messages", gatewayhttp.OpsErrorLoggerMiddleware(nil, nil, provideOpsObservationAccess()), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)

	require.NotPanics(t, func() {
		r.ServeHTTP(rec, req)
	})
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestGetOpsAPIKeyFallsBackToOpsFallbackKey(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// An early authentication exit can leave the primary Key absent.
	require.Nil(t, provideOpsObservationAccess().APIKey(c))

	// The Ops-specific Key includes its loaded user and group for error recording.
	groupID := int64(55)
	apiKey := &apikey.APIKey{
		ID:      100,
		GroupID: &groupID,
		User:    &identity.User{ID: 7},
		Group:   &routing.Group{ID: groupID},
	}
	c.Set(string(keyhttp.ContextKeyOpsFallbackAPIKey), apiKey)

	got := provideOpsObservationAccess().APIKey(c)
	require.NotNil(t, got)
	require.Equal(t, int64(100), got.ID)
	require.NotNil(t, got.User)
	require.Equal(t, int64(7), got.User.ID)
	require.NotNil(t, got.Group)
	_, authenticated := authctx.GetPrincipal(c)
	require.False(t, authenticated, "读取失败 Key 的观测投影不能创建认证主体")
}

func TestGetOpsAPIKeyPrefersPrimaryContextKey(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	primary := &apikey.APIKey{ID: 1}
	fallback := &apikey.APIKey{ID: 2}
	c.Set("gateway_effective_key", primary)
	c.Set(string(keyhttp.ContextKeyOpsFallbackAPIKey), fallback)

	got := provideOpsObservationAccess().APIKey(c)
	require.NotNil(t, got)
	require.Equal(t, int64(1), got.ID, "已鉴权请求应优先使用正式 api key")
}

// TestOpsObservationAccessUsesTypedIngressMarker checks typed rejection markers and ignores ordinary strings.
func TestOpsObservationAccessUsesTypedIngressMarker(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	access := provideOpsObservationAccess()
	require.False(t, access.Rejected(c))
	c.Set("ingress_reject_reason", "invalid_api_key")
	require.False(t, access.Rejected(c))
	middleware2.MarkIngressRejected(c, middleware2.IngressRejectInvalidAPIKey)
	require.True(t, access.Rejected(c))
}
