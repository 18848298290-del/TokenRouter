package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/ops"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// Historical dashboard charts share a requested time window and optional
// platform and group filters. Most endpoints use the common Ops time parser
// with a one-hour default, while model token statistics accept a finite preset
// list and derive their own UTC range ending at the current time.
//
// Throughput and error trends select one of three bucket widths from the
// requested span. Histogram requests validate five ordered millisecond cutoffs
// before storage. Token rankings choose either a leading-model limit or
// pagination, and the presence of top_n together with either pagination
// parameter is rejected before those values are parsed. These HTTP rules
// determine the query inputs; the Ops service chooses raw or aggregated storage
// where supported.

// GetDashboardOverview returns window counts, rates, and available system
// observations after the monitoring check. It defaults the time range to one
// hour, trims platform, and requires a positive supplied group ID before
// querying the service.
// GET /api/v1/admin/ops/dashboard/overview
func (h *OpsHandler) GetDashboardOverview(c *gin.Context) {
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

	data, err := h.opsService.GetDashboardOverview(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

// GetDashboardThroughputTrend returns request and token rates in buckets
// selected from the requested window length. It checks monitoring first, uses a
// one-hour default, and forwards trimmed platform and positive optional group
// filters.
// GET /api/v1/admin/ops/dashboard/throughput-trend
func (h *OpsHandler) GetDashboardThroughputTrend(c *gin.Context) {
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
	data, err := h.opsService.GetThroughputTrend(c.Request.Context(), filter, bucketSeconds)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

// GetDashboardLatencyHistogram returns duration counts from saved usage rows
// using validated millisecond cutoffs. Monitoring and time/group validation precede
// cutoff parsing; an omitted cutoff list uses the shared defaults, and invalid
// values return bad request.
// GET /api/v1/admin/ops/dashboard/latency-histogram
func (h *OpsHandler) GetDashboardLatencyHistogram(c *gin.Context) {
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

	bucketBoundariesMS, err := parseOpsLatencyBucketBoundaries(c.Query("bucket_boundaries_ms"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	data, err := h.opsService.GetLatencyHistogram(c.Request.Context(), filter, bucketBoundariesMS)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

// parseOpsLatencyBucketBoundaries parses a comma-separated decimal millisecond
// list and delegates ordered-cutoff validation to Ops. Blank input returns
// copied defaults; supplied input must contain five increasing positive values
// no larger than one day.
func parseOpsLatencyBucketBoundaries(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ops.DefaultOpsLatencyBucketBoundariesMS(), nil
	}

	parts := strings.Split(raw, ",")
	boundaries := make([]int64, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid bucket_boundaries_ms")
		}
		boundaries = append(boundaries, value)
	}

	normalized, err := ops.NormalizeOpsLatencyBucketBoundariesMS(boundaries)
	if err != nil {
		return nil, fmt.Errorf("invalid bucket_boundaries_ms: %w", err)
	}
	return normalized, nil
}

// GetDashboardErrorTrend returns windowed final-client, SLA, business-limit,
// and upstream error series. It uses the same span-based bucket selection as
// throughput and checks monitoring and positive optional group IDs before
// requesting the trend.
// GET /api/v1/admin/ops/dashboard/error-trend
func (h *OpsHandler) GetDashboardErrorTrend(c *gin.Context) {
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
	data, err := h.opsService.GetErrorTrend(c.Request.Context(), filter, bucketSeconds)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

// GetDashboardErrorDistribution returns the service's error classification and
// status distributions for the selected window. It checks monitoring, defaults
// to one hour, and rejects nonpositive group filters before the repository
// query.
// GET /api/v1/admin/ops/dashboard/error-distribution
func (h *OpsHandler) GetDashboardErrorDistribution(c *gin.Context) {
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

	data, err := h.opsService.GetErrorDistribution(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

// GetDashboardTokenStats returns model-grouped usage statistics with either a
// leading-model limit or pagination. It checks monitoring before parsing the
// preset range and ranking parameters, and maps parsing errors to bad request.
// GET /api/v1/admin/ops/dashboard/token-stats
func (h *OpsHandler) GetDashboardTokenStats(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	filter, err := parseOpsTokenStatsFilter(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	data, err := h.opsService.GetTokenStats(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, data)
}

// parseOpsTokenStatsFilter builds a UTC model-statistics window ending at the
// current time from a supported preset. A nil context is invalid, blank
// time_range selects thirty days, and platform is trimmed while a supplied
// group ID must be positive.
//
// A nonblank top_n selects the leading-model mode and conflicts with either
// nonblank pagination parameter, even if that parameter would parse as a
// default. Otherwise pagination defaults to page one and size twenty. TopN and
// page size each accept one through one hundred; explicit start_time and
// end_time parameters do not participate in this parser.
func parseOpsTokenStatsFilter(c *gin.Context) (*ops.OpsTokenStatsFilter, error) {
	if c == nil {
		return nil, fmt.Errorf("invalid request")
	}

	timeRange := strings.TrimSpace(c.Query("time_range"))
	if timeRange == "" {
		timeRange = "30d"
	}
	dur, ok := parseOpsTokenStatsDuration(timeRange)
	if !ok {
		return nil, fmt.Errorf("invalid time_range")
	}
	// Derive both endpoints from one UTC timestamp so the preset duration stays
	// fixed even when parameter parsing takes time.
	end := time.Now().UTC()
	start := end.Add(-dur)

	filter := &ops.OpsTokenStatsFilter{
		TimeRange: timeRange,
		StartTime: start,
		EndTime:   end,
		Platform:  strings.TrimSpace(c.Query("platform")),
	}

	if v := strings.TrimSpace(c.Query("group_id")); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid group_id")
		}
		filter.GroupID = &id
	}

	topNRaw := strings.TrimSpace(c.Query("top_n"))
	pageRaw := strings.TrimSpace(c.Query("page"))
	pageSizeRaw := strings.TrimSpace(c.Query("page_size"))
	// Use parameter presence to choose one ranking mode, preventing a top limit
	// from being combined with offset-based pagination.
	if topNRaw != "" && (pageRaw != "" || pageSizeRaw != "") {
		return nil, fmt.Errorf("invalid query: top_n cannot be used with page/page_size")
	}

	if topNRaw != "" {
		topN, err := strconv.Atoi(topNRaw)
		// Bound the leading-model response to one hundred grouped rows.
		if err != nil || topN < 1 || topN > 100 {
			return nil, fmt.Errorf("invalid top_n")
		}
		filter.TopN = topN
		return filter, nil
	}

	filter.Page = 1
	filter.PageSize = 20
	if pageRaw != "" {
		page, err := strconv.Atoi(pageRaw)
		if err != nil || page < 1 {
			return nil, fmt.Errorf("invalid page")
		}
		filter.Page = page
	}
	if pageSizeRaw != "" {
		pageSize, err := strconv.Atoi(pageSizeRaw)
		// Bound paginated responses to one hundred grouped model rows.
		if err != nil || pageSize < 1 || pageSize > 100 {
			return nil, fmt.Errorf("invalid page_size")
		}
		filter.PageSize = pageSize
	}
	return filter, nil
}

// parseOpsTokenStatsDuration recognizes the trimmed, case-sensitive
// model-statistics presets thirty minutes, one hour, one day, fifteen days, and
// thirty days. Unknown input returns zero and false; day presets use elapsed
// twenty-four-hour durations.
func parseOpsTokenStatsDuration(v string) (time.Duration, bool) {
	// Day presets represent elapsed hours, independent of calendar timezones.
	switch strings.TrimSpace(v) {
	case "30m":
		return 30 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "1d":
		return 24 * time.Hour, true
	case "15d":
		return 15 * 24 * time.Hour, true
	case "30d":
		return 30 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

// pickThroughputBucketSeconds returns sixty-second buckets through two hours,
// three hundred seconds through one day, and one hour afterward. Nonpositive
// windows select the shortest bucket; caller validation determines whether such
// a range is queried.
func pickThroughputBucketSeconds(window time.Duration) int {
	// Use coarser buckets for longer windows to bound the number of returned trend
	// points.
	switch {
	case window <= 2*time.Hour:
		return 60
	case window <= 24*time.Hour:
		return 300
	default:
		return 3600
	}
}
