package httpapi

import (
	"errors"

	"github.com/gin-gonic/gin"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// management_credential_fields.go binds batch credential-field edits and
// formats their per-provider update results.
// Credential-field maintenance validates each selected provider before applying
// per-provider patches. The HTTP layer permits the declared string or boolean
// fields and reports partial write failures as item results, while a missing
// prevalidation record ends the request with 404.

// BatchUpdateCredentialsRequest selects providers and one supported credential
// field for value replacement.
type BatchUpdateCredentialsRequest struct {
	// credential patch target identifiers
	ProviderIDs []int64 `json:"provider_ids" binding:"required,min=1"`
	// selected credential field name
	Field string `json:"field" binding:"required,oneof=account_uuid org_uuid intercept_warmup_requests"`
	// Value follows Field's string or boolean type. Null clears the two UUID
	// fields, while intercept_warmup_requests requires a boolean value.
	Value any `json:"value"` // replacement credential field value
}

// BatchUpdateCredentials strictly binds a supported credential field and
// validates its value before batch maintenance. Missing providers during
// prevalidation return 404; per-item write failures remain in the successful
// HTTP result.
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
