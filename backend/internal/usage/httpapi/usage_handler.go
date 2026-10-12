package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/usage"
	"github.com/TokenFlux/TokenRouter/internal/usage/httpapi/dto"
	"github.com/TokenFlux/TokenRouter/internal/usage/httpapi/ports"
)

const (
	defaultAPIKeyDailyUsageDays = 30
	maxAPIKeyDailyUsageDays     = 90
)

type userUsageFilters struct {
	Filters   usage.UsageLogFilters
	StartTime time.Time
	EndTime   time.Time
}

type userModelStat struct {
	Model               string  `json:"model"`
	Requests            int64   `json:"requests"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	Cost                float64 `json:"cost"`
	ActualCost          float64 `json:"actual_cost"`
}

type userGroupStat struct {
	GroupID     int64   `json:"group_id"`
	GroupName   string  `json:"group_name"`
	Requests    int64   `json:"requests"`
	TotalTokens int64   `json:"total_tokens"`
	Cost        float64 `json:"cost"`
	ActualCost  float64 `json:"actual_cost"`
}

type usageRankingItemResponse struct {
	Rank                int      `json:"rank"`
	UserID              int64    `json:"user_id"`
	DisplayName         string   `json:"display_name"`
	AvatarURL           string   `json:"avatar_url"`
	Requests            *int64   `json:"requests,omitempty"`
	InputTokens         *int64   `json:"input_tokens,omitempty"`
	OutputTokens        *int64   `json:"output_tokens,omitempty"`
	CacheCreationTokens *int64   `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     *int64   `json:"cache_read_tokens,omitempty"`
	TotalTokens         *int64   `json:"total_tokens,omitempty"`
	ActualCost          *float64 `json:"actual_cost,omitempty"`
}

type usageRankingResponse struct {
	Ranking         []usageRankingItemResponse `json:"ranking"`
	TotalRequests   *int64                     `json:"total_requests,omitempty"`
	TotalTokens     *int64                     `json:"total_tokens,omitempty"`
	TotalActualCost *float64                   `json:"total_actual_cost,omitempty"`
	SortBy          string                     `json:"sort_by"`
	ShowTotalTokens bool                       `json:"show_total_tokens"`
	ShowRequests    bool                       `json:"show_requests"`
	ShowActualCost  bool                       `json:"show_actual_cost"`
	StartDate       string                     `json:"start_date"`
	EndDate         string                     `json:"end_date"`
	Limit           int                        `json:"limit"`
}

// UsageHandler serves user usage queries.
type UsageHandler struct {
	calendar       timezone.Calendar
	usageService   *usage.UsageService
	apiKeyService  ports.KeyReader
	opsService     ports.UserErrors
	settingService ports.Settings
}

// BatchAPIKeysUsageRequest selects API keys for a batch usage query.
type BatchAPIKeysUsageRequest struct {
	APIKeyIDs []int64 `json:"api_key_ids" binding:"required"`
}

func usageRankingInt64Ptr(value int64) *int64 {
	return &value
}

func usageRankingFloat64Ptr(value float64) *float64 {
	return &value
}

