package httpapi

import (
	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// management_batch.go handles batch provider deletion, refresh, and error
// recovery with per-provider results.
// Administrative batches can succeed for some providers and fail for others, so
// the response includes counters and per-provider messages. Deletion receives
// normalized positive IDs; refresh and error recovery receive the supplied
// selection and delegate their concurrency and missing-record handling to
// ManagementBatch.

// BatchDelete normalizes the supplied IDs into positive unique numeric order
// before deletion. The batch use case coordinates selected parent trees and
// reports partial failures through successful HTTP batch results.
// POST /api/v1/admin/providers/batch-delete
func (h *ManagementHandler) BatchDelete(c *gin.Context) {
	var req ProviderBatchDeleteRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// DeleteNormalized requires positive unique IDs. Sort the admitted selection
	// here so dependent deletion results have one stable identifier order.
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

// BatchRefresh forwards a nonempty supplied ID list to managed credential
// refresh. The batch use case reports missing records, refresh failures and
// warnings alongside successes; this handler does not normalize duplicates.
// POST /api/v1/admin/providers/batch-refresh
func (h *ManagementHandler) BatchRefresh(c *gin.Context) {
	var req ProviderBatchRefreshRequest
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

// BatchClearError forwards a nonempty supplied ID list to managed error
// recovery. Per-provider failures remain in the result returned with HTTP
// success, while binding and batch-level errors end the request.
// POST /api/v1/admin/providers/batch-clear-error
func (h *ManagementHandler) BatchClearError(c *gin.Context) {
	var req ProviderBatchClearErrorRequest
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

// managementFailures copies core failure values into response maps for JSON
// encoding. Nil remains nil, while a nonnil empty input creates an empty slice
// so the response retains that distinction.
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

// managementWarnings copies core warning values into response maps for JSON
// encoding. Nil remains nil, while a nonnil empty input creates an empty slice
// so the response retains that distinction.
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
