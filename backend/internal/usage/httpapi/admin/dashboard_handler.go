package admin

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// dashboard_handler.go serves administrator dashboard statistics, filtered
// charts, and batch usage reports.
// Administrator dashboard charts parse optional scopes before using
// dashboard-owned result caches. A supplied request category takes precedence
// over legacy stream text for trend, model and group charts, while user
// breakdown retains both selectors as intersecting conditions.
//
// Date-only query ends advance to the next calendar midnight, including short
// or long DST days. Invalid chart dates select default bounds. The returned
// date labels subtract a fixed twenty-four hours from the end, so those
// labels can differ from the final included calendar date. Batch requests
// normalize IDs before cache lookup, and spending ranking applies its own
// result-limit cap.

// DashboardHandler serves administrator reports using a bound dashboard and
// range calendar.
type DashboardHandler struct {
	calendar         timezone.Calendar       // query parsing date calendar
	dashboardService *usage.DashboardService // dashboard report and cache service
	startTime        time.Time               // handler construction uptime anchor
}

// BatchUsersUsageRequest carries user identifiers submitted for administrator
// batch cost summaries.
type BatchUsersUsageRequest struct {
	UserIDs []int64 `json:"user_ids" binding:"required"` // submitted user identifier list
}

// BatchAPIKeysUsageRequest carries key identifiers submitted for
// administrator batch cost summaries.
type BatchAPIKeysUsageRequest struct {
	APIKeyIDs []int64 `json:"api_key_ids" binding:"required"` // submitted key identifier list
}

// parseOptionalBoolDashboardFilter returns an optional boolean for the
// trimmed query value at key. Empty values return nil; strconv.ParseBool
// spellings are accepted, and unsupported text returns a filter error.
func parseOptionalBoolDashboardFilter(c *gin.Context, key string) (*bool, error) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s value, use true or false", key)
	}
	return &value, nil
}

// NewDashboardHandler binds report queries and the range calendar, capturing
// the current instant for uptime. Returned uptime measures elapsed time from
// this construction call, so callers create the handler during application
// route wiring.
func NewDashboardHandler(dashboardService *usage.DashboardService, calendar timezone.Calendar) *DashboardHandler {
	return &DashboardHandler{
		calendar:         calendar,
		dashboardService: dashboardService,
		startTime:        time.Now(),
	}
}

// parseTimeRange returns chart bounds parsed in a valid user zone or the
// supplied server calendar. Missing or invalid start text selects midnight
// seven dates before now; missing or invalid end text selects tomorrow
// midnight.
//
// Date-only ends advance with AddDate, allowing the selected local day to
// span twenty-three or twenty-five hours. Explicit datetime bounds retain
// their parsed instant and supplied offset. The helper does not reject
// reversed bounds, leaving later query handling to the service and
// repository.
func parseTimeRange(c *gin.Context, calendar timezone.Calendar) (time.Time, time.Time) {
	userTZ := c.Query("timezone")
	now := calendar.NowInUserLocation(userTZ)
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")

	var startTime, endTime time.Time

	if startDate != "" {
		if t, _, err := calendar.ParseDateTimeInUserLocation(startDate, userTZ); err == nil {
			startTime = t
		} else {
			// An unusable supplied date retains the ordinary chart window, allowing the
			// endpoint to query its seven-date start under the selected calendar location.
			startTime = calendar.StartOfDayInUserLocation(now.AddDate(0, 0, -7), userTZ)
		}
	} else {
		startTime = calendar.StartOfDayInUserLocation(now.AddDate(0, 0, -7), userTZ)
	}

	if endDate != "" {
		if t, dateOnly, err := calendar.ParseDateTimeInUserLocation(endDate, userTZ); err == nil {
			if dateOnly {
				// Calendar-date expansion includes the selected local day even when a
				// DST transition changes its elapsed duration from twenty-four hours.
				t = t.AddDate(0, 0, 1)
			}
			endTime = t
		} else {
			// Unparseable end text selects tomorrow midnight so the ordinary chart
			// window still covers today according to the selected local-date rules.
			endTime = calendar.StartOfDayInUserLocation(now.AddDate(0, 0, 1), userTZ)
		}
	} else {
		endTime = calendar.StartOfDayInUserLocation(now.AddDate(0, 0, 1), userTZ)
	}

	return startTime, endTime
}

