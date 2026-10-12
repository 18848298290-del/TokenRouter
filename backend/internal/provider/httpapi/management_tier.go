package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// BatchRefreshTierRequest represents batch tier refresh request.
type BatchRefreshTierRequest struct {
	ProviderIDs []int64 `json:"provider_ids"`
}

// RefreshTier returns tier data after provider lookup; lookup failures return 404.
func (h *ManagementHandler) RefreshTier(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}
	v, err := h.adminService.GetProvider(c.Request.Context(), id)
	if err != nil {
		response.NotFound(c, "Provider not found")
		return
	}
	result, err := h.tier.Refresh(c.Request.Context(), v)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	extra := result.StorageInfo
	response.Success(c, gin.H{"tier_id": result.TierID, "storage_info": extra, "drive_storage_limit": extra["drive_storage_limit"], "drive_storage_usage": extra["drive_storage_usage"], "updated_at": extra["drive_tier_updated_at"]})
}

// BatchRefreshTier treats malformed or empty input as a request for all Google One providers.
func (h *ManagementHandler) BatchRefreshTier(c *gin.Context) {
	var req BatchRefreshTierRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		req = BatchRefreshTierRequest{}
	}
	result, err := h.tier.Batch(c.Request.Context(), req.ProviderIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	var failures []gin.H
	for _, v := range result.Errors {
		failures = append(failures, gin.H{"provider_id": v.ProviderID, "error": v.Error})
	}
	response.Success(c, gin.H{"total": result.Total, "success": result.Success, "failed": result.Failed, "errors": failures})
}
