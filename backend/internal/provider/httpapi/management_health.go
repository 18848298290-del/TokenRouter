package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// management_health.go exposes provider health recovery and temporary-pause
// inspection through administrator endpoints.
// Provider recovery updates runtime conditions independently of the
// administrator scheduling switch. The recovery endpoint rereads the provider
// for presentation after completing recovery; temporary-block display treats a
// missing or expired state as inactive using the HTTP host's current clock.

// RecoverState requests unified recovery with token invalidation and then
// rereads the management view. A missing recovery service returns 503; read
// failure after recovery leaves the completed maintenance in place.
// POST /api/v1/admin/providers/:id/recover-state
func (h *ManagementHandler) RecoverState(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	if h.recovery == nil {
		response.Error(c, http.StatusServiceUnavailable, "Rate limit service unavailable")
		return
	}

	if _, err := h.recovery.RecoverProviderState(c.Request.Context(), providerID, providercore.ProviderRecoveryOptions{
		InvalidateToken: true,
	}); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	provider, err := h.adminService.GetProvider(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, h.presenter.Present(c.Request.Context(), provider))
}

// ClearRateLimit clears the recoverable provider rate-limit state and returns a
// reread runtime view. A later provider read failure returns an error after
// clearing has already completed in the use case.
// POST /api/v1/admin/providers/:id/clear-rate-limit
func (h *ManagementHandler) ClearRateLimit(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	err = h.recovery.ClearRateLimit(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	provider, err := h.adminService.GetProvider(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, h.presenter.Present(c.Request.Context(), provider))
}

// GetTempUnschedulable returns an active temporary-block state when its Unix
// deadline is in the future. Missing and expired states return active=false,
// and this display check does not clear the stored block.
// GET /api/v1/admin/providers/:id/temp-unschedulable
func (h *ManagementHandler) GetTempUnschedulable(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	state, err := h.recovery.GetTempUnschedStatus(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	if state == nil || state.UntilUnix <= time.Now().Unix() {
		response.Success(c, gin.H{"active": false})
		return
	}

	response.Success(c, gin.H{
		"active": true,
		"state":  state,
	})
}

// ClearTempUnschedulable delegates clearing the provider's temporary scheduling
// block to recovery. Malformed decimal IDs return 400, use-case errors retain
// their mapping, and success returns the fixed confirmation message.
// DELETE /api/v1/admin/providers/:id/temp-unschedulable
func (h *ManagementHandler) ClearTempUnschedulable(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	if err := h.recovery.ClearTempUnschedulable(c.Request.Context(), providerID); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Temp unschedulable cleared successfully"})
}
