package httpapi

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// management_create.go handles strict batch creation requests through the
// administrator response-idempotency executor.
// Batch creation accepts a strict list of complete provider requests and
// returns an outcome for each supplied item. Deprecated Extra keys are
// discarded before response idempotency records the payload, while the batch
// use case owns privacy and post-creation task dispatch.

// BatchCreate strictly binds at least one creation request and applies
// administrator response idempotency. ManagementBatch owns item creation and
// follow-up work; the HTTP response contains names, successful IDs and failure
// messages.
func (h *ManagementHandler) BatchCreate(c *gin.Context) {
	var req ProviderBatchCreateRequest
	if err := response.BindJSONStrict(c, &req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	// Strip retired settings before the executor fingerprints the entire batch,
	// so ignored client fields cannot distinguish otherwise equivalent creation requests.
	for i := range req.Providers {
		provider.DiscardDeprecatedProviderExtra(req.Providers[i].Extra)
	}
	h.ExecuteAdminIdempotentJSON(c, "admin.providers.batch_create", req, h.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		inputs := make([]provider.CreateProviderInput, 0, len(req.Providers))
		for _, item := range req.Providers {
			inputs = append(inputs, provider.CreateProviderInput{Name: item.Name, Notes: item.Notes, Platform: item.Platform, Type: item.Type, Credentials: item.Credentials, Extra: item.Extra, ProxyID: item.ProxyID, Concurrency: item.Concurrency, Priority: item.Priority, RateMultiplier: item.RateMultiplier, LoadFactor: item.LoadFactor, GroupIDs: item.GroupIDs, ExpiresAt: item.ExpiresAt, AutoPauseOnExpired: item.AutoPauseOnExpired})
		}
		result, err := h.batch.Create(ctx, inputs)
		if err != nil {
			return nil, err
		}
		items := make([]gin.H, 0, len(result.Results))
		for _, item := range result.Results {
			value := gin.H{"name": item.Name, "success": item.Success}
			if item.Success {
				value["id"] = item.ID
			} else {
				value["error"] = item.Error
			}
			items = append(items, value)
		}
		return gin.H{"success": result.Success, "failed": result.Failed, "results": items}, nil
	})
}
