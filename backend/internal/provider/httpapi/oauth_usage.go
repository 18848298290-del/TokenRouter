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

// oauth_usage.go serves active and passive OAuth usage and caches batch local
// statistics responses.
// OAuth usage queries can use stored passive observations or active upstream
// maintenance, while today statistics come from local usage data. Batch
// today-statistics responses reuse a process-wide thirty-second snapshot keyed
// by the normalized provider selection and supply an ETag for conditional
// requests.
//
// Normalization sorts and removes invalid or duplicate IDs before cache lookup.
// A cache hit can return 304 without another statistics query; a miss queries
// and replaces the snapshot. Usage batches return result and error maps
// directly. The cache key carries provider IDs, so all handler
// instances in this process share a selection's snapshot until its expiration.

// providerTodayStatsBatchCache retains normalized provider-selection statistics
// for thirty seconds across handler instances and pairs cached payloads with
// conditional response tags.
var providerTodayStatsBatchCache = response.NewSnapshotCache(30 * time.Second)

// OAuthUsageHandler binds provider usage maintenance and local statistics to
// administrator HTTP responses.
type OAuthUsageHandler struct {
	core  *provider.OAuthUsageService    // borrowed OAuth usage maintenance
	stats *provider.LocalUsageStatistics // borrowed local usage statistics
}

// BatchTodayStatsRequest identifies the providers selected for local
// calendar-day statistics.
type BatchTodayStatsRequest struct {
	// ProviderIDs is normalized into positive unique sorted IDs before use. An
	// empty normalized selection returns an empty object without querying local
	// statistics.
	ProviderIDs []int64 `json:"provider_ids" binding:"required"` // requested provider selection identifiers
}

// BatchUsageRequest selects providers and optional forced refresh for active
// usage observations.
type BatchUsageRequest struct {
	// ProviderIDs is normalized into positive unique sorted IDs before active
	// usage dispatch. No remaining IDs returns two empty usage and error objects.
	ProviderIDs []int64 `json:"provider_ids" binding:"required"` // requested provider selection identifiers
	// Force is forwarded to OAuthUsageService for platform-specific refresh
	// selection. Omission and JSON null retain false, leaving its ordinary cache
	// and throttling rules available.
	Force bool `json:"force"` // force active usage refresh
}

// NewOAuthUsageHandler borrows OAuth usage and local statistics services for
// mounted requests. Their owner manages query shutdown, while the package-level
// statistics snapshot cache is shared across handler instances.
func NewOAuthUsageHandler(core *provider.OAuthUsageService, stats *provider.LocalUsageStatistics) *OAuthUsageHandler {
	return &OAuthUsageHandler{core, stats}
}

// GetUsage returns passive observations when source equals passive exactly, and
// otherwise requests active usage. Force is enabled only by the exact query
// text true; the use case owns refresh and eligibility.
// GET /api/v1/admin/providers/:id/usage?source=passive|active&force=true
func (h *OAuthUsageHandler) GetUsage(c *gin.Context) {
	h.getUsage(c, c.Query("source") == "passive")
}

// GetPassiveUsage returns stored Anthropic observations through a dedicated query route.
// Query parameters cannot select active maintenance on this endpoint. Unsupported
// provider types retain the passive service's existing error response.
func (h *OAuthUsageHandler) GetPassiveUsage(c *gin.Context) {
	h.getUsage(c, true)
}

// getUsage reads the selected provider through the caller's chosen observation mode.
// Passive mode uses stored samples; active mode honors the existing force query
// switch and can update provider health or quota observations.
func (h *OAuthUsageHandler) getUsage(c *gin.Context, passive bool) {
	providerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid provider ID")
		return
	}

	force := c.Query("force") == "true"

	var usage *provider.UsageInfo
	if passive {
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

// GetTodayStats parses a decimal provider identifier and returns the local
// service's calendar-day statistics. Parse failures return 400, and service
// failures retain the common domain error mapping before response encoding.
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

// GetBatchTodayStats normalizes selected IDs and serves their local statistics
// from a shared snapshot when available. A normalized empty selection returns
// an empty stats object. Cache hits expose their stored ETag, allowing matching
// requests to return 304 before another local statistics query.
//
// A miss loads statistics with the HTTP context and stores a thirty-second
// snapshot before sending success. The returned payload and tag follow
// SnapshotCache's supported copying rules; the selection key is process-wide
// and contains no administrator identity. X-Snapshot-Cache marks successful hit
// or miss responses, while matching 304 responses return earlier.
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
	// Normalized IDs make equivalent selections share a snapshot. A hit supplies
	// the payload and matching tag together, allowing conditional response before another query.
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

// GetBatchUsage normalizes selected provider IDs before active usage
// maintenance and returns result and error maps. An empty normalized selection
// succeeds with two empty objects; Force is forwarded to the usage use case.
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

// BuildProviderTodayStatsBatchCacheKey joins decimal IDs in supplied order into
// the selection cache key. Empty input uses a distinct key; callers normalize
// and sort selections beforehand when equivalent inputs should share cache
// entries.
func BuildProviderTodayStatsBatchCacheKey(providerIDs []int64) string {
	if len(providerIDs) == 0 {
		return "providers_today_stats_empty"
	}
	var b strings.Builder
	b.Grow(len(providerIDs) * 6)
	// strings.Builder append methods return nil errors. Discard their error
	// values throughout this sequence because every append contributes directly to the selection key.
	_, _ = b.WriteString("providers_today_stats:")
	for i, id := range providerIDs {
		if i > 0 {
			_ = b.WriteByte(',')
		}
		_, _ = b.WriteString(strconv.FormatInt(id, 10))
	}
	return b.String()
}