// GetStats returns administrator dashboard metrics and whole-second uptime
// since handler construction. The dashboard service determines cache and
// aggregation handling; a failed query becomes the handler's generic
// internal-error response.
func (h *DashboardHandler) GetStats(c *gin.Context) {
	stats, err := h.dashboardService.GetDashboardStats(c.Request.Context())
	if err != nil {
		response.Error(c, 500, "Failed to get dashboard statistics")
		return
	}

	// Report whole elapsed seconds since this handler was constructed.
	uptime := int64(time.Since(h.startTime).Seconds())

	response.Success(c, gin.H{
		// User counts.
		"total_users":     stats.TotalUsers,
		"today_new_users": stats.TodayNewUsers,
		"active_users":    stats.ActiveUsers,

		// API key counts.
		"total_api_keys":  stats.TotalAPIKeys,
		"active_api_keys": stats.ActiveAPIKeys,

		// Provider counts by status.
		"total_providers":     stats.TotalProviders,
		"normal_providers":    stats.NormalProviders,
		"error_providers":     stats.ErrorProviders,
		"ratelimit_providers": stats.RateLimitProviders,
		"overload_providers":  stats.OverloadProviders,

		// Retained usage totals.
		"total_requests":              stats.TotalRequests,
		"total_input_tokens":          stats.TotalInputTokens,
		"total_output_tokens":         stats.TotalOutputTokens,
		"total_cache_creation_tokens": stats.TotalCacheCreationTokens,
		"total_cache_read_tokens":     stats.TotalCacheReadTokens,
		"total_tokens":                stats.TotalTokens,
		"total_cost":                  stats.TotalCost,       // Cost before the user multiplier.
		"total_actual_cost":           stats.TotalActualCost, // Amount charged.

		// Today's usage totals.
		"today_requests":              stats.TodayRequests,
		"today_input_tokens":          stats.TodayInputTokens,
		"today_output_tokens":         stats.TodayOutputTokens,
		"today_cache_creation_tokens": stats.TodayCacheCreationTokens,
		"today_cache_read_tokens":     stats.TodayCacheReadTokens,
		"today_tokens":                stats.TodayTokens,
		"today_cost":                  stats.TodayCost,       // Today's cost before the user multiplier.
		"today_actual_cost":           stats.TodayActualCost, // Today's amount charged.

		// Request latency and uptime.
		"average_duration_ms": stats.AverageDurationMs,
		"uptime":              uptime,

		// Recent request and token rates.
		"rpm": stats.Rpm,
		"tpm": stats.Tpm,

		// Aggregation freshness and hourly activity.
		"hourly_active_users": stats.HourlyActiveUsers,
		"stats_updated_at":    stats.StatsUpdatedAt,
		"stats_stale":         stats.StatsStale,
	})
}

// GetRealtimeMetrics returns fixed zero values for the dashboard's
// placeholder realtime fields. It samples no live activity, so the response
// describes this endpoint's current placeholder state even while other
// reports contain measured usage.
func (h *DashboardHandler) GetRealtimeMetrics(c *gin.Context) {
	response.Success(c, gin.H{
		"active_requests":       0,
		"requests_per_minute":   0,
		"average_response_time": 0,
		"error_rate":            0.0,
	})
}