// projectUsageRankingResponse exposes ranking fields allowed by public visibility settings.
func projectUsageRankingResponse(ranking *usage.UsageRankingResponse, settings usage.UsageRankingSettings) usageRankingResponse {
	result := usageRankingResponse{
		Ranking:         make([]usageRankingItemResponse, 0, len(ranking.Ranking)),
		SortBy:          string(settings.SortBy),
		ShowTotalTokens: settings.ShowTotalTokens,
		ShowRequests:    settings.ShowRequests,
		ShowActualCost:  settings.ShowActualCost,
	}
	if settings.ShowRequests {
		result.TotalRequests = usageRankingInt64Ptr(ranking.TotalRequests)
	}
	if settings.ShowTotalTokens {
		result.TotalTokens = usageRankingInt64Ptr(ranking.TotalTokens)
	}
	if settings.ShowActualCost {
		result.TotalActualCost = usageRankingFloat64Ptr(ranking.TotalActualCost)
	}
	for _, row := range ranking.Ranking {
		item := usageRankingItemResponse{
			Rank:        row.Rank,
			UserID:      row.UserID,
			DisplayName: row.DisplayName,
			AvatarURL:   row.AvatarURL,
		}
		if settings.ShowRequests {
			item.Requests = usageRankingInt64Ptr(row.Requests)
		}
		if settings.ShowTotalTokens {
			item.InputTokens = usageRankingInt64Ptr(row.InputTokens)
			item.OutputTokens = usageRankingInt64Ptr(row.OutputTokens)
			item.CacheCreationTokens = usageRankingInt64Ptr(row.CacheCreationTokens)
			item.CacheReadTokens = usageRankingInt64Ptr(row.CacheReadTokens)
			item.TotalTokens = usageRankingInt64Ptr(row.TotalTokens)
		}
		if settings.ShowActualCost {
			item.ActualCost = usageRankingFloat64Ptr(row.ActualCost)
		}
		result.Ranking = append(result.Ranking, item)
	}
	return result
}

// NewUsageHandler creates the user usage handler.
func NewUsageHandler(
	usageService *usage.UsageService,
	apiKeyService ports.KeyReader,
	opsService ports.UserErrors,
	settingService ports.Settings,
	calendar timezone.Calendar,
) *UsageHandler {
	return &UsageHandler{
		calendar:       calendar,
		usageService:   usageService,
		apiKeyService:  apiKeyService,
		opsService:     opsService,
		settingService: settingService,
	}
}

