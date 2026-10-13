package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	response "github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Administrator snapshot requests choose chart sections before a shared
// loader runs. Parsed filters, range values and inclusion flags form the
// cache identity and become loader inputs, allowing the HTTP waiter to
// observe its own cancellation while the loader continues for other waiters.
//
// The sections have different scopes: Stats uses full dashboard totals,
// UsersTrend uses the selected range and ranking limit, and the trend, model
// and group charts receive their supported selectors. Model-name filtering
// applies to Trend here. The cached response keeps generated time, uptime and
// formatted dates until expiration; UTC second-precision key bounds can
// therefore share one stored response across different fractional or location
// representations.

// dashboardSnapshotV2Cache stores process-wide combined administrator
// responses and ETags for thirty seconds per entry.
var dashboardSnapshotV2Cache = newSnapshotCache(30 * time.Second)

// dashboardSnapshotV2Stats carries full administrator totals and the
// handler's uptime observation.
type dashboardSnapshotV2Stats struct {
	usage.DashboardStats       // embedded global dashboard metrics
	Uptime               int64 `json:"uptime"` // observed handler uptime seconds
}

// dashboardSnapshotV2Response carries the included administrator chart
// sections and their display metadata.
type dashboardSnapshotV2Response struct {
	GeneratedAt string `json:"generated_at"` // UTC snapshot construction time

	StartDate string `json:"start_date"` // formatted query start date
	// EndDate formats the end after subtracting a fixed twenty-four hours.
	// Date-time inputs and DST transitions can therefore produce a label
	// differing from the last included calendar date.
	EndDate     string `json:"end_date"`    // formatted elapsed day end label
	Granularity string `json:"granularity"` // normalized chart bucket label

	// Stats contains full dashboard totals, independent of the selected chart
	// bounds and filters. Nil omits that section when include_stats resolves
	// false.
	Stats *dashboardSnapshotV2Stats `json:"stats,omitempty"` // optional global dashboard section
	Trend []usage.TrendDataPoint    `json:"trend,omitempty"` // optional filtered trend buckets
	// Models groups by the internal requested-model dimension and receives the
	// supported identity and category selectors. The Model-name filter used by
	// Trend is not passed to this query.
	Models []usage.ModelStat `json:"models,omitempty"` // optional model grouping section
	Groups []usage.GroupStat `json:"groups,omitempty"` // optional group grouping section
	// UsersTrend uses start, end, granularity and its own result limit. Identity
	// and category chart selectors do not restrict this global payer trend.
	UsersTrend []usage.UserUsageTrendPoint `json:"users_trend,omitempty"` // optional global payer trend section
}

// dashboardSnapshotV2Filters carries parsed scalar and optional selectors
// captured before shared loading.
type dashboardSnapshotV2Filters struct {
	UserID     int64  // filter actor user identifier
	APIKeyID   int64  // filter key identifier
	ProviderID int64  // filter provider identifier
	GroupID    int64  // filter group identifier
	TeamID     int64  // filter team identifier
	Model      string // trend model equality selector
	// RequestType and Stream retain their optional query meaning. Parsing a
	// nonempty request category clears Stream selection, so malformed legacy
	// stream text is ignored when a category was supplied.
	RequestType        *int16 // optional request category selector
	Stream             *bool  // optional stream equality selector
	BillingType        *int8  // optional funding category selector
	NativeCompactionV2 *bool  // optional compaction v2 selector
}

// dashboardSnapshotV2CacheKey identifies the captured query selections and
// included administrator response sections.
type dashboardSnapshotV2CacheKey struct {
	// StartTime and EndTime encode UTC bounds at RFC3339 second precision.
	// Fractional-second or location differences can match the same cached
	// response despite exact loader bounds and display locations.
	StartTime   string `json:"start_time"`  // UTC start instant text
	EndTime     string `json:"end_time"`    // UTC end instant text
	Granularity string `json:"granularity"` // normalized chart bucket label
	UserID      int64  `json:"user_id"`     // filter actor user identifier
	APIKeyID    int64  `json:"api_key_id"`  // filter key identifier
	ProviderID  int64  `json:"provider_id"` // filter provider identifier
	GroupID     int64  `json:"group_id"`    // filter group identifier
	TeamID      int64  `json:"team_id"`     // filter team identifier
	Model       string `json:"model"`       // trend model equality selector
	// RequestType and Stream retain their optional query meaning. Parsing a
	// nonempty request category clears Stream selection, so malformed legacy
	// stream text is ignored when a category was supplied.
	RequestType        *int16 `json:"request_type"`         // optional request category selector
	Stream             *bool  `json:"stream"`               // optional stream equality selector
	BillingType        *int8  `json:"billing_type"`         // optional funding category selector
	NativeCompactionV2 *bool  `json:"native_compaction_v2"` // optional compaction v2 selector
	IncludeStats       bool   `json:"include_stats"`        // global dashboard inclusion flag
	IncludeTrend       bool   `json:"include_trend"`        // filtered trend inclusion flag
	IncludeModels      bool   `json:"include_models"`       // model grouping inclusion flag
	IncludeGroups      bool   `json:"include_groups"`       // group grouping inclusion flag
	IncludeUsersTrend  bool   `json:"include_users_trend"`  // global payer trend inclusion flag
	UsersTrendLimit    int    `json:"users_trend_limit"`    // payer trend row limit
}

