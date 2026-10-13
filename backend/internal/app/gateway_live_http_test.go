package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Live admission and application stopping are checked before request-body
// reading. Two handlers share one activity barrier, making an application stop
// visible to the second handler while the first scenario exposes the group's
// disabled Live permission.

// TestLiveAssemblyKeepsPermissionAndStopBeforeRead invokes two handlers sharing
// one application activity barrier. It checks group-permission rejection before
// stopping, shutdown rejection afterward, and no request-body read in either
// path.
func TestLiveAssemblyKeepsPermissionAndStopBeforeRead(t *testing.T) {
	activity := &gatewayRequestActivity{Operations: lifecycle.NewOperations("live-contract")}
	first := provideLiveHTTP(nil, nil, nil, nil, activity)
	second := provideLiveHTTP(nil, nil, nil, nil, activity)
	for _, stopped := range []bool{false, true} {
		if stopped {
			// Seal the shared barrier after the first call so the second handler
			// observes shutdown through its existing activity binding before reading request input.
			require.NoError(t, activity.StopContext(context.Background()))
		}
		writer := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(writer)
		body := &protocolGateTrackingReader{}
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/live", body)
		c.Set(string(keyhttp.ContextKeyAPIKey), &apikey.APIKey{ID: 2, UserID: 1, Group: &routing.Group{}})
		c.Set(authctx.ContextKeyUser, authctx.AuthSubject{UserID: 1})
		if stopped {
			second.Live(c)
			require.Equal(t, http.StatusServiceUnavailable, writer.Code)
			require.Contains(t, writer.Body.String(), "Service is shutting down")
		} else {
			first.Live(c)
			require.Equal(t, http.StatusForbidden, writer.Code)
			require.Contains(t, writer.Body.String(), "Live is not enabled for this group")
		}
		require.False(t, body.read)
	}
}