func (h *UsageHandler) parseUserUsageFilters(c *gin.Context, requireRange bool) (*userUsageFilters, bool) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return nil, false
	}

	var apiKeyID int64
	if apiKeyIDStr := strings.TrimSpace(c.Query("api_key_id")); apiKeyIDStr != "" {
		id, err := strconv.ParseInt(apiKeyIDStr, 10, 64)
		if err != nil {
			response.BadRequest(c, "Invalid api_key_id")
			return nil, false
		}
		if h.apiKeyService == nil {
			response.InternalError(c, "API key service not available")
			return nil, false
		}
		_, err = h.apiKeyService.GetByID(c.Request.Context(), id)
		if err != nil {
			response.ErrorFrom(c, err)
			return nil, false
		}
		// The usage scope enforces visibility; owners may filter keys belonging to other team members.
		apiKeyID = id
	}

	var groupID int64
	if groupIDStr := strings.TrimSpace(c.Query("group_id")); groupIDStr != "" {
		id, err := strconv.ParseInt(groupIDStr, 10, 64)
		if err != nil {
			response.BadRequest(c, "Invalid group_id")
			return nil, false
		}
		groupID = id
	}

	var requestType *int16
	var stream *bool
	var nativeCompactionV2 *bool
	if requestTypeStr := strings.TrimSpace(c.Query("request_type")); requestTypeStr != "" {
		parsed, err := usage.ParseUsageRequestType(requestTypeStr)
		if err != nil {
			response.BadRequest(c, err.Error())
			return nil, false
		}
		value := int16(parsed)
		requestType = &value
	} else if streamStr := strings.TrimSpace(c.Query("stream")); streamStr != "" {
		val, err := strconv.ParseBool(streamStr)
		if err != nil {
			response.BadRequest(c, "Invalid stream value, use true or false")
			return nil, false
		}
		stream = &val
	}
	if raw := strings.TrimSpace(c.Query("native_compaction_v2")); raw != "" {
		val, err := strconv.ParseBool(raw)
		if err != nil {
			response.BadRequest(c, "Invalid native_compaction_v2 value, use true or false")
			return nil, false
		}
		nativeCompactionV2 = &val
	}

	var billingType *int8
	if billingTypeStr := strings.TrimSpace(c.Query("billing_type")); billingTypeStr != "" {
		val, err := strconv.ParseInt(billingTypeStr, 10, 8)
		if err != nil {
			response.BadRequest(c, "Invalid billing_type")
			return nil, false
		}
		bt := int8(val)
		billingType = &bt
	}

	billingMode := strings.TrimSpace(c.Query("billing_mode"))
	if billingMode != "" && !pricing.BillingMode(billingMode).IsValidUsageFilter() {
		response.BadRequest(c, "Invalid billing_mode")
		return nil, false
	}

	userTZ := c.Query("timezone")
	now := h.calendar.NowInUserLocation(userTZ)
	var startTime, endTime time.Time
	var startPtr, endPtr *time.Time
	startDateStr := strings.TrimSpace(c.Query("start_date"))
	endDateStr := strings.TrimSpace(c.Query("end_date"))

	if startDateStr != "" {
		t, _, err := h.calendar.ParseDateTimeInUserLocation(startDateStr, userTZ)
		if err != nil {
			response.BadRequest(c, "Invalid start_date format, use YYYY-MM-DD or YYYY-MM-DDTHH:mm:ss")
			return nil, false
		}
		startTime = t
		startPtr = &startTime
	}
	if endDateStr != "" {
		t, dateOnly, err := h.calendar.ParseDateTimeInUserLocation(endDateStr, userTZ)
		if err != nil {
			response.BadRequest(c, "Invalid end_date format, use YYYY-MM-DD or YYYY-MM-DDTHH:mm:ss")
			return nil, false
		}
		if dateOnly {
			// Use the next calendar midnight as the exclusive end for date-only inputs.
			t = t.AddDate(0, 0, 1)
		}
		endTime = t
		endPtr = &endTime
	}

	if requireRange && strings.TrimSpace(c.Query("request_id")) == "" {
		if startPtr == nil {
			switch c.DefaultQuery("period", "") {
			case "today":
				startTime = h.calendar.StartOfDayInUserLocation(now, userTZ)
			case "week":
				startTime = now.AddDate(0, 0, -7)
			case "month":
				startTime = now.AddDate(0, -1, 0)
			default:
				startTime = h.calendar.StartOfDayInUserLocation(now.AddDate(0, 0, -7), userTZ)
			}
			startPtr = &startTime
		}
		if endPtr == nil {
			if strings.TrimSpace(c.Query("period")) != "" {
				endTime = now
			} else {
				endTime = h.calendar.StartOfDayInUserLocation(now.AddDate(0, 0, 1), userTZ)
			}
			endPtr = &endTime
		}
	}

	if strings.TrimSpace(c.Query("request_id")) != "" {
		if startPtr == nil {
			startTime = time.Unix(0, 0).UTC()
		} else {
			startTime = *startPtr
		}
		if endPtr == nil {
			endTime = time.Now().UTC()
		} else {
			endTime = *endPtr
		}
	}
	return &userUsageFilters{
		Filters: usage.UsageLogFilters{
			RequestID:          strings.TrimSpace(c.Query("request_id")),
			UserID:             subject.UserID,
			APIKeyID:           apiKeyID,
			GroupID:            groupID,
			IncludeOwnedTeam:   true,
			Model:              strings.TrimSpace(c.Query("model")),
			ModelFilterSource:  usage.ModelSourceRequested,
			RequestType:        requestType,
			Stream:             stream,
			NativeCompactionV2: nativeCompactionV2,
			BillingType:        billingType,
			BillingMode:        billingMode,
			StartTime:          startPtr,
			EndTime:            endPtr,
		},
		StartTime: startTime,
		EndTime:   endTime,
	}, true
}

// List returns a page of usage records visible to the current user.
// GET /api/v1/usage
func (h *UsageHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	parsed, ok := h.parseUserUsageFilters(c, false)
	if !ok {
		return
	}

	params := pagination.PaginationParams{
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.DefaultQuery("sort_by", "created_at"),
		SortOrder: c.DefaultQuery("sort_order", "desc"),
	}

	records, result, err := h.usageService.ListWithFilters(c.Request.Context(), params, parsed.Filters)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]dto.UsageLog, 0, len(records))
	for i := range records {
		out = append(out, *dto.FromUsage(&records[i], locale.FromContext(c.Request.Context())))
	}
	response.Paginated(c, out, result.Total, page, pageSize)
}

