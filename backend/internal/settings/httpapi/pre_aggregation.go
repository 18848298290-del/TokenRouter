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

// pre_aggregation.go serves aggregation settings, task status, and manual
// backfill requests.
// Administrator preaggregation settings show normalized runtime controls beside
// deployment permissions and observed task state. GET can select cached or default
// controls after a settings read fails, while PUT validates capability and cadence
// before saving a complete replacement. A manual usage backfill is admitted by
// deployment permission, current usage enablement, and the requested day limit.
//
// The backfill response acknowledges persisted work, and the usage task performs
// the range asynchronously. Missing task dependencies produce unavailable status
// for display and reject a manual backfill before triggering it. The current-day
// range is constructed in UTC, letting the usage implementation align its saved
// manual target and cursor to the aggregation hours.

// preAggregationAvailabilityResponse exposes deployment permissions and limits
// in the administrator response.
type preAggregationAvailabilityResponse struct {
	UsageAvailable          bool   `json:"usage_available"`                 // deployment usage aggregation permission
	UsageDisabledReason     string `json:"usage_disabled_reason,omitempty"` // usage deployment restriction reason
	OpsAvailable            bool   `json:"ops_available"`                   // deployment Ops aggregation permission
	OpsDisabledReason       string `json:"ops_disabled_reason,omitempty"`   // Ops deployment restriction reason
	ManualBackfillAvailable bool   `json:"manual_backfill_available"`       // deployment manual backfill permission
	ManualBackfillMaxDays   int    `json:"manual_backfill_max_days"`        // maximum manual backfill days
}

// preAggregationSettingsResponse combines resolved controls, deployment permissions,
// and observed task status.
type preAggregationSettingsResponse struct {
	Settings     preaggregation.PreAggregationSettings      `json:"settings"`     // resolved runtime aggregation controls
	Availability preAggregationAvailabilityResponse         `json:"availability"` // deployment task capability restrictions
	UsageStatus  preaggregation.PreAggregationRuntimeStatus `json:"usage_status"` // usage task coverage observations
	OpsStatus    preaggregation.PreAggregationRuntimeStatus `json:"ops_status"`   // Ops hourly heartbeat observations
}

// preAggregationBackfillRequest selects the recent usage history requested for
// asynchronous recomputation.
type preAggregationBackfillRequest struct {
	// Days selects the UTC recent-day range. BackfillPreAggregation accepts one
	// through the deployment maximum before asking the usage task to persist work.
	Days int `json:"days"` // recent calendar days requested
}

// AggregationStatus supplies observed aggregation state for the administrator settings response.
type AggregationStatus interface {
	// RuntimeStatus returns the task's current persisted observations or a
	// phase describing missing dependencies or read failure. Its timestamp pointers
	// follow the producer's sharing rules and are treated as immutable by this handler.
	RuntimeStatus(context.Context) preaggregation.PreAggregationRuntimeStatus
}

// UsageBackfill reports usage aggregation state and admits persisted manual backfill requests.
type UsageBackfill interface {
	AggregationStatus // usage task status reader
	// TriggerBackfill persists the supplied range and wakes asynchronous usage
	// processing. Successful return admits work; callers display acceptance while
	// coverage and task status remain responsible for reporting later completion.
	TriggerBackfill(time.Time, time.Time) error
}

// PreAggregationHandler maps HTTP requests to the shared settings controller and running tasks.
type PreAggregationHandler struct {
	// preAggregationSettings is the shared controller supplied at assembly.
	// Endpoints reject its absence before resolving or replacing configuration.
	preAggregationSettings *preaggregation.PreAggregationSettingsService
	// dashboardAggregation supplies usage status and backfill admission. A nil
	// dependency displays unavailable status and rejects manual backfill before any request is made.
	dashboardAggregation UsageBackfill
	// opsAggregation supplies hourly task observations when installed. A nil
	// dependency leaves its response phase unavailable while settings remain readable.
	opsAggregation AggregationStatus
}

// NewPreAggregationHandler retains the shared settings controller and task
// readers supplied by assembly. Task readers may be nil for unavailable status;
// manual backfill additionally requires the usage reader with its trigger operation.
func NewPreAggregationHandler(settings *preaggregation.PreAggregationSettingsService, usage UsageBackfill, ops AggregationStatus) *PreAggregationHandler {
	return &PreAggregationHandler{preAggregationSettings: settings, dashboardAggregation: usage, opsAggregation: ops}
}

// GetPreAggregationSettings returns resolved controls, deployment permissions,
// and observed task status through the success response. An absent controller
// fails; settings read failures follow Resolve's cached or default display selection.
func (h *PreAggregationHandler) GetPreAggregationSettings(c *gin.Context) {
	if h.preAggregationSettings == nil {
		response.InternalError(c, "Pre-aggregation settings service not available")
		return
	}
	response.Success(c, h.buildPreAggregationSettingsResponse(c))
}

// UpdatePreAggregationSettings validates and persists a complete runtime
// replacement. The usage interval must be 30 through 3,600 seconds, and submitted
// enabled switches require deployment availability. Omitted object fields keep
// the bound struct's zero values; this endpoint supplies no partial merge.
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
	// Reject unsupported cadence before saving this complete runtime replacement.
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

// BackfillPreAggregation admits recent usage backfill after checking deployment
// permission, day limits, and current usage enablement. It computes the requested
// UTC calendar-day range and delegates persistence plus task wakeup before returning
// 202 acceptance with the submitted days.
//
// The usage service owns hour alignment and later execution. Its disabled and
// range-limit errors become forbidden or bad-request responses, while other trigger
// failures keep their error classification. Acceptance reports admitted work;
// clients use later task observations to determine completion and coverage.
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
	// Keep the requested UTC history within the deployment manual-backfill ceiling.
	if request.Days < 1 || request.Days > availability.ManualBackfillMaxDays {
		response.BadRequest(c, "Backfill days exceed the configured limit")
		return
	}
	if !h.preAggregationSettings.UsageEnabled(c.Request.Context()) {
		response.BadRequest(c, "Usage pre-aggregation is disabled")
		return
	}
	// Build the recent-day interval in UTC before the usage service aligns its
	// persisted manual target and cursor to hours and wakes asynchronous aggregation work.
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -request.Days)
	if err := h.dashboardAggregation.TriggerBackfill(start, end); err != nil {
		// Translate the task's admission errors to the same HTTP decisions as the
		// deployment checks, while retaining other persistence or wakeup failures for the caller.
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

// buildPreAggregationSettingsResponse resolves controls and adds deployment
// permissions with usage/Ops observations. Each missing task reader keeps an
// unavailable phase, while installed readers supply their own current status and
// shared timestamp values for the response.
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