// GetUsageTrend returns cached trend buckets under the parsed dashboard
// bounds and optional selectors. Request type takes precedence over stream
// text, invalid ID text leaves selectors unset, and query failures return the
// generic trend error.
func (h *DashboardHandler) GetUsageTrend(c *gin.Context) {
	startTime, endTime := parseTimeRange(c, h.calendar)
	granularity := c.DefaultQuery("granularity", "day")

	var userID, apiKeyID, providerID, groupID, teamID int64
	var model string
	var requestType *int16
	var stream *bool
	var billingType *int8
	var nativeCompactionV2 *bool

	// Scalar ID parsing failures leave the corresponding selector at zero. This
	// chart path then queries the remaining scopes rather than rejecting the request.
	if userIDStr := c.Query("user_id"); userIDStr != "" {
		if id, err := strconv.ParseInt(userIDStr, 10, 64); err == nil {
			userID = id
		}
	}
	if apiKeyIDStr := c.Query("api_key_id"); apiKeyIDStr != "" {
		if id, err := strconv.ParseInt(apiKeyIDStr, 10, 64); err == nil {
			apiKeyID = id
		}
	}
	if providerIDStr := c.Query("provider_id"); providerIDStr != "" {
		if id, err := strconv.ParseInt(providerIDStr, 10, 64); err == nil {
			providerID = id
		}
	}
	if groupIDStr := c.Query("group_id"); groupIDStr != "" {
		if id, err := strconv.ParseInt(groupIDStr, 10, 64); err == nil {
			groupID = id
		}
	}
	if teamIDStr := c.Query("team_id"); teamIDStr != "" {
		if id, err := strconv.ParseInt(teamIDStr, 10, 64); err == nil {
			teamID = id
		}
	}
	if modelStr := c.Query("model"); modelStr != "" {
		model = modelStr
	}
	if requestTypeStr := strings.TrimSpace(c.Query("request_type")); requestTypeStr != "" {
		parsed, err := usage.ParseUsageRequestType(requestTypeStr)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		value := int16(parsed)
		requestType = &value
	} else if streamStr := c.Query("stream"); streamStr != "" {
		if streamVal, err := strconv.ParseBool(streamStr); err == nil {
			stream = &streamVal
		} else {
			response.BadRequest(c, "Invalid stream value, use true or false")
			return
		}
	}
	if billingTypeStr := c.Query("billing_type"); billingTypeStr != "" {
		if v, err := strconv.ParseInt(billingTypeStr, 10, 8); err == nil {
			bt := int8(v)
			billingType = &bt
		} else {
			response.BadRequest(c, "Invalid billing_type")
			return
		}
	}
	var boolErr error
	nativeCompactionV2, boolErr = parseOptionalBoolDashboardFilter(c, "native_compaction_v2")
	if boolErr != nil {
		response.BadRequest(c, boolErr.Error())
		return
	}

	trend, hit, err := h.getUsageTrendCached(c.Request.Context(), startTime, endTime, granularity, userID, apiKeyID, providerID, groupID, teamID, model, requestType, stream, billingType, nativeCompactionV2)
	if err != nil {
		response.Error(c, 500, "Failed to get usage trend")
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))

	response.Success(c, gin.H{
		"trend":      trend,
		"start_date": startTime.Format("2006-01-02"),
		// DST shifts can move this label away from the final included calendar date.
		"end_date":    endTime.Add(-24 * time.Hour).Format("2006-01-02"),
		"granularity": granularity,
	})
}

// GetModelStats returns cached model grouping under parsed bounds and
// selectors. It validates the selected requested, upstream or mapping source,
// gives request type precedence over stream text, and exposes cache-hit
// status.
func (h *DashboardHandler) GetModelStats(c *gin.Context) {
	startTime, endTime := parseTimeRange(c, h.calendar)

	var userID, apiKeyID, providerID, groupID, teamID int64
	modelSource := usage.ModelSourceRequested
	var requestType *int16
	var stream *bool
	var billingType *int8
	var nativeCompactionV2 *bool

	// Scalar ID parsing failures leave the corresponding selector at zero. This
	// chart path then queries the remaining scopes rather than rejecting the request.
	if userIDStr := c.Query("user_id"); userIDStr != "" {
		if id, err := strconv.ParseInt(userIDStr, 10, 64); err == nil {
			userID = id
		}
	}
	if apiKeyIDStr := c.Query("api_key_id"); apiKeyIDStr != "" {
		if id, err := strconv.ParseInt(apiKeyIDStr, 10, 64); err == nil {
			apiKeyID = id
		}
	}
	if providerIDStr := c.Query("provider_id"); providerIDStr != "" {
		if id, err := strconv.ParseInt(providerIDStr, 10, 64); err == nil {
			providerID = id
		}
	}
	if groupIDStr := c.Query("group_id"); groupIDStr != "" {
		if id, err := strconv.ParseInt(groupIDStr, 10, 64); err == nil {
			groupID = id
		}
	}
	if teamIDStr := c.Query("team_id"); teamIDStr != "" {
		if id, err := strconv.ParseInt(teamIDStr, 10, 64); err == nil {
			teamID = id
		}
	}
	if rawModelSource := strings.TrimSpace(c.Query("model_source")); rawModelSource != "" {
		if !usage.IsValidModelSource(rawModelSource) {
			response.BadRequest(c, "Invalid model_source, use requested/upstream/mapping")
			return
		}
		modelSource = rawModelSource
	}
	if requestTypeStr := strings.TrimSpace(c.Query("request_type")); requestTypeStr != "" {
		parsed, err := usage.ParseUsageRequestType(requestTypeStr)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		value := int16(parsed)
		requestType = &value
	} else if streamStr := c.Query("stream"); streamStr != "" {
		if streamVal, err := strconv.ParseBool(streamStr); err == nil {
			stream = &streamVal
		} else {
			response.BadRequest(c, "Invalid stream value, use true or false")
			return
		}
	}
	if billingTypeStr := c.Query("billing_type"); billingTypeStr != "" {
		if v, err := strconv.ParseInt(billingTypeStr, 10, 8); err == nil {
			bt := int8(v)
			billingType = &bt
		} else {
			response.BadRequest(c, "Invalid billing_type")
			return
		}
	}
	var boolErr error
	nativeCompactionV2, boolErr = parseOptionalBoolDashboardFilter(c, "native_compaction_v2")
	if boolErr != nil {
		response.BadRequest(c, boolErr.Error())
		return
	}

	stats, hit, err := h.getModelStatsCached(c.Request.Context(), startTime, endTime, userID, apiKeyID, providerID, groupID, teamID, modelSource, requestType, stream, billingType, nativeCompactionV2)
	if err != nil {
		response.Error(c, 500, "Failed to get model statistics")
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))

	response.Success(c, gin.H{
		"models":     stats,
		"start_date": startTime.Format("2006-01-02"),
		// DST shifts can move this label away from the final included calendar date.
		"end_date": endTime.Add(-24 * time.Hour).Format("2006-01-02"),
	})
}

