package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

var providerTodayStatsBatchCache = response.NewSnapshotCache(30 * time.Second)

// OAuthUsageHandler serves active and passive usage queries, batch queries, statistics, and ETag responses.
type OAuthUsageHandler struct {
	core  *provider.OAuthUsageService
	stats *provider.LocalUsageStatistics
}

// BatchTodayStatsRequest identifies providers for today's statistics.
type BatchTodayStatsRequest struct {
	ProviderIDs []int64 `json:"provider_ids" binding:"required"`
}
type BatchUsageRequest struct {
	ProviderIDs []int64 `json:"provider_ids" binding:"required"`
	Force       bool    `json:"force"`
}

func NewOAuthUsageHandler(core *provider.OAuthUsageService, stats *provider.LocalUsageStatistics) *OAuthUsageHandler {
	return &OAuthUsageHandler{core, stats}
}

// GetUsage queries a provider's usage.
// GET /api/v1/admin/providers/:id/usage?source=passive|active&force=true
func (h *OAuthUsageHandler) GetUsage(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	source := c.DefaultQuery("source", "active")
	force := c.Query("force") == "true"

	var usage *provider.UsageInfo
	if source == "passive" {
		usage, err = h.core.GetPassiveUsage(c.Request.Context(), providerID)
	} else {
		usage, err = h.core.GetUsage(c.Request.Context(), providerID, force)
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, usage)
}

// GetTodayStats handles getting provider today statistics
// GET /api/v1/admin/providers/:id/today-stats
func (h *OAuthUsageHandler) GetTodayStats(c *gin.Context) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	stats, err := h.stats.GetTodayStats(c.Request.Context(), providerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, stats)
}

// GetBatchTodayStats returns today's statistics for multiple providers.
// POST /api/v1/admin/providers/today-stats/batch
func (h *OAuthUsageHandler) GetBatchTodayStats(c *gin.Context) {
	var req BatchTodayStatsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	providerIDs := response.NormalizeInt64IDList(req.ProviderIDs)
	if len(providerIDs) == 0 {
		response.Success(c, gin.H{"stats": map[string]any{}})
		return
	}

	cacheKey := BuildProviderTodayStatsBatchCacheKey(providerIDs)
	if cached, ok := providerTodayStatsBatchCache.Get(cacheKey); ok {
		if cached.ETag != "" {
			c.Header("ETag", cached.ETag)
			c.Header("Vary", "If-None-Match")
			if response.IfNoneMatchMatched(c.GetHeader("If-None-Match"), cached.ETag) {
				c.Status(http.StatusNotModified)
				return
			}
		}
		c.Header("X-Snapshot-Cache", "hit")
		response.Success(c, cached.Payload)
		return
	}

	stats, err := h.stats.GetTodayStatsBatch(c.Request.Context(), providerIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	payload := gin.H{"stats": stats}
	cached := providerTodayStatsBatchCache.Set(cacheKey, payload)
	if cached.ETag != "" {
		c.Header("ETag", cached.ETag)
		c.Header("Vary", "If-None-Match")
	}
	c.Header("X-Snapshot-Cache", "miss")
	response.Success(c, payload)
}

// GetBatchUsage returns current usage for multiple providers.
// POST /api/v1/admin/providers/usage/batch
func (h *OAuthUsageHandler) GetBatchUsage(c *gin.Context) {
	var req BatchUsageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	providerIDs := response.NormalizeInt64IDList(req.ProviderIDs)
	if len(providerIDs) == 0 {
		response.Success(c, gin.H{
			"usage":  map[string]any{},
			"errors": map[string]string{},
		})
		return
	}

	usageByProvider, errorsByProvider, err := h.core.GetUsageBatch(c.Request.Context(), providerIDs, req.Force)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"usage":  usageByProvider,
		"errors": errorsByProvider,
	})
}

func BuildProviderTodayStatsBatchCacheKey(providerIDs []int64) string {
	if len(providerIDs) == 0 {
		return "providers_today_stats_empty"
	}
	var b strings.Builder
	b.Grow(len(providerIDs) * 6)
	_, _ = b.WriteString("providers_today_stats:")
	for i, id := range providerIDs {
		if i > 0 {
			_ = b.WriteByte(',')
		}
		_, _ = b.WriteString(strconv.FormatInt(id, 10))
	}
	return b.String()
}