// ListErrors returns redacted failed requests belonging to the current user.
// GET /api/v1/usage/errors
func (h *UsageHandler) ListErrors(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	// Enforce visibility on the server even if the frontend hides the entry point.
	if h.settingService == nil || !h.settingService.IsUserErrorViewAllowed(c.Request.Context()) {
		response.Forbidden(c, "Error requests view is disabled")
		return
	}
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}

	page, pageSize := response.ParsePagination(c)
	if pageSize > 100 {
		pageSize = 100
	}

	filter := &ops.OpsErrorLogFilter{Page: page, PageSize: pageSize, RequestID: strings.TrimSpace(c.Query("request_id"))}

	// Use the same half-open [start, end) date range as the usage list.
	userTZ := c.Query("timezone")
	if startDateStr := c.Query("start_date"); startDateStr != "" {
		t, err := h.calendar.ParseInUserLocation("2006-01-02", startDateStr, userTZ)
		if err != nil {
			response.BadRequest(c, "Invalid start_date format, use YYYY-MM-DD")
			return
		}
		filter.StartTime = &t
	}
	if endDateStr := c.Query("end_date"); endDateStr != "" {
		t, err := h.calendar.ParseInUserLocation("2006-01-02", endDateStr, userTZ)
		if err != nil {
			response.BadRequest(c, "Invalid end_date format, use YYYY-MM-DD")
			return
		}
		t = t.AddDate(0, 0, 1)
		filter.EndTime = &t
	}

	filter.Model = strings.TrimSpace(c.Query("model"))

	if k := strings.TrimSpace(c.Query("api_key_id")); k != "" {
		n, err := strconv.ParseInt(k, 10, 64)
		if err != nil || n < 0 {
			response.BadRequest(c, "Invalid api_key_id")
			return
		}
		if n > 0 {
			filter.APIKeyID = &n
		}
	}

	if sc := strings.TrimSpace(c.Query("status_code")); sc != "" {
		n, err := strconv.Atoi(sc)
		if err != nil || n < 0 {
			response.BadRequest(c, "Invalid status_code")
			return
		}
		filter.StatusCodes = []int{n}
	}

	if cat := strings.TrimSpace(c.Query("category")); cat != "" {
		phases, types := ops.CategoryToFilter(cat)
		filter.ErrorPhasesAny = phases
		filter.ErrorTypesAny = types
	}

	// The repository validates columns and normalizes direction, defaulting invalid sort values to created_at DESC.
	filter.SetSort(c.Query("sort_by"), c.Query("sort_order"))

	result, err := h.opsService.ListUserErrorRequests(c.Request.Context(), subject.UserID, filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, result.Items, int64(result.Total), result.Page, result.PageSize)
}

// GetErrorDetail returns one redacted failed request belonging to the current user.
// GET /api/v1/usage/errors/:id
func (h *UsageHandler) GetErrorDetail(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h.settingService == nil || !h.settingService.IsUserErrorViewAllowed(c.Request.Context()) {
		response.Forbidden(c, "Error requests view is disabled")
		return
	}
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid id")
		return
	}
	detail, err := h.opsService.GetUserErrorRequestDetail(c.Request.Context(), subject.UserID, id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, detail)
}

// GetByID returns a usage record belonging to the current requester.
// GET /api/v1/usage/:id
func (h *UsageHandler) GetByID(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	usageID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid usage ID")
		return
	}

	record, err := h.usageService.GetByID(c.Request.Context(), usageID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// Record details require the actual requester so team members cannot read another member's request metadata.
	if record.UserID != subject.UserID {
		response.Forbidden(c, "Not authorized to access this record")
		return
	}

	response.Success(c, dto.FromUsage(record, locale.FromContext(c.Request.Context())))
}

