package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// TestPreviewAdvancedSchedulerScoreRejectsSensitiveContextFields submits a
// preview object with a session_hash field outside the request schema. It
// requires a bad-request status and an unknown-field message before the
// unavailable diagnostic source can be invoked.
func TestPreviewAdvancedSchedulerScoreRejectsSensitiveContextFields(t *testing.T) {
	handler := NewDiagnosticsHandler(scheduler.NewDiagnosticService(nil, nil, scheduler.DiagnosticPorts{}))
	router := gin.New()
	router.POST("/api/v1/admin/providers/:id/advanced-scheduler-score/preview", handler.PreviewAdvancedSchedulerScore)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/providers/42/advanced-scheduler-score/preview",
		strings.NewReader(`{"group_id": 9, "session_hash": "must-not-be-accepted"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), "unknown field")
}