// GetGroupStats returns cached group grouping under parsed bounds and
// optional selectors. Request type takes precedence over stream text, invalid
// ID text leaves selectors unset, and the response exposes cache-hit status.
func (h *DashboardHandler) GetGroupStats(c *gin.Context) {
	startTime, endTime := parseTimeRange(c, h.calendar)

	var userID, apiKeyID, providerID, groupID, teamID int64
	var requestType *int16
	var stream *bool
	var billingType *int8
	var nativeCompactionV2 *bool

	// Scalar ID parsing failures leave the corresponding selector at zero. This
	// chart path then queries the remaining scopes rather than rejecting the request.
	if userIDStr := c.Query("user_id"); userIDStr != "" {
		if id, err := strconv.ParseInt(userIDStr, 10, 64); err == nil {
			userID = id
		}
	}
	if apiKeyIDStr := c.Query("api_key_id"); apiKeyIDStr != "" {
		if id, err := strconv.ParseInt(apiKeyIDStr, 10, 64); err == nil {
			apiKeyID = id
		}
	}
	if providerIDStr := c.Query("provider_id"); providerIDStr != "" {
		if id, err := strconv.ParseInt(providerIDStr, 10, 64); err == nil {
			providerID = id
		}
	}
	if groupIDStr := c.Query("group_id"); groupIDStr != "" {
		if id, err := strconv.ParseInt(groupIDStr, 10, 64); err == nil {
			groupID = id
		}
	}
	if teamIDStr := c.Query("team_id"); teamIDStr != "" {
		if id, err := strconv.ParseInt(teamIDStr, 10, 64); err == nil {
			teamID = id
		}
	}
	if requestTypeStr := strings.TrimSpace(c.Query("request_type")); requestTypeStr != "" {
		parsed, err := usage.ParseUsageRequestType(requestTypeStr)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		value := int16(parsed)
		requestType = &value
	} else if streamStr := c.Query("stream"); streamStr != "" {
		if streamVal, err := strconv.ParseBool(streamStr); err == nil {
			stream = &streamVal
		} else {
			response.BadRequest(c, "Invalid stream value, use true or false")
			return
		}
	}
	if billingTypeStr := c.Query("billing_type"); billingTypeStr != "" {
		if v, err := strconv.ParseInt(billingTypeStr, 10, 8); err == nil {
			bt := int8(v)
			billingType = &bt
		} else {
			response.BadRequest(c, "Invalid billing_type")
			return
		}
	}
	var boolErr error
	nativeCompactionV2, boolErr = parseOptionalBoolDashboardFilter(c, "native_compaction_v2")
	if boolErr != nil {
		response.BadRequest(c, boolErr.Error())
		return
	}

	stats, hit, err := h.getGroupStatsCached(c.Request.Context(), startTime, endTime, userID, apiKeyID, providerID, groupID, teamID, requestType, stream, billingType, nativeCompactionV2)
	if err != nil {
		response.Error(c, 500, "Failed to get group statistics")
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))

	response.Success(c, gin.H{
		"groups":     stats,
		"start_date": startTime.Format("2006-01-02"),
		// DST shifts can move this label away from the final included calendar date.
		"end_date": endTime.Add(-24 * time.Hour).Format("2006-01-02"),
	})
}

