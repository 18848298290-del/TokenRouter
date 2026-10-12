package httpapi

import (
	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// BatchDelete deletes providers with bounded concurrency and returns ordered per-provider results.
// POST /api/v1/admin/providers/batch-delete
func (h *ManagementHandler) BatchDelete(c *gin.Context) {
	var req struct {
		ProviderIDs []int64 `json:"provider_ids"`
	}
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	providerIDs := response.NormalizeInt64IDList(req.ProviderIDs)
	if len(providerIDs) == 0 {
		response.BadRequest(c, "provider_ids is required")
		return
	}

	result, err := h.batch.DeleteNormalized(c.Request.Context(), providerIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"total": result.Total, "success": result.Success, "failed": result.Failed, "errors": managementFailures(result.Errors), "success_ids": result.SuccessIDs, "failed_ids": result.FailedIDs})
}

// BatchRefresh handles batch refreshing provider credentials
// POST /api/v1/admin/providers/batch-refresh
func (h *ManagementHandler) BatchRefresh(c *gin.Context) {
	var req struct {
		ProviderIDs []int64 `json:"provider_ids"`
	}
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if len(req.ProviderIDs) == 0 {
		response.BadRequest(c, "provider_ids is required")
		return
	}

	result, err := h.batch.Refresh(c.Request.Context(), req.ProviderIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"total": result.Total, "success": result.Success, "failed": result.Failed, "errors": managementFailures(result.Errors), "warnings": managementWarnings(result.Warnings)})
}

// BatchClearError handles batch clearing provider errors
// POST /api/v1/admin/providers/batch-clear-error
func (h *ManagementHandler) BatchClearError(c *gin.Context) {
	var req struct {
		ProviderIDs []int64 `json:"provider_ids"`
	}
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if len(req.ProviderIDs) == 0 {
		response.BadRequest(c, "provider_ids is required")
		return
	}

	result, err := h.batch.ClearError(c.Request.Context(), req.ProviderIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"total": result.Total, "success": result.Success, "failed": result.Failed, "errors": managementFailures(result.Errors)})
}

// managementFailures converts failures to HTTP fields and preserves nil versus empty slices.
func managementFailures(values []providercore.ManagementBatchFailure) []gin.H {
	if values == nil {
		return nil
	}
	out := make([]gin.H, 0, len(values))
	for _, v := range values {
		out = append(out, gin.H{"provider_id": v.ProviderID, "error": v.Error})
	}
	return out
}

func managementWarnings(values []providercore.ManagementBatchWarning) []gin.H {
	if values == nil {
		return nil
	}
	out := make([]gin.H, 0, len(values))
	for _, v := range values {
		out = append(out, gin.H{"provider_id": v.ProviderID, "warning": v.Warning})
	}
	return out
}
