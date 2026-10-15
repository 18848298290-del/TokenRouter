package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// scheduled_plans.go handles scheduled provider-test plan administration and
// result-history requests.
// Scheduled provider tests expose plan and result values directly as JSON on
// successful administration calls. Creation enables a plan by default, update
// retains values for omitted optional booleans and empty strings, and the
// provider service validates cron expressions and computes the next execution
// time before storage.

// ScheduledTestHandler binds recurring provider-test plan and result
// administration to the provider service.
type ScheduledTestHandler struct {
	scheduledTestSvc *provider.ScheduledTestService // borrowed plan and result administration
}

// createScheduledTestPlanRequest supplies a provider selection, required cron
// expression and optional plan controls.
type createScheduledTestPlanRequest struct {
	ProviderID     int64  `json:"provider_id" binding:"required"`     // scheduled test provider identifier
	ModelID        string `json:"model_id"`                           // requested test model identifier
	CronExpression string `json:"cron_expression" binding:"required"` // recurring schedule expression text
	// Enabled defaults to true when omitted. A supplied false creates a disabled
	// plan while still validating and storing its cron expression.
	Enabled *bool `json:"enabled"` // optional plan enablement flag
	// MaxResults limits retained test history for the plan. Nonpositive creation
	// values select fifty through ScheduledTestService before the plan is
	// persisted.
	MaxResults int `json:"max_results"` // retained test result count
	// AutoRecover defaults to false when omitted. A supplied value controls
	// whether the runner requests state recovery after a successful scheduled
	// test.
	AutoRecover *bool `json:"auto_recover"` // optional successful-test recovery flag
}

// updateScheduledTestPlanRequest supplies selected mutable fields for an
// existing provider-test plan.
type updateScheduledTestPlanRequest struct {
	// ModelID and CronExpression replace stored values only when nonempty.
	// MaxResults replaces its stored value only when positive, while Enabled and
	// AutoRecover replace theirs whenever supplied pointers are nonnil.
	ModelID        string `json:"model_id"`        // selected test model edit
	CronExpression string `json:"cron_expression"` // selected recurring schedule edit
	Enabled        *bool  `json:"enabled"`         // optional plan enablement edit
	MaxResults     int    `json:"max_results"`     // retained result count edit
	AutoRecover    *bool  `json:"auto_recover"`    // optional test recovery edit
}

// NewScheduledTestHandler borrows recurring plan and result administration for
// HTTP routes. The owner keeps repositories and schedule callbacks available
// during requests; plan creation here does not start the scheduled runner.
func NewScheduledTestHandler(scheduledTestSvc *provider.ScheduledTestService) *ScheduledTestHandler {
	return &ScheduledTestHandler{scheduledTestSvc: scheduledTestSvc}
}

// ListByProvider parses a decimal provider identifier and returns its plans
// directly as JSON. Read failures use the endpoint's 500 response, while
// malformed identifiers are rejected with 400 before repository access.
func (h *ScheduledTestHandler) ListByProvider(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid provider id")
		return
	}

	plans, err := h.scheduledTestSvc.ListPlansByProvider(c.Request.Context(), providerID)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, plans)
}

// Create binds a provider and required cron expression and returns the stored
// plan directly as JSON. Omitted enablement defaults true, omitted
// auto-recovery defaults false, and the service defaults nonpositive
// retained-result limits to fifty.
func (h *ScheduledTestHandler) Create(c *gin.Context) {
	var req createScheduledTestPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	plan := &provider.ScheduledTestPlan{
		ProviderID:     req.ProviderID,
		ModelID:        req.ModelID,
		CronExpression: req.CronExpression,
		Enabled:        true,
		MaxResults:     req.MaxResults,
	}
	if req.Enabled != nil {
		plan.Enabled = *req.Enabled
	}
	if req.AutoRecover != nil {
		plan.AutoRecover = *req.AutoRecover
	}

	created, err := h.scheduledTestSvc.CreatePlan(c.Request.Context(), plan)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, created)
}

// Update loads a plan before binding selected edits and returns its updated
// value directly as JSON. Empty model or cron strings and nonpositive result
// limits retain stored values; optional booleans permit explicit false.
func (h *ScheduledTestHandler) Update(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}

	existing, err := h.scheduledTestSvc.GetPlan(c.Request.Context(), planID)
	if err != nil {
		response.NotFound(c, "plan not found")
		return
	}

	var req updateScheduledTestPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	if req.ModelID != "" {
		existing.ModelID = req.ModelID
	}
	if req.CronExpression != "" {
		existing.CronExpression = req.CronExpression
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if req.MaxResults > 0 {
		existing.MaxResults = req.MaxResults
	}
	if req.AutoRecover != nil {
		existing.AutoRecover = *req.AutoRecover
	}

	updated, err := h.scheduledTestSvc.UpdatePlan(c.Request.Context(), existing)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, updated)
}

// Delete removes the decimal path-selected plan and returns its confirmation
// directly as JSON. Identifier failures return 400, while service deletion
// failures use the endpoint's 500 error response.
func (h *ScheduledTestHandler) Delete(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}

	if err := h.scheduledTestSvc.DeletePlan(c.Request.Context(), planID); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// ListResults returns a plan's result history directly as JSON using a default
// limit of fifty. A positive parsed limit is forwarded unchanged, and read
// failures use the endpoint's 500 response.
func (h *ScheduledTestHandler) ListResults(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}

	limit := 50
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 {
		limit = l
	}

	results, err := h.scheduledTestSvc.ListResults(c.Request.Context(), planID, limit)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, results)
}
