package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// ProviderSchedulerDiagnostics supplies score diagnostics computed by scheduler.
type ProviderSchedulerDiagnostics interface {
	GetOverview(context.Context, int64) (*policy.AdvancedSchedulerScoreDiagnosticResponse, error)
	GetDetail(context.Context, int64, policy.AdvancedSchedulerScoreDiagnosticRequest) (*policy.AdvancedSchedulerScoreDiagnosticResponse, error)
}

// DiagnosticsHandler handles HTTP requests for scheduler score diagnostics.
type DiagnosticsHandler struct{ diagnostics ProviderSchedulerDiagnostics }

func NewDiagnosticsHandler(source ProviderSchedulerDiagnostics) *DiagnosticsHandler {
	return &DiagnosticsHandler{diagnostics: source}
}

// GetAdvancedSchedulerScore returns advanced group summaries or a full explanation for one group.
// GET /api/v1/admin/providers/:id/advanced-scheduler-score?group_id=:groupID
func (h *DiagnosticsHandler) GetAdvancedSchedulerScore(c *gin.Context) {
	providerID, ok := parseAdvancedSchedulerScoreProviderID(c)
	if !ok {
		return
	}
	if h == nil || h.diagnostics == nil {
		response.InternalError(c, "Advanced scheduler diagnostics are unavailable")
		return
	}

	groupIDRaw := strings.TrimSpace(c.Query("group_id"))
	if groupIDRaw == "" {
		result, err := h.diagnostics.GetOverview(c.Request.Context(), providerID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		response.Success(c, result)
		return
	}
	groupID, err := strconv.ParseInt(groupIDRaw, 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	result, err := h.diagnostics.GetDetail(c.Request.Context(), providerID, policy.AdvancedSchedulerScoreDiagnosticRequest{GroupID: groupID})
	if err != nil {
		if strings.Contains(err.Error(), "advanced scheduler group") || strings.Contains(err.Error(), "group_id") {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// PreviewAdvancedSchedulerScore simulates scores using model and provider IDs without session state.
// POST /api/v1/admin/providers/:id/advanced-scheduler-score/preview
func (h *DiagnosticsHandler) PreviewAdvancedSchedulerScore(c *gin.Context) {
	providerID, ok := parseAdvancedSchedulerScoreProviderID(c)
	if !ok {
		return
	}
	if h == nil || h.diagnostics == nil {
		response.InternalError(c, "Advanced scheduler diagnostics are unavailable")
		return
	}

	var request policy.AdvancedSchedulerScoreDiagnosticRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		response.BadRequest(c, "Invalid advanced scheduler score preview request: "+err.Error())
		return
	}
	if err := ensureAdvancedSchedulerScorePreviewEOF(decoder); err != nil {
		response.BadRequest(c, "Invalid advanced scheduler score preview request: "+err.Error())
		return
	}
	if request.GroupID <= 0 {
		response.BadRequest(c, "group_id is required")
		return
	}

	result, err := h.diagnostics.GetDetail(c.Request.Context(), providerID, request)
	if err != nil {
		if strings.Contains(err.Error(), "advanced scheduler group") || strings.Contains(err.Error(), "group_id") || strings.Contains(err.Error(), "sticky provider") || strings.Contains(err.Error(), "requested_model") {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func parseAdvancedSchedulerScoreProviderID(c *gin.Context) (int64, bool) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || providerID <= 0 {
		response.BadRequest(c, "Invalid provider ID")
		return 0, false
	}
	return providerID, true
}

func ensureAdvancedSchedulerScorePreviewEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return io.ErrUnexpectedEOF
	}
	return err
}

// RegisterProviderDiagnostics registers provider score routes; app installs group authorization.
func RegisterProviderDiagnostics(providers *gin.RouterGroup, endpoint *DiagnosticsHandler) {
	providers.GET("/:id/advanced-scheduler-score", endpoint.GetAdvancedSchedulerScore)
	providers.POST("/:id/advanced-scheduler-score/preview", endpoint.PreviewAdvancedSchedulerScore)
}
