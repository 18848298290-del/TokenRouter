package httpapi

import (
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// UpdateDefaultPricing 只在管理员明确更新时重新加载目录，普通查询不触发同步。
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#gateway_default_pricing
func (h *PricingHandler) UpdateDefaultPricing(c *gin.Context) {
	if h.catalog == nil || h.catalog.Update == nil {
		httpx.Error(c, http.StatusServiceUnavailable, "pricing catalog updater is unavailable")
		return
	}
	if err := h.catalog.Update(); err != nil {
		slog.Warn("failed to update pricing catalog", "error", err)
		httpx.Error(c, http.StatusBadGateway, "failed to update pricing catalog")
		return
	}
	httpx.Success(c, gin.H{"updated": true})
}

// ListDefaultPricing 查询网关内存中已加载的默认价格。
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#gateway_default_pricing
func (h *PricingHandler) ListDefaultPricing(c *gin.Context) {
	if h.catalog.Query != nil {
		page, size := httpx.ParsePagination(c)
		offset := int(^uint(0) >> 1)
		if page-1 <= offset/size {
			offset = (page - 1) * size
		}
		result := h.catalog.Query(routing.DefaultPricingQuery{Search: c.Query("search"), Platform: c.Query("platform"), Mode: c.Query("billing_mode"), Offset: offset, Limit: size})
		httpx.Success(c, gin.H{"items": result.Prices, "total": result.Total, "last_updated": result.UpdatedAt, "platforms": result.Platforms, "version": result.Version, "last_error": result.LastError})
		return
	}
	snapshot := h.catalog.Snapshot()
	platformSet := make(map[string]struct{})
	for _, row := range snapshot.Prices {
		platformSet[row.Platform] = struct{}{}
	}
	platforms := make([]string, 0, len(platformSet))
	for platform := range platformSet {
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	page, pageSize := httpx.ParsePagination(c)
	platform := strings.ToLower(strings.TrimSpace(c.Query("platform")))
	search := strings.ToLower(strings.TrimSpace(c.Query("search")))
	mode := c.Query("billing_mode")
	rows := make([]pricing.DefaultModelPrice, 0)
	for _, row := range snapshot.Prices {
		if platform != "" && row.Platform != platform {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(row.Model), search) {
			continue
		}
		if mode != "" && row.BillingMode != mode {
			continue
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Model != rows[j].Model {
			return rows[i].Model < rows[j].Model
		}
		return rows[i].Platform < rows[j].Platform
	})
	start := len(rows)
	if page-1 <= len(rows)/pageSize {
		start = (page - 1) * pageSize
	}
	end := min(start+pageSize, len(rows))
	httpx.Success(c, gin.H{"items": rows[start:end], "total": len(rows), "last_updated": snapshot.UpdatedAt, "platforms": platforms, "version": snapshot.Version, "last_error": snapshot.LastError})
}