// Ranking returns usage rankings for the selected time range.
// GET /api/v1/usage/ranking
func (h *UsageHandler) Ranking(c *gin.Context) {
	if _, ok := authctx.GetAuthSubjectFromContext(c); !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	rankingSettings, err := h.settingService.GetUsageRankingSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !rankingSettings.Enabled {
		response.Forbidden(c, "Usage ranking is currently disabled")
		return
	}

	userTZ := c.Query("timezone")
	now := h.calendar.NowInUserLocation(userTZ)
	startTime, endTime, err := parseUsageRankingTimeRange(c, now, userTZ, h.calendar)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	ranking, err := h.usageService.GetUsageRanking(c.Request.Context(), startTime, endTime, rankingSettings.Limit, rankingSettings.SortBy)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	projected := projectUsageRankingResponse(ranking, rankingSettings)
	projected.StartDate = startTime.Format("2006-01-02")
	projected.EndDate = usageRankingDisplayEndDate(endTime)
	projected.Limit = rankingSettings.Limit

	// The response includes the applied range formatted in the user's timezone.
	response.Success(c, projected)
}

// parseUsageRankingTimeRange defaults to today in the user's timezone when no dates are supplied.
func parseUsageRankingTimeRange(c *gin.Context, now time.Time, userTZ string, calendar timezone.Calendar) (time.Time, time.Time, error) {
	startDateStr := c.Query("start_date")
	endDateStr := c.Query("end_date")
	defaultStart := calendar.StartOfDayInUserLocation(now, userTZ)
	defaultEnd := defaultStart.AddDate(0, 0, 1)
	if startDateStr == "" && endDateStr == "" {
		return defaultStart, defaultEnd, nil
	}

	startTime := defaultStart
	endTime := defaultEnd
	if startDateStr != "" {
		parsed, _, err := calendar.ParseDateTimeInUserLocation(startDateStr, userTZ)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("invalid start_date format, use YYYY-MM-DD or YYYY-MM-DDTHH:mm:ss")
		}
		startTime = parsed
	}
	if endDateStr != "" {
		parsed, dateOnly, err := calendar.ParseDateTimeInUserLocation(endDateStr, userTZ)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("invalid end_date format, use YYYY-MM-DD or YYYY-MM-DDTHH:mm:ss")
		}
		if dateOnly {
			// Include the selected end date by using the next midnight with created_at < endTime.
			parsed = parsed.AddDate(0, 0, 1)
		}
		endTime = parsed
	}
	if !endTime.After(startTime) {
		return time.Time{}, time.Time{}, errors.New("end_date must be later than start_date")
	}
	return startTime, endTime, nil
}

// usageRankingDisplayEndDate converts an exclusive end to the last included display date.
func usageRankingDisplayEndDate(endTime time.Time) string {
	return endTime.Add(-time.Nanosecond).Format("2006-01-02")
}

// Stats returns usage statistics within the current user's scope.
// GET /api/v1/usage/stats
func (h *UsageHandler) Stats(c *gin.Context) {
	parsed, ok := h.parseUserUsageFilters(c, true)
	if !ok {
		return
	}

	stats, err := h.usageService.GetUserStatsWithFilters(c.Request.Context(), parsed.Filters)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	stats.TotalProviderCost = nil
	stats.UpstreamEndpoints = nil
	stats.EndpointPaths = nil

	response.Success(c, stats)
}

