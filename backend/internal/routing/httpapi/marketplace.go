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

// MarketplaceStatsReader reads public homepage statistics.
type MarketplaceStatsReader interface {
	PublicStats(context.Context) (dto.ModelMarketplaceStats, error)
}

type MarketplaceHandler struct {
	marketplace *routing.Marketplace
	stats       MarketplaceStatsReader
}

func NewMarketplaceHandler(marketplace *routing.Marketplace, stats MarketplaceStatsReader) *MarketplaceHandler {
	return &MarketplaceHandler{marketplace: marketplace, stats: stats}
}

// ListPublic includes capacity by default; include_capacity=false skips that query.
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

// StatsPublic returns public token and user counts.
func (h *MarketplaceHandler) StatsPublic(c *gin.Context) {
	stats, err := h.stats.PublicStats(c.Request.Context())
	if err != nil {
		httpx.ErrorFrom(c, err)
		return
	}
	httpx.Success(c, stats)
}

// RegisterPublicMarketplaceRoutes registers public model and statistics queries.
func RegisterPublicMarketplaceRoutes(v1 *gin.RouterGroup, endpoint *MarketplaceHandler) {
	marketplace := v1.Group("/marketplace")
	marketplace.GET("/models", endpoint.ListPublic)
	marketplace.GET("/stats", endpoint.StatsPublic)
}