// GetAPIKeyUsageTrend returns cached top-key buckets under the parsed chart
// bounds. An invalid or nonpositive limit selects five; granularity is passed
// to the service, and the response includes cache-hit status.
func (h *DashboardHandler) GetAPIKeyUsageTrend(c *gin.Context) {
	startTime, endTime := parseTimeRange(c, h.calendar)
	granularity := c.DefaultQuery("granularity", "day")
	limitStr := c.DefaultQuery("limit", "5")
	limit, err := strconv.Atoi(limitStr)
	// Invalid or nonpositive row counts use this endpoint's five-row default,
	// keeping the repository query within the endpoint's ordinary selected ranking size.
	if err != nil || limit <= 0 {
		limit = 5
	}

	trend, hit, err := h.getAPIKeyUsageTrendCached(c.Request.Context(), startTime, endTime, granularity, limit)
	if err != nil {
		response.Error(c, 500, "Failed to get API key usage trend")
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))

	response.Success(c, gin.H{
		"trend":      trend,
		"start_date": startTime.Format("2006-01-02"),
		// DST shifts can move this label away from the final included calendar date.
		"end_date":    endTime.Add(-24 * time.Hour).Format("2006-01-02"),
		"granularity": granularity,
	})
}

// GetUserUsageTrend returns cached top-payer buckets under the parsed chart
// bounds. An invalid or nonpositive limit selects twelve; granularity is
// passed to the service, and the response includes cache-hit status.
func (h *DashboardHandler) GetUserUsageTrend(c *gin.Context) {
	startTime, endTime := parseTimeRange(c, h.calendar)
	granularity := c.DefaultQuery("granularity", "day")
	limitStr := c.DefaultQuery("limit", "12")
	limit, err := strconv.Atoi(limitStr)
	// Invalid or nonpositive row counts use this endpoint's twelve-row default,
	// keeping the repository query within the endpoint's ordinary selected ranking size.
	if err != nil || limit <= 0 {
		limit = 12
	}

	trend, hit, err := h.getUserUsageTrendCached(c.Request.Context(), startTime, endTime, granularity, limit)
	if err != nil {
		response.Error(c, 500, "Failed to get user usage trend")
		return
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))

	response.Success(c, gin.H{
		"trend":      trend,
		"start_date": startTime.Format("2006-01-02"),
		// DST shifts can move this label away from the final included calendar date.
		"end_date":    endTime.Add(-24 * time.Hour).Format("2006-01-02"),
		"granularity": granularity,
	})
}

// parseRankingLimit trims ranking-limit text and returns a positive capped
// row limit. Empty, invalid or nonpositive values select twelve, while values
// greater than fifty return the cap of fifty.
func parseRankingLimit(raw string) int {
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit <= 0 {
		return 12
	}
	// Cap public handler input before it forms ranking cache keys.
	if limit > 50 {
		return 50
	}
	return limit
}

// GetUserSpendingRanking returns cached payer spending rows and full-range
// totals under a capped limit. Display dates use the stored RankingReport
// range, which can retain the first cache entry's time locations.
func (h *DashboardHandler) GetUserSpendingRanking(c *gin.Context) {
	startTime, endTime := parseTimeRange(c, h.calendar)
	limit := parseRankingLimit(c.DefaultQuery("limit", "12"))

	report, hit, err := h.dashboardService.GetUserSpendingRankingCached(c.Request.Context(), startTime, endTime, limit)
	ranking := report.Data
	startTime, endTime = report.Start, report.End
	if err != nil {
		response.Error(c, 500, "Failed to get user spending ranking")
		return
	}

	payload := gin.H{
		"ranking":           ranking.Ranking,
		"total_actual_cost": ranking.TotalActualCost,
		"total_requests":    ranking.TotalRequests,
		"total_tokens":      ranking.TotalTokens,
		"start_date":        startTime.Format("2006-01-02"),
		// DST shifts can move this label away from the final included calendar date.
		"end_date": endTime.Add(-24 * time.Hour).Format("2006-01-02"),
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, payload)
}