// GetSnapshotV2 returns the selected administrator dashboard sections from a
// combined HTTP cache. It normalizes granularity and inclusion defaults,
// validates selectors, then captures those values before invoking the shared
// loader.
//
// The captured values define both key encoding and query inputs, keeping Gin
// access in the HTTP waiter. Matching keys share work with independent waiter
// cancellation; query errors reject the complete response. The stored payload
// supplies ETag and Vary headers. A matching If-None-Match ends with 304
// before the body and X-Snapshot-Cache header are written.
func (h *DashboardHandler) GetSnapshotV2(c *gin.Context) {
	startTime, endTime := parseTimeRange(c, h.calendar)
	granularity := strings.TrimSpace(c.DefaultQuery("granularity", "day"))
	if granularity != "hour" {
		granularity = "day"
	}

	includeStats := parseBoolQueryWithDefault(c.Query("include_stats"), true)
	includeTrend := parseBoolQueryWithDefault(c.Query("include_trend"), true)
	includeModels := parseBoolQueryWithDefault(c.Query("include_model_stats"), true)
	includeGroups := parseBoolQueryWithDefault(c.Query("include_group_stats"), false)
	includeUsersTrend := parseBoolQueryWithDefault(c.Query("include_users_trend"), false)
	// Keep the twelve-row default when the supplied limit is outside one to fifty.
	usersTrendLimit := 12
	if raw := strings.TrimSpace(c.Query("users_trend_limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 50 {
			usersTrendLimit = parsed
		}
	}

	filters, err := parseDashboardSnapshotV2Filters(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	// This key has supported scalar and scalar-pointer JSON fields. Encode the same
	// parsed selectors and inclusion decisions that the shared loader receives below.
	keyRaw, _ := json.Marshal(dashboardSnapshotV2CacheKey{
		StartTime:          startTime.UTC().Format(time.RFC3339),
		EndTime:            endTime.UTC().Format(time.RFC3339),
		Granularity:        granularity,
		UserID:             filters.UserID,
		APIKeyID:           filters.APIKeyID,
		ProviderID:         filters.ProviderID,
		GroupID:            filters.GroupID,
		TeamID:             filters.TeamID,
		Model:              filters.Model,
		RequestType:        filters.RequestType,
		Stream:             filters.Stream,
		BillingType:        filters.BillingType,
		NativeCompactionV2: filters.NativeCompactionV2,
		IncludeStats:       includeStats,
		IncludeTrend:       includeTrend,
		IncludeModels:      includeModels,
		IncludeGroups:      includeGroups,
		IncludeUsersTrend:  includeUsersTrend,
		UsersTrendLimit:    usersTrendLimit,
	})
	cacheKey := string(keyRaw)

	// A live shared query can outlast a canceled HTTP waiter. Capture parsed values
	// before dispatch so the loader reads its inputs without revisiting the Gin context.
	cached, hit, err := dashboardSnapshotV2Cache.GetOrLoadContext(c.Request.Context(), cacheKey, func(shared context.Context) (any, error) {
		return h.buildSnapshotV2Response(
			shared,
			startTime,
			endTime,
			granularity,
			filters,
			includeStats,
			includeTrend,
			includeModels,
			includeGroups,
			includeUsersTrend,
			usersTrendLimit,
		)
	})
	if err != nil {
		response.Error(c, 500, err.Error())
		return
	}
	if cached.ETag != "" {
		c.Header("ETag", cached.ETag)
		c.Header("Vary", "If-None-Match")
		// Matching validation conditions reuse the stored response identity. End with
		// 304 before emitting the envelope body, leaving ETag and Vary available to clients.
		if ifNoneMatchMatched(c.GetHeader("If-None-Match"), cached.ETag) {
			c.Status(http.StatusNotModified)
			return
		}
	}
	c.Header("X-Snapshot-Cache", cacheStatusValue(hit))
	response.Success(c, cached.Payload)
}

// buildSnapshotV2Response builds the enabled sections sequentially from
// captured values and a nonnil filters pointer. GeneratedAt records
// construction time, and EndDate uses fixed elapsed-day subtraction when
// formatting the response.
//
// Stats queries global dashboard metrics; UsersTrend queries the selected
// range with its own limit. Trend receives the Model selector, while Models
// and Groups receive their supported identity and category selectors. Every
// enabled query must succeed before a combined result is returned, so
// failures discard the partial response assembled in memory.
func (h *DashboardHandler) buildSnapshotV2Response(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	filters *dashboardSnapshotV2Filters,
	includeStats, includeTrend, includeModels, includeGroups, includeUsersTrend bool,
	usersTrendLimit int,
) (*dashboardSnapshotV2Response, error) {
	resp := &dashboardSnapshotV2Response{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		StartDate:   startTime.Format("2006-01-02"),
		// DST shifts can move this label away from the last included calendar date.
		EndDate:     endTime.Add(-24 * time.Hour).Format("2006-01-02"),
		Granularity: granularity,
	}

	if includeStats {
		stats, err := h.dashboardService.GetDashboardStats(ctx)
		if err != nil {
			return nil, errors.New("failed to get dashboard statistics")
		}
		resp.Stats = &dashboardSnapshotV2Stats{
			DashboardStats: *stats,
			// Report whole uptime seconds at snapshot construction time.
			Uptime: int64(time.Since(h.startTime).Seconds()),
		}
	}

	if includeTrend {
		trend, _, err := h.getUsageTrendCached(
			ctx,
			startTime,
			endTime,
			granularity,
			filters.UserID,
			filters.APIKeyID,
			filters.ProviderID,
			filters.GroupID,
			filters.TeamID,
			filters.Model,
			filters.RequestType,
			filters.Stream,
			filters.BillingType,
			filters.NativeCompactionV2,
		)
		if err != nil {
			return nil, errors.New("failed to get usage trend")
		}
		resp.Trend = trend
	}

	if includeModels {
		models, _, err := h.getModelStatsCached(
			ctx,
			startTime,
			endTime,
			filters.UserID,
			filters.APIKeyID,
			filters.ProviderID,
			filters.GroupID,
			filters.TeamID,
			usage.ModelSourceRequested,
			filters.RequestType,
			filters.Stream,
			filters.BillingType,
			filters.NativeCompactionV2,
		)
		if err != nil {
			return nil, errors.New("failed to get model statistics")
		}
		resp.Models = models
	}

	if includeGroups {
		groups, _, err := h.getGroupStatsCached(
			ctx,
			startTime,
			endTime,
			filters.UserID,
			filters.APIKeyID,
			filters.ProviderID,
			filters.GroupID,
			filters.TeamID,
			filters.RequestType,
			filters.Stream,
			filters.BillingType,
			filters.NativeCompactionV2,
		)
		if err != nil {
			return nil, errors.New("failed to get group statistics")
		}
		resp.Groups = groups
	}

	if includeUsersTrend {
		usersTrend, _, err := h.getUserUsageTrendCached(ctx, startTime, endTime, granularity, usersTrendLimit)
		if err != nil {
			return nil, errors.New("failed to get user usage trend")
		}
		resp.UsersTrend = usersTrend
	}

	return resp, nil
}

// parseDashboardSnapshotV2Filters parses trimmed chart selectors and returns
// an error for invalid numeric or boolean text. Request type takes precedence
// over stream; parsed numeric values retain their signs for the repository's
// positive-ID filtering policy.
func parseDashboardSnapshotV2Filters(c *gin.Context) (*dashboardSnapshotV2Filters, error) {
	filters := &dashboardSnapshotV2Filters{
		Model: strings.TrimSpace(c.Query("model")),
	}

	if userIDStr := strings.TrimSpace(c.Query("user_id")); userIDStr != "" {
		id, err := strconv.ParseInt(userIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		filters.UserID = id
	}
	if apiKeyIDStr := strings.TrimSpace(c.Query("api_key_id")); apiKeyIDStr != "" {
		id, err := strconv.ParseInt(apiKeyIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		filters.APIKeyID = id
	}
	if providerIDStr := strings.TrimSpace(c.Query("provider_id")); providerIDStr != "" {
		id, err := strconv.ParseInt(providerIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		filters.ProviderID = id
	}
	if groupIDStr := strings.TrimSpace(c.Query("group_id")); groupIDStr != "" {
		id, err := strconv.ParseInt(groupIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		filters.GroupID = id
	}
	if teamIDStr := strings.TrimSpace(c.Query("team_id")); teamIDStr != "" {
		id, err := strconv.ParseInt(teamIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		filters.TeamID = id
	}

	if requestTypeStr := strings.TrimSpace(c.Query("request_type")); requestTypeStr != "" {
		parsed, err := usage.ParseUsageRequestType(requestTypeStr)
		if err != nil {
			return nil, err
		}
		value := int16(parsed)
		filters.RequestType = &value
	} else if streamStr := strings.TrimSpace(c.Query("stream")); streamStr != "" {
		streamVal, err := strconv.ParseBool(streamStr)
		if err != nil {
			return nil, err
		}
		filters.Stream = &streamVal
	}

	if billingTypeStr := strings.TrimSpace(c.Query("billing_type")); billingTypeStr != "" {
		v, err := strconv.ParseInt(billingTypeStr, 10, 8)
		if err != nil {
			return nil, err
		}
		bt := int8(v)
		filters.BillingType = &bt
	}
	nativeCompactionV2, err := parseOptionalBoolDashboardFilter(c, "native_compaction_v2")
	if err != nil {
		return nil, err
	}
	filters.NativeCompactionV2 = nativeCompactionV2

	return filters, nil
}
