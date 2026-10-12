package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/gin-gonic/gin"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

type preAggregationAvailabilityResponse struct {
	UsageAvailable          bool   `json:"usage_available"`
	UsageDisabledReason     string `json:"usage_disabled_reason,omitempty"`
	OpsAvailable            bool   `json:"ops_available"`
	OpsDisabledReason       string `json:"ops_disabled_reason,omitempty"`
	ManualBackfillAvailable bool   `json:"manual_backfill_available"`
	ManualBackfillMaxDays   int    `json:"manual_backfill_max_days"`
}

type preAggregationSettingsResponse struct {
	Settings     preaggregation.PreAggregationSettings      `json:"settings"`
	Availability preAggregationAvailabilityResponse         `json:"availability"`
	UsageStatus  preaggregation.PreAggregationRuntimeStatus `json:"usage_status"`
	OpsStatus    preaggregation.PreAggregationRuntimeStatus `json:"ops_status"`
}

type preAggregationBackfillRequest struct {
	Days int `json:"days"`
}

// AggregationStatus reads pre-aggregation task status.
type AggregationStatus interface {
	RuntimeStatus(context.Context) preaggregation.PreAggregationRuntimeStatus
}
type UsageBackfill interface {
	AggregationStatus
	TriggerBackfill(time.Time, time.Time) error
}

// PreAggregationHandler maps HTTP requests to the shared settings controller and running tasks.
type PreAggregationHandler struct {
	preAggregationSettings *preaggregation.PreAggregationSettingsService
	dashboardAggregation   UsageBackfill
	opsAggregation         AggregationStatus
}

func NewPreAggregationHandler(settings *preaggregation.PreAggregationSettingsService, usage UsageBackfill, ops AggregationStatus) *PreAggregationHandler {
	return &PreAggregationHandler{preAggregationSettings: settings, dashboardAggregation: usage, opsAggregation: ops}
}

// GetPreAggregationSettings returns runtime settings, deployment availability, and task status.
func (h *PreAggregationHandler) GetPreAggregationSettings(c *gin.Context) {
	if h.preAggregationSettings == nil {
		response.InternalError(c, "Pre-aggregation settings service not available")
		return
	}
	response.Success(c, h.buildPreAggregationSettingsResponse(c))
}

// UpdatePreAggregationSettings replaces the runtime pre-aggregation configuration.
func (h *PreAggregationHandler) UpdatePreAggregationSettings(c *gin.Context) {
	if h.preAggregationSettings == nil {
		response.InternalError(c, "Pre-aggregation settings service not available")
		return
	}
	var request preaggregation.PreAggregationSettings
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	if request.Usage.IntervalSeconds < preaggregation.PreAggregationMinIntervalSeconds || request.Usage.IntervalSeconds > preaggregation.PreAggregationMaxIntervalSeconds {
		response.BadRequest(c, "Usage aggregation interval must be between 30 and 3600 seconds")
		return
	}
	availability := h.preAggregationSettings.Availability()
	if request.Usage.Enabled && !availability.UsageAvailable {
		response.BadRequest(c, "Usage pre-aggregation is disabled by deployment configuration")
		return
	}
	if request.Ops.Enabled && !availability.OpsAvailable {
		response.BadRequest(c, "Ops pre-aggregation is disabled by deployment configuration")
		return
	}
	if _, err := h.preAggregationSettings.Update(c.Request.Context(), request); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, h.buildPreAggregationSettingsResponse(c))
}

// BackfillPreAggregation requests an asynchronous usage backfill for the specified number of recent days.
func (h *PreAggregationHandler) BackfillPreAggregation(c *gin.Context) {
	if h.preAggregationSettings == nil || h.dashboardAggregation == nil {
		response.InternalError(c, "Usage pre-aggregation service not available")
		return
	}
	var request preAggregationBackfillRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	availability := h.preAggregationSettings.Availability()
	if !availability.ManualBackfillAvailable {
		response.Forbidden(c, "Manual pre-aggregation backfill is disabled")
		return
	}
	if request.Days < 1 || request.Days > availability.ManualBackfillMaxDays {
		response.BadRequest(c, "Backfill days exceed the configured limit")
		return
	}
	if !h.preAggregationSettings.UsageEnabled(c.Request.Context()) {
		response.BadRequest(c, "Usage pre-aggregation is disabled")
		return
	}
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -request.Days)
	if err := h.dashboardAggregation.TriggerBackfill(start, end); err != nil {
		if errors.Is(err, usage.ErrDashboardBackfillDisabled) {
			response.Forbidden(c, "Manual pre-aggregation backfill is disabled")
			return
		}
		if errors.Is(err, usage.ErrDashboardBackfillTooLarge) {
			response.BadRequest(c, "Backfill days exceed the configured limit")
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Accepted(c, gin.H{"status": "accepted", "days": request.Days})
}

func (h *PreAggregationHandler) buildPreAggregationSettingsResponse(c *gin.Context) preAggregationSettingsResponse {
	ctx := c.Request.Context()
	availability := h.preAggregationSettings.Availability()
	result := preAggregationSettingsResponse{
		Settings: h.preAggregationSettings.Resolve(ctx),
		Availability: preAggregationAvailabilityResponse{
			UsageAvailable:          availability.UsageAvailable,
			UsageDisabledReason:     availability.UsageDisabledReason,
			OpsAvailable:            availability.OpsAvailable,
			OpsDisabledReason:       availability.OpsDisabledReason,
			ManualBackfillAvailable: availability.ManualBackfillAvailable,
			ManualBackfillMaxDays:   availability.ManualBackfillMaxDays,
		},
		UsageStatus: preaggregation.PreAggregationRuntimeStatus{Phase: "unavailable"},
		OpsStatus:   preaggregation.PreAggregationRuntimeStatus{Phase: "unavailable"},
	}
	if h.dashboardAggregation != nil {
		result.UsageStatus = h.dashboardAggregation.RuntimeStatus(ctx)
	}
	if h.opsAggregation != nil {
		result.OpsStatus = h.opsAggregation.RuntimeStatus(ctx)
	}
	return result
}