// GetBatchUsersUsage normalizes submitted IDs and returns cached batch user
// charges. An empty normalized set returns an empty stats object; user cache
// keys include the calendar date for their today totals.
func (h *DashboardHandler) GetBatchUsersUsage(c *gin.Context) {
	var req BatchUsersUsageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	userIDs := normalizeInt64IDList(req.UserIDs)
	if len(userIDs) == 0 {
		response.Success(c, gin.H{"stats": map[string]any{}})
		return
	}

	// Batch summaries contain today-specific values. Calendar.Today in the service
	// key separates this normalized ID set from its entry on the preceding date.
	stats, hit, err := h.dashboardService.GetBatchUsersUsageCached(c.Request.Context(), userIDs)
	if err != nil {
		response.Error(c, 500, "Failed to get user usage stats")
		return
	}

	payload := gin.H{"stats": stats}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, payload)
}

// GetBatchAPIKeysUsage normalizes submitted IDs and returns cached batch key
// charges. An empty normalized set returns an empty stats object; key-cache
// entries can reuse today totals across midnight until expiration.
func (h *DashboardHandler) GetBatchAPIKeysUsage(c *gin.Context) {
	var req BatchAPIKeysUsageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	apiKeyIDs := normalizeInt64IDList(req.APIKeyIDs)
	if len(apiKeyIDs) == 0 {
		response.Success(c, gin.H{"stats": map[string]any{}})
		return
	}

	stats, hit, err := h.dashboardService.GetBatchKeysUsageCached(c.Request.Context(), apiKeyIDs)
	if err != nil {
		response.Error(c, 500, "Failed to get API key usage stats")
		return
	}

	payload := gin.H{"stats": stats}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, payload)
}

// GetUserBreakdown returns payer rows within the requested dimensions and
// actor selectors. Request type and stream are both retained; limit defaults
// to fifty unless a supplied value is between one and two hundred.
func (h *DashboardHandler) GetUserBreakdown(c *gin.Context) {
	startTime, endTime := parseTimeRange(c, h.calendar)

	dim := usage.UserBreakdownDimension{}
	// This breakdown parser leaves malformed ID, stream and billing text unset.
	// Remaining dimensions still reach storage, while category and compaction
	// errors reject the request.
	if v := c.Query("group_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			dim.GroupID = id
		}
	}
	dim.Model = c.Query("model")
	rawModelSource := strings.TrimSpace(c.DefaultQuery("model_source", usage.ModelSourceRequested))
	if !usage.IsValidModelSource(rawModelSource) {
		response.BadRequest(c, "Invalid model_source, use requested/upstream/mapping")
		return
	}
	dim.ModelType = rawModelSource
	dim.Endpoint = c.Query("endpoint")
	dim.EndpointType = c.DefaultQuery("endpoint_type", "inbound")

	if v := c.Query("user_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			dim.UserID = id
		}
	}
	if v := c.Query("api_key_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			dim.APIKeyID = id
		}
	}
	if v := c.Query("provider_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			dim.ProviderID = id
		}
	}
	if v := strings.TrimSpace(c.Query("request_type")); v != "" {
		parsed, err := usage.ParseUsageRequestType(v)
		if err != nil {
			response.BadRequest(c, err.Error())
			return
		}
		rtVal := int16(parsed)
		dim.RequestType = &rtVal
	}
	if v := c.Query("stream"); v != "" {
		if s, err := strconv.ParseBool(v); err == nil {
			dim.Stream = &s
		}
	}
	nativeCompactionV2, err := parseOptionalBoolDashboardFilter(c, "native_compaction_v2")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	dim.NativeCompactionV2 = nativeCompactionV2
	if v := c.Query("billing_type"); v != "" {
		if bt, err := strconv.ParseInt(v, 10, 8); err == nil {
			btVal := int8(bt)
			dim.BillingType = &btVal
		}
	}

	// The repository chooses ORDER BY from a fixed allowlist. Forward trimmed text
	// so its actual_cost default also handles unrecognized names for this breakdown.
	dim.SortBy = strings.TrimSpace(c.Query("sort_by"))

	// Out-of-range or invalid limits use this endpoint's fifty-row default.
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	stats, err := h.dashboardService.GetUserBreakdownStats(
		c.Request.Context(), startTime, endTime, dim, limit,
	)
	if err != nil {
		response.Error(c, 500, "Failed to get user breakdown stats")
		return
	}

	response.Success(c, gin.H{
		"users":      stats,
		"start_date": startTime.Format("2006-01-02"),
		// DST shifts can move this label away from the final included calendar date.
		"end_date": endTime.Add(-24 * time.Hour).Format("2006-01-02"),
	})
}
