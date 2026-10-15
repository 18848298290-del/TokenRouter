package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/ops"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// ops_snapshot_v2_handler.go serves cached dashboard snapshots with
// conditional ETag responses.
// Dashboard snapshot responses pair a payload with a JSON-derived ETag. A
// matching request condition produces a bodyless 304 only when the service
// reports a cache hit; a newly loaded response is sent even when its generated
// tag matches the condition.
//
// The handler checks monitoring and parses the common time window, trimmed
// platform, and positive optional group before asking the service for a cached
// snapshot. Trend width follows the same span selector as separate dashboard
// charts. Successful hashing writes ETag and replaces Vary with If-None-Match.
// The hit/miss diagnostic header is added after conditional handling, so the
// early 304 path omits that header while full responses include it.

// GetDashboardSnapshotV2 returns the combined dashboard snapshot or a bodyless
// conditional response. It validates monitoring, a time range defaulting to one
// hour, and a positive optional group ID before querying the service.
//
// The ETag hashes the returned payload's JSON representation. If hashing
// succeeds, a cache hit with a matching If-None-Match condition returns 304
// before the cache diagnostic header is set. A miss bypasses conditional
// handling, as does an absent tag. Full-response handling reports the service's
// cache-hit flag through X-Snapshot-Cache before passing the payload to the
// JSON writer.
func (h *OpsHandler) GetDashboardSnapshotV2(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	startTime, endTime, err := parseOpsTimeRange(c, "1h")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	filter := &ops.OpsDashboardFilter{
		StartTime: startTime,
		EndTime:   endTime,
		Platform:  strings.TrimSpace(c.Query("platform")),
	}
	if v := strings.TrimSpace(c.Query("group_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid group_id")
			return
		}
		filter.GroupID = &id
	}
	bucketSeconds := pickThroughputBucketSeconds(endTime.Sub(startTime))

	resp, hit, err := h.opsService.CachedDashboardSnapshot(c.Request.Context(), filter, bucketSeconds)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Hash the returned payload before conditional handling, then set validator
	// headers before choosing the early 304 or full-response path.
	etag := response.BuildETagFromAny(resp)
	if etag != "" {
		c.Header("ETag", etag)
		c.Header("Vary", "If-None-Match")
		if hit && response.IfNoneMatchMatched(c.GetHeader("If-None-Match"), etag) {
			c.Status(http.StatusNotModified)
			return
		}
	}
	if hit {
		c.Header("X-Snapshot-Cache", "hit")
	} else {
		c.Header("X-Snapshot-Cache", "miss")
	}
	response.Success(c, resp)
}
