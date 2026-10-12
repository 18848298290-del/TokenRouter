package httpapi

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	keydto "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	groupdto "github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// BatchSetGroupRateMultipliersRequest contains per-user rate multipliers for a group.
type BatchSetGroupRateMultipliersRequest struct {
	Entries []billing.GroupRateMultiplierInput `json:"entries" binding:"required"`
}

// BatchSetGroupRPMOverridesRequest contains per-user RPM overrides for a group.
type BatchSetGroupRPMOverridesRequest struct {
	Entries []billing.GroupRPMOverrideInput `json:"entries" binding:"required"`
}

// GroupUsageSummary contains the admin usage totals returned by the usage reader.
type GroupUsageSummary struct {
	GroupID       int64   `json:"group_id"`
	TodayCost     float64 `json:"today_cost"`
	YesterdayCost float64 `json:"yesterday_cost"`
	TotalCost     float64 `json:"total_cost"`
}
type GroupRateAdministration interface {
	GetGroupRateMultipliers(context.Context, int64) ([]billing.UserGroupRateEntry, error)
	ClearGroupRateMultipliers(context.Context, int64) error
	BatchSetGroupRateMultipliers(context.Context, int64, []billing.GroupRateMultiplierInput) error
	BatchSetGroupRPMOverrides(context.Context, int64, []billing.GroupRPMOverrideInput) error
	ClearGroupRPMOverrides(context.Context, int64) error
}

// GroupResources supplies group capacity and linked-resource queries.
type GroupResources struct {
	Capacity interface {
		GetAllGroupCapacity(context.Context) ([]routing.GroupCapacitySummary, error)
	}
	Keys           func(context.Context, int64, int, int) ([]keydto.APIKey[groupdto.Group], int64, error)
	Rates          GroupRateAdministration
	UsageSummary   func(context.Context, time.Time) ([]GroupUsageSummary, error)
	Today          func() time.Time
	LiveCapability func(context.Context) error
}

// GetLiveCapability reports whether the server can generate Live attestations.
func (h *GroupHandler) GetLiveCapability(c *gin.Context) {
	err := h.resources.LiveCapability(c.Request.Context())
	result := gin.H{"supported": err == nil}
	if err != nil {
		result["reason"] = err.Error()
	}
	response.Success(c, result)
}

// GetUsageSummary returns today's, yesterday's, and all-time group costs in the server timezone.
// GET /api/v1/admin/groups/usage-summary
func (h *GroupHandler) GetUsageSummary(c *gin.Context) {
	todayStart := h.resources.Today()

	results, err := h.resources.UsageSummary(c.Request.Context(), todayStart)
	if err != nil {
		response.Error(c, 500, "Failed to get group usage summary")
		return
	}

	response.Success(c, results)
}

// GetCapacitySummary returns aggregated capacity (concurrency/sessions/RPM) for all active groups.
// GET /api/v1/admin/groups/capacity-summary
func (h *GroupHandler) GetCapacitySummary(c *gin.Context) {
	results, err := h.resources.Capacity.GetAllGroupCapacity(c.Request.Context())
	if err != nil {
		response.Error(c, 500, "Failed to get group capacity summary")
		return
	}
	response.Success(c, results)
}

// GetGroupAPIKeys returns a page of API keys in the group.
// GET /api/v1/admin/groups/:id/api-keys
func (h *GroupHandler) GetGroupAPIKeys(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	page, pageSize := response.ParsePagination(c)

	keys, total, err := h.resources.Keys(c.Request.Context(), groupID, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Paginated(c, keys, total, page, pageSize)
}

// GetGroupRateMultipliers returns per-user multipliers for the group.
// GET /api/v1/admin/groups/:id/rate-multipliers
func (h *GroupHandler) GetGroupRateMultipliers(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	entries, err := h.resources.Rates.GetGroupRateMultipliers(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	if entries == nil {
		entries = []billing.UserGroupRateEntry{}
	}
	response.Success(c, entries)
}

// ClearGroupRateMultipliers removes every per-user multiplier for the group.
// DELETE /api/v1/admin/groups/:id/rate-multipliers
func (h *GroupHandler) ClearGroupRateMultipliers(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	if err := h.resources.Rates.ClearGroupRateMultipliers(c.Request.Context(), groupID); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Rate multipliers cleared successfully"})
}

// BatchSetGroupRateMultipliers saves per-user multipliers for the group.
// PUT /api/v1/admin/groups/:id/rate-multipliers
func (h *GroupHandler) BatchSetGroupRateMultipliers(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	var req BatchSetGroupRateMultipliersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.resources.Rates.BatchSetGroupRateMultipliers(c.Request.Context(), groupID, req.Entries); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Rate multipliers updated successfully"})
}

// BatchSetGroupRPMOverrides saves per-user RPM overrides for the group.
// PUT /api/v1/admin/groups/:id/rpm-overrides
func (h *GroupHandler) BatchSetGroupRPMOverrides(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	var req BatchSetGroupRPMOverridesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if err := h.resources.Rates.BatchSetGroupRPMOverrides(c.Request.Context(), groupID, req.Entries); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "RPM overrides updated successfully"})
}

// ClearGroupRPMOverrides removes every per-user RPM override for the group.
// DELETE /api/v1/admin/groups/:id/rpm-overrides
func (h *GroupHandler) ClearGroupRPMOverrides(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid group ID")
		return
	}

	if err := h.resources.Rates.ClearGroupRPMOverrides(c.Request.Context(), groupID); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "RPM overrides cleared successfully"})
}
