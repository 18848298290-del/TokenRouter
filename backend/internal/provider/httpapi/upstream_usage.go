package httpapi

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// upstream_usage.go validates API-key usage query selections and serializes
// results with individual failures.
// API-key upstream usage is returned as normalized display data, with batch
// successes and errors encoded separately. This handler requires positive
// provider IDs, limits the supplied batch to one hundred entries before
// querying, and keeps domain error reason and status fields in each serialized
// item failure.

// UpstreamUsageQueries supplies normalized administrator usage results for
// individual and batch requests.
type UpstreamUsageQueries interface {
	// QueryProvider queries normalized usage for the supplied provider and request
	// context. The implementation owns upstream eligibility, identity checks and
	// query lifetime, and returns domain errors for the handler to serialize.
	QueryProvider(context.Context, int64) (*provider.UpstreamUsageQueryResult, error)
	// QueryBatch returns separate maps of successful normalized results and
	// provider-specific errors. A batch-level error rejects the request; item
	// errors remain visible alongside other providers' successful results.
	QueryBatch(context.Context, []int64) (map[int64]*provider.UpstreamUsageQueryResult, map[int64]error, error)
}

// UpstreamUsageHandler binds usage selections and serializes normalized results
// and per-provider errors.
type UpstreamUsageHandler struct {
	queries UpstreamUsageQueries          // optional normalized usage queries
	alerts  *provider.BalanceAlertMonitor // persisted balance reminder status
}

// UpstreamUsageBatchRequest carries the required provider selection for a
// manual batch usage query.
type UpstreamUsageBatchRequest struct {
	// ProviderIDs is checked as supplied: the handler admits one to one hundred
	// positive entries and forwards duplicate IDs to QueryBatch unchanged.
	ProviderIDs []int64 `json:"provider_ids" binding:"required"` // supplied usage query identifiers
}

// NewUpstreamUsageHandler borrows the usage query implementation for manual
// administrator requests. A nil implementation remains nil and mounted query
// methods return the domain service-unavailable error when reached.
func NewUpstreamUsageHandler(queries UpstreamUsageQueries, alerts *provider.BalanceAlertMonitor) *UpstreamUsageHandler {
	return &UpstreamUsageHandler{queries: queries, alerts: alerts}
}

// QueryUpstreamUsage validates a positive path identifier and returns its
// normalized API-key usage. Missing query dependencies return the unavailable
// domain error; provider eligibility and upstream execution belong to the query
// implementation.
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

// QueryBatchUpstreamUsage admits one to one hundred positive IDs and returns
// successes plus structured item errors. Duplicate IDs are passed through as
// supplied; batch-level query errors end the whole HTTP request.
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

// BalanceAlertStatus returns persisted reminder state after current-identity validation.
// Invalid identifiers and missing dependencies return domain errors. Reading this
// endpoint performs no upstream query and never sends an email notification.
func (h *UpstreamUsageHandler) BalanceAlertStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, provider.ErrUpstreamUsageProviderInvalid)
		return
	}
	if h.alerts == nil {
		response.ErrorFrom(c, provider.ErrUpstreamUsageUnavailable)
		return
	}
	state, err := h.alerts.Status(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}
