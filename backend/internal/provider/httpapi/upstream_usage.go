package httpapi

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// UpstreamUsageQueries provides administrator usage query operations.
type UpstreamUsageQueries interface {
	QueryProvider(context.Context, int64) (*provider.UpstreamUsageQueryResult, error)
	QueryBatch(context.Context, []int64) (map[int64]*provider.UpstreamUsageQueryResult, map[int64]error, error)
}
type UpstreamUsageHandler struct{ queries UpstreamUsageQueries }

// UpstreamUsageBatchRequest identifies API key providers for an upstream usage query.
type UpstreamUsageBatchRequest struct {
	ProviderIDs []int64 `json:"provider_ids" binding:"required"`
}

func NewUpstreamUsageHandler(queries UpstreamUsageQueries) *UpstreamUsageHandler {
	return &UpstreamUsageHandler{queries: queries}
}

// QueryUpstreamUsage fetches an API key provider's current upstream usage.
// POST /api/v1/admin/providers/:id/upstream-usage/query
func (h *UpstreamUsageHandler) QueryUpstreamUsage(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || providerID <= 0 {
		response.ErrorFrom(c, provider.ErrUpstreamUsageProviderInvalid)
		return
	}
	if h.queries == nil {
		response.ErrorFrom(c, provider.ErrUpstreamUsageUnavailable)
		return
	}
	result, err := h.queries.QueryProvider(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// QueryBatchUpstreamUsage fetches current upstream usage for multiple API key providers.
// POST /api/v1/admin/providers/upstream-usage/query/batch
func (h *UpstreamUsageHandler) QueryBatchUpstreamUsage(c *gin.Context) {
	var req UpstreamUsageBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorFrom(c, provider.ErrUpstreamUsageBatchInvalid)
		return
	}
	if len(req.ProviderIDs) == 0 {
		response.ErrorFrom(c, provider.ErrUpstreamUsageBatchInvalid)
		return
	}
	if len(req.ProviderIDs) > 100 {
		response.ErrorFrom(c, provider.ErrUpstreamUsageBatchTooLarge)
		return
	}
	for _, providerID := range req.ProviderIDs {
		if providerID <= 0 {
			response.ErrorFrom(c, provider.ErrUpstreamUsageBatchInvalid)
			return
		}
	}
	if h.queries == nil {
		response.ErrorFrom(c, provider.ErrUpstreamUsageUnavailable)
		return
	}
	usageByProvider, errorsByProvider, err := h.queries.QueryBatch(c.Request.Context(), req.ProviderIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	serializedErrors := make(map[string]gin.H, len(errorsByProvider))
	for providerID, queryErr := range errorsByProvider {
		status := infraerrors.FromError(queryErr)
		serializedErrors[strconv.FormatInt(providerID, 10)] = gin.H{
			"code":    status.Reason,
			"reason":  status.Reason,
			"message": status.Message,
			"status":  status.Code,
		}
	}
	response.Success(c, gin.H{
		"usage":  usageByProvider,
		"errors": serializedErrors,
	})
}