func parseAPIKeyDailyUsageDays(raw string) (int, bool) {
	if strings.TrimSpace(raw) == "" {
		return defaultAPIKeyDailyUsageDays, true
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days <= 0 || days > maxAPIKeyDailyUsageDays {
		return 0, false
	}
	return days, true
}

func apiKeyDailyUsageRange(days int, userTZ string, calendar timezone.Calendar) (time.Time, time.Time) {
	now := calendar.NowInUserLocation(userTZ)
	startTime := calendar.StartOfDayInUserLocation(now.AddDate(0, 0, -(days-1)), userTZ)
	endTime := calendar.StartOfDayInUserLocation(now.AddDate(0, 0, 1), userTZ)
	return startTime, endTime
}

// DashboardStats returns the user's dashboard counters.
// GET /api/v1/usage/dashboard/stats
func (h *UsageHandler) DashboardStats(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	stats, err := h.usageService.GetUserDashboardStats(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, stats)
}

// DashboardTrend returns usage trends within the user's scope.
// GET /api/v1/usage/dashboard/trend
func (h *UsageHandler) DashboardTrend(c *gin.Context) {
	parsed, ok := h.parseUserUsageFilters(c, true)
	if !ok {
		return
	}
	granularity := c.DefaultQuery("granularity", "day")

	trend, err := h.usageService.GetUsageTrendWithFilters(c.Request.Context(), parsed.StartTime, parsed.EndTime, granularity, parsed.Filters)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"trend":       trend,
		"start_date":  parsed.StartTime.Format("2006-01-02"),
		"end_date":    usageFilterDisplayEndDate(parsed.EndTime),
		"granularity": granularity,
	})
}

// DashboardModels returns usage grouped by requested model within the user's scope.
// GET /api/v1/usage/dashboard/models
func (h *UsageHandler) DashboardModels(c *gin.Context) {
	parsed, ok := h.parseUserUsageFilters(c, true)
	if !ok {
		return
	}

	modelSource := strings.TrimSpace(c.Query("model_source"))
	if modelSource != "" && modelSource != usage.ModelSourceRequested {
		response.BadRequest(c, "Invalid model_source, user usage only supports requested")
		return
	}

	stats, err := h.usageService.GetModelStatsWithFiltersBySource(c.Request.Context(), parsed.StartTime, parsed.EndTime, parsed.Filters, usage.ModelSourceRequested)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"models":     userModelStatsFromUsageStats(stats),
		"start_date": parsed.StartTime.Format("2006-01-02"),
		"end_date":   usageFilterDisplayEndDate(parsed.EndTime),
	})
}

// DashboardSnapshotV2 returns a combined chart snapshot for the user's usage page.
// GET /api/v1/usage/dashboard/snapshot-v2
func (h *UsageHandler) DashboardSnapshotV2(c *gin.Context) {
	parsed, ok := h.parseUserUsageFilters(c, true)
	if !ok {
		return
	}

	granularity := strings.TrimSpace(c.DefaultQuery("granularity", "day"))
	if granularity != "hour" {
		granularity = "day"
	}
	includeTrend, ok := parseBoolQueryWithDefault(c, "include_trend", true)
	if !ok {
		return
	}
	includeModels, ok := parseBoolQueryWithDefault(c, "include_model_stats", true)
	if !ok {
		return
	}
	includeGroups, ok := parseBoolQueryWithDefault(c, "include_group_stats", false)
	if !ok {
		return
	}

	resp := gin.H{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"start_date":   parsed.StartTime.Format("2006-01-02"),
		"end_date":     usageFilterDisplayEndDate(parsed.EndTime),
		"granularity":  granularity,
	}

	if includeTrend {
		trend, err := h.usageService.GetUsageTrendWithFilters(c.Request.Context(), parsed.StartTime, parsed.EndTime, granularity, parsed.Filters)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		resp["trend"] = trend
	}
	if includeModels {
		models, err := h.usageService.GetModelStatsWithFiltersBySource(c.Request.Context(), parsed.StartTime, parsed.EndTime, parsed.Filters, usage.ModelSourceRequested)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		resp["models"] = userModelStatsFromUsageStats(models)
	}
	if includeGroups {
		groups, err := h.usageService.GetGroupStatsWithFilters(c.Request.Context(), parsed.StartTime, parsed.EndTime, parsed.Filters)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		resp["groups"] = userGroupStatsFromUsageStats(groups)
	}

	response.Success(c, resp)
}

func userModelStatsFromUsageStats(stats []usage.ModelStat) []userModelStat {
	out := make([]userModelStat, 0, len(stats))
	for _, stat := range stats {
		out = append(out, userModelStat{
			Model:               stat.Model,
			Requests:            stat.Requests,
			InputTokens:         stat.InputTokens,
			OutputTokens:        stat.OutputTokens,
			CacheCreationTokens: stat.CacheCreationTokens,
			CacheReadTokens:     stat.CacheReadTokens,
			TotalTokens:         stat.TotalTokens,
			Cost:                stat.Cost,
			ActualCost:          stat.ActualCost,
		})
	}
	return out
}

