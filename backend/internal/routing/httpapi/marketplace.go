package httpapi

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Public model browsing can omit current capacity aggregation while retaining
// model, price, and availability data returned by routing. The HTTP parameter
// selects that work before the marketplace query; homepage totals use a
// separate statistics reader and the same success envelope.

// MarketplaceStatsReader supplies token and user totals for public homepage
// responses.
type MarketplaceStatsReader interface {
	// PublicStats returns the current public token and user counters for the
	// supplied request context. Reader failures become the endpoint's standard
	// error response, so callers return the error with the failed read.
	PublicStats(context.Context) (dto.ModelMarketplaceStats, error)
}

// MarketplaceHandler serves public model browsing and aggregate homepage
// statistics.
type MarketplaceHandler struct {
	marketplace *routing.Marketplace   // public group model reader
	stats       MarketplaceStatsReader // public homepage totals reader
}

// NewMarketplaceHandler binds the marketplace and public statistics reader
// for HTTP queries.
func NewMarketplaceHandler(marketplace *routing.Marketplace, stats MarketplaceStatsReader) *MarketplaceHandler {
	return &MarketplaceHandler{marketplace: marketplace, stats: stats}
}

// ListPublic returns publicly visible model groups and their display data.
// include_capacity defaults to true; false skips capacity aggregation, and an
// invalid boolean returns INVALID_INCLUDE_CAPACITY before reading marketplace
// groups.
func (h *MarketplaceHandler) ListPublic(c *gin.Context) {
	includeCapacity, err := strconv.ParseBool(c.DefaultQuery("include_capacity", "true"))
	if err != nil {
		httpx.ErrorFrom(c, apperror.BadRequest("INVALID_INCLUDE_CAPACITY", "include_capacity must be a boolean"))
		return
	}
	groups, err := h.marketplace.ListPublic(c.Request.Context(), routing.MarketplaceListOptions{IncludeCapacity: includeCapacity})
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, dto.ModelMarketplaceGroupsFromRouting(groups))
}

// StatsPublic returns homepage token and user counts through the configured
// reader. It passes the HTTP request context to that reader and exposes read
// failures through the standard error response.
func (h *MarketplaceHandler) StatsPublic(c *gin.Context) {
	stats, err := h.stats.PublicStats(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, stats)
}

// RegisterPublicMarketplaceRoutes binds model browsing and homepage totals
// under /marketplace. The caller supplies the router group used for public
// access; this registration adds the two GET handlers directly.
func RegisterPublicMarketplaceRoutes(v1 *gin.RouterGroup, endpoint *MarketplaceHandler) {
	marketplace := v1.Group("/marketplace")
	marketplace.GET("/models", endpoint.ListPublic)
	marketplace.GET("/stats", endpoint.StatsPublic)
}
