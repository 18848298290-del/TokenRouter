package httpapi

import (
	"errors"

	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// BatchUpdateCredentialsRequest represents batch credentials update request.
type BatchUpdateCredentialsRequest struct {
	ProviderIDs []int64 `json:"provider_ids" binding:"required,min=1"`
	Field       string  `json:"field" binding:"required,oneof=account_uuid org_uuid intercept_warmup_requests"`
	Value       any     `json:"value"`
}

// BatchUpdateCredentials validates field types and provider existence before returning per-item update results.
func (h *ManagementHandler) BatchUpdateCredentials(c *gin.Context) {
	var req BatchUpdateCredentialsRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := providercore.ValidateCredentialFieldValue(req.Field, req.Value); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	result, err := h.batch.PatchCredentials(c.Request.Context(), req.ProviderIDs, req.Field, req.Value)
	if err != nil {
		if missing, ok := errors.AsType[*providercore.ManagementProviderMissing](err); ok {
			response.Error(c, 404, missing.Error())
		} else {
			response.ErrorFrom(c, err)
		}
		return
	}
	items := make([]gin.H, 0, len(result.Results))
	for _, item := range result.Results {
		view := gin.H{"provider_id": item.ProviderID, "success": item.Success}
		if !item.Success {
			view["error"] = item.Error
		}
		items = append(items, view)
	}
	response.Success(c, gin.H{"success": result.Success, "failed": result.Failed, "success_ids": result.SuccessIDs, "failed_ids": result.FailedIDs, "results": items})
}
