package httpapi

import (
	"strconv"

	"github.com/gin-gonic/gin"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// management_tier.go handles single-provider and batch Google One tier refresh
// requests.
// Google One tier maintenance can target one loaded provider or a batch
// selection. Batch JSON binding failures deliberately become an empty
// selection, which the tier use case interprets as its Google One population
// query; item failures remain in the successful batch response.

// BatchRefreshTierRequest optionally selects provider IDs, with an empty list
// requesting Google One candidates.
type BatchRefreshTierRequest struct {
	// ProviderIDs selects explicit tier targets when nonempty. Empty or omitted
	// input asks TierManagement to query its bounded Google One population.
	ProviderIDs []int64 `json:"provider_ids"` // optional tier target identifiers
}

// RefreshTier loads a provider and returns tier plus Drive storage display
// fields after maintenance. Lookup failure maps to 404; the core handles
// eligibility and identity-checked configuration writes before producing its
// result.
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

// BatchRefreshTier refreshes supplied provider IDs or the core's Google One
// selection. Malformed JSON and an empty selection both choose that population
// query, and individual errors remain alongside successful counts.
func (h *ManagementHandler) BatchRefreshTier(c *gin.Context) {
	var req BatchRefreshTierRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		// Binding failure chooses the same population query as omitted IDs. Reset
		// partially decoded fields so malformed input cannot leave a partial selection.
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
