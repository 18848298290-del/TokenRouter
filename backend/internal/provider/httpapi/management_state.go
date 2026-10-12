package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// The administrator scheduling switch, quota accounting and recoverable errors
// have separate maintenance operations. State responses reread or use the
// updated provider before runtime presentation, while proxy-fallback
// restoration returns a confirmation after the administration operation
// succeeds.

// SetSchedulableRequest carries the administrator scheduling switch, with
// omission binding as false.
type SetSchedulableRequest struct {
	// Schedulable writes the administrator switch as supplied. Omission and JSON
	// null bind to false here and therefore request disabling that switch.
	Schedulable bool `json:"schedulable"` // administrator scheduling switch
}

// SetSchedulable writes the scheduling switch from a strict JSON body and
// returns the updated view. An omitted schedulable field binds as false because
// this request uses a plain boolean.
// POST /api/v1/admin/providers/:id/schedulable
func (h *ManagementHandler) SetSchedulable(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	var req SetSchedulableRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	provider, err := h.adminService.SetProviderSchedulable(c.Request.Context(), providerID, req.Schedulable)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, h.presenter.Present(c.Request.Context(), provider))
}

// ResetQuota resets provider quota accounting and rereads the runtime view for
// the response. A failed reread does not roll back the completed reset; both
// stages retain their standard error mapping.
// POST /api/v1/admin/providers/:id/reset-quota
func (h *ManagementHandler) ResetQuota(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	if err := h.adminService.ResetProviderQuota(c.Request.Context(), providerID); err != nil {
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

// ClearError delegates provider error recovery to the managed credential
// service and presents its returned record. Decimal path parsing precedes
// maintenance, and use-case failures prevent runtime response presentation.
// POST /api/v1/admin/providers/:id/clear-error
func (h *ManagementHandler) ClearError(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	provider, err := h.managed.ClearError(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, h.presenter.Present(c.Request.Context(), provider))
}

// RevertProxyFallback asks administration to restore the recorded pre-fallback
// proxy selection. Malformed decimal IDs return 400, and the fixed confirmation
// is sent after the use case reports success.
// POST /api/v1/admin/providers/:id/revert-proxy-fallback
func (h *ManagementHandler) RevertProxyFallback(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}
	if err := h.adminService.RevertProviderProxyFallback(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "reverted"})
}
