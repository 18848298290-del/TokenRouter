package admin

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Administrator chart handlers translate query results into cache status
// headers. Their adapters call the dashboard-owned query caches, while the
// shared HTTP snapshot helper supplies composite snapshot storage, ETag
// matching, boolean defaults and normalized positive ID lists.

// snapshotCache names the HTTP snapshot cache that stores response payloads
// with generated ETags.
type snapshotCache = httpx.SnapshotCache

// getUsageTrendCached returns dashboard trend data and cache-hit status from
// the service cache. Callers supply a nonnil context and immutable optional
// selector pointers while shared loading may continue after a waiter cancels.
func (h *DashboardHandler) getUsageTrendCached(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	userID, apiKeyID, providerID, groupID, teamID int64,
	model string,
	requestType *int16,
	stream *bool,
	billingType *int8,
	nativeCompactionV2 *bool,
) ([]usage.TrendDataPoint, bool, error) {
	return h.dashboardService.GetUsageTrendCached(ctx, startTime, endTime, granularity, userID, apiKeyID, providerID, groupID, teamID, model, requestType, stream, billingType, nativeCompactionV2)
}

// getModelStatsCached returns model-grouped data and cache-hit status from
// the service cache. Callers supply a nonnil context and immutable optional
// selector pointers while shared loading may continue after a waiter cancels.
func (h *DashboardHandler) getModelStatsCached(
	ctx context.Context,
	startTime, endTime time.Time,
	userID, apiKeyID, providerID, groupID, teamID int64,
	modelSource string,
	requestType *int16,
	stream *bool,
	billingType *int8,
	nativeCompactionV2 *bool,
) ([]usage.ModelStat, bool, error) {
	return h.dashboardService.GetModelStatsCached(ctx, startTime, endTime, userID, apiKeyID, providerID, groupID, teamID, modelSource, requestType, stream, billingType, nativeCompactionV2)
}

// getGroupStatsCached returns group data and cache-hit status from the
// service cache. Callers supply a nonnil context and immutable optional
// selector pointers while shared loading may continue after a waiter cancels.
func (h *DashboardHandler) getGroupStatsCached(
	ctx context.Context,
	startTime, endTime time.Time,
	userID, apiKeyID, providerID, groupID, teamID int64,
	requestType *int16,
	stream *bool,
	billingType *int8,
	nativeCompactionV2 *bool,
) ([]usage.GroupStat, bool, error) {
	return h.dashboardService.GetGroupStatsCached(ctx, startTime, endTime, userID, apiKeyID, providerID, groupID, teamID, requestType, stream, billingType, nativeCompactionV2)
}

// getAPIKeyUsageTrendCached returns a key trend and cache-hit status through
// the dashboard service. Callers supply a nonnil context; the service cache
// independently observes that waiter's cancellation while matching loaders
// share work.
func (h *DashboardHandler) getAPIKeyUsageTrendCached(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) ([]usage.APIKeyUsageTrendPoint, bool, error) {
	return h.dashboardService.GetAPIKeyUsageTrendCached(ctx, startTime, endTime, granularity, limit)
}

// getUserUsageTrendCached returns a payer trend and cache-hit status through
// the dashboard service. Callers supply a nonnil context; the service cache
// independently observes that waiter's cancellation while matching loaders
// share work.
func (h *DashboardHandler) getUserUsageTrendCached(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) ([]usage.UserUsageTrendPoint, bool, error) {
	return h.dashboardService.GetUserUsageTrendCached(ctx, startTime, endTime, granularity, limit)
}

// cacheStatusValue returns the hit or miss value used by snapshot response
// headers.
func cacheStatusValue(hit bool) string {
	if hit {
		return "hit"
	}
	return "miss"
}

// newSnapshotCache creates an HTTP payload cache with ETags and the requested
// entry lifetime. Nonpositive lifetimes select the shared helper's
// thirty-second default, with supported payload copying handled by
// querycache.
func newSnapshotCache(ttl time.Duration) *snapshotCache { return httpx.NewSnapshotCache(ttl) }

// parseBoolQueryWithDefault delegates trimmed case-insensitive boolean text
// to the HTTP helper. Known numeric, yes/no and on/off spellings are
// accepted; empty or unrecognized values return the supplied default.
func parseBoolQueryWithDefault(raw string, def bool) bool {
	return httpx.ParseBoolQueryWithDefault(raw, def)
}

// normalizeInt64IDList returns sorted unique positive IDs in independent
// slice storage. Empty input returns nil, while supplied values that are all
// unusable return an empty slice for batch query handling.
func normalizeInt64IDList(v []int64) []int64 { return httpx.NormalizeInt64IDList(v) }

// ifNoneMatchMatched compares a comma-separated condition list with the
// supplied nonempty ETag. Trimmed exact, wildcard and uppercase W/ weak-tag
// forms match; empty inputs return false under the shared HTTP helper.
func ifNoneMatchMatched(raw, tag string) bool { return httpx.IfNoneMatchMatched(raw, tag) }