func userGroupStatsFromUsageStats(stats []usage.GroupStat) []userGroupStat {
	out := make([]userGroupStat, 0, len(stats))
	for _, stat := range stats {
		out = append(out, userGroupStat{
			GroupID:     stat.GroupID,
			GroupName:   stat.GroupName,
			Requests:    stat.Requests,
			TotalTokens: stat.TotalTokens,
			Cost:        stat.Cost,
			ActualCost:  stat.ActualCost,
		})
	}
	return out
}

func parseBoolQueryWithDefault(c *gin.Context, key string, fallback bool) (bool, bool) {
	raw := c.Query(key)
	if strings.TrimSpace(raw) == "" {
		return fallback, true
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		response.BadRequest(c, "Invalid "+key+" value, use true or false")
		return false, false
	}
	return parsed, true
}

// usageFilterDisplayEndDate converts the half-open range's end to the last included display date.
func usageFilterDisplayEndDate(endTime time.Time) string {
	if endTime.IsZero() {
		return ""
	}
	return endTime.Add(-time.Nanosecond).Format("2006-01-02")
}

// DashboardAPIKeysUsage returns batch usage for keys owned by the current user.
// POST /api/v1/usage/dashboard/api-keys-usage
func (h *UsageHandler) DashboardAPIKeysUsage(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	var req BatchAPIKeysUsageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if len(req.APIKeyIDs) == 0 {
		response.Success(c, gin.H{"stats": map[string]any{}})
		return
	}

	// Bound batch size to limit query parameters and work per request.
	if len(req.APIKeyIDs) > 100 {
		response.BadRequest(c, "Too many API key IDs (maximum 100 allowed)")
		return
	}

	validAPIKeyIDs, err := h.apiKeyService.VerifyOwnership(c.Request.Context(), subject.UserID, req.APIKeyIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	if len(validAPIKeyIDs) == 0 {
		response.Success(c, gin.H{"stats": map[string]any{}})
		return
	}

	stats, err := h.usageService.GetBatchAPIKeyUsageStats(c.Request.Context(), validAPIKeyIDs, time.Time{}, time.Time{})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"stats": stats})
}

// GetMyAPIKeyDailyUsage returns daily usage for a key owned by the current user.
// GET /api/v1/user/api-keys/:id/usage/daily?days=30
func (h *UsageHandler) GetMyAPIKeyDailyUsage(c *gin.Context) {
	subject, ok := authctx.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	apiKeyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid API key ID")
		return
	}

	days, ok := parseAPIKeyDailyUsageDays(c.DefaultQuery("days", ""))
	if !ok {
		response.BadRequest(c, "Invalid days, allowed range is 1-90")
		return
	}

	if h.apiKeyService == nil {
		response.InternalError(c, "API key service is not configured")
		return
	}

	apiKey, err := h.apiKeyService.GetByID(c.Request.Context(), apiKeyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if apiKey.UserID != subject.UserID {
		response.Forbidden(c, "Not authorized to access this API key's usage")
		return
	}

	userTZ := c.Query("timezone")
	startTime, endTime := apiKeyDailyUsageRange(days, userTZ, h.calendar)
	items, err := h.usageService.GetAPIKeyDailyUsage(c.Request.Context(), subject.UserID, apiKeyID, startTime, endTime)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"items":      items,
		"days":       days,
		"start_date": startTime.Format("2006-01-02"),
		"end_date":   endTime.AddDate(0, 0, -1).Format("2006-01-02"),
	})
}

// ParseAPIKeyDailyUsageDays shares daily-range validation with the public usage endpoint.
func ParseAPIKeyDailyUsageDays(raw string) (int, bool) { return parseAPIKeyDailyUsageDays(raw) }
