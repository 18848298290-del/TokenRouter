package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
)

// Dashboard chart filters become JSON cache keys with UTC range text at
// second precision. Optional request, stream, billing and compaction values
// retain nil separately from concrete values, and each chart family owns its
// cache and expiration policy.
//
// The querycache loader combines matching keys and gives each waiter its own
// cancellation result. Shared work inherits the first caller's context
// values; group keys include presentation language when user presentation is
// requested. The callbacks retain filter pointers supplied by callers, so
// those inputs remain immutable while shared loading can continue. Returned
// chart slices are copied through querycache's supported payload traversal.

// dashboardTrendCacheKey identifies a trend query by its recorded range and
// selectors.
type dashboardTrendCacheKey struct {
	// StartTime and EndTime serialize UTC bounds at RFC3339 second precision.
	// Queries differing solely in fractional seconds can therefore share an
	// entry despite retaining exact loader bounds.
	StartTime   string `json:"start_time"`  // UTC query start text
	EndTime     string `json:"end_time"`    // UTC query end text
	Granularity string `json:"granularity"` // requested trend bucket label
	UserID      int64  `json:"user_id"`     // filter actor user identifier
	APIKeyID    int64  `json:"api_key_id"`  // filter key identifier
	ProviderID  int64  `json:"provider_id"` // filter provider identifier
	GroupID     int64  `json:"group_id"`    // filter group identifier
	TeamID      int64  `json:"team_id"`     // filter team identifier
	Model       string `json:"model"`       // requested model filter text
	// RequestType, Stream, BillingType and NativeCompactionV2 retain nil
	// distinctly from supplied values in JSON. Their pointer contents are read
	// during key encoding and remain shared loader inputs.
	RequestType        *int16 `json:"request_type"`         // optional request category selector
	Stream             *bool  `json:"stream"`               // optional stream transport selector
	BillingType        *int8  `json:"billing_type"`         // optional funding category selector
	NativeCompactionV2 *bool  `json:"native_compaction_v2"` // optional compaction v2 selector
}

// dashboardModelGroupCacheKey identifies model or group queries within their
// separate caches.
type dashboardModelGroupCacheKey struct {
	// StartTime and EndTime serialize UTC bounds at RFC3339 second precision.
	// Queries differing solely in fractional seconds can therefore share an
	// entry despite retaining exact loader bounds.
	StartTime  string `json:"start_time"`  // UTC query start text
	EndTime    string `json:"end_time"`    // UTC query end text
	UserID     int64  `json:"user_id"`     // filter actor user identifier
	APIKeyID   int64  `json:"api_key_id"`  // filter key identifier
	ProviderID int64  `json:"provider_id"` // filter provider identifier
	GroupID    int64  `json:"group_id"`    // filter group identifier
	TeamID     int64  `json:"team_id"`     // filter team identifier
	// ModelSource holds the normalized model grouping dimension for model
	// queries. Empty values are omitted for group queries, which use a separate
	// cache.
	ModelSource string `json:"model_source,omitempty"` // normalized model grouping dimension
	// RequestType, Stream, BillingType and NativeCompactionV2 retain nil
	// distinctly from supplied values in JSON. Their pointer contents are read
	// during key encoding and remain shared loader inputs.
	RequestType        *int16 `json:"request_type"`         // optional request category selector
	Stream             *bool  `json:"stream"`               // optional stream transport selector
	BillingType        *int8  `json:"billing_type"`         // optional funding category selector
	NativeCompactionV2 *bool  `json:"native_compaction_v2"` // optional compaction v2 selector
}

// dashboardEntityTrendCacheKey identifies an entity trend range, granularity
// and result limit.
type dashboardEntityTrendCacheKey struct {
	// StartTime and EndTime serialize UTC bounds at RFC3339 second precision.
	// Queries differing solely in fractional seconds can therefore share an
	// entry despite retaining exact loader bounds.
	StartTime   string `json:"start_time"`  // UTC query start text
	EndTime     string `json:"end_time"`    // UTC query end text
	Granularity string `json:"granularity"` // requested entity bucket label
	Limit       int    `json:"limit"`       // requested entity result limit
}

// dashboardQueryCaches holds independent result caches for dashboard chart
// and batch query families.
type dashboardQueryCaches struct {
	dashboardTrendCache            *querycache.Cache // filtered usage trend cache
	dashboardModelStatsCache       *querycache.Cache // model grouping result cache
	dashboardGroupStatsCache       *querycache.Cache // group grouping result cache
	dashboardUsersTrendCache       *querycache.Cache // user trend result cache
	dashboardAPIKeysTrendCache     *querycache.Cache // key trend result cache
	ranking, batchUsers, batchKeys *querycache.Cache // ranking: spending rows; batchUsers: user summaries; batchKeys: key summaries
}

// mustMarshalDashboardCacheKey encodes a supported key value as JSON text.
// Encoding failure returns an empty key, which querycache treats as uncached
// loading rather than a shared stored entry.
func mustMarshalDashboardCacheKey(value any) string {
	raw, err := json.Marshal(value)
	// Empty keys bypass storage and flight sharing in querycache, allowing callers
	// to query after an encoding failure rather than reuse an unidentified payload.
	if err != nil {
		return ""
	}
	return string(raw)
}

// snapshotPayloadAs asserts the payload's dynamic type as T and returns the
// typed value. A different type produces an error; a nil pointer already
// typed as T remains a successful typed value.
func snapshotPayloadAs[T any](payload any) (T, error) {
	typed, ok := payload.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("unexpected cache payload type %T", payload)
	}
	return typed, nil
}

// GetUsageTrendCached returns the selected trend and whether a cache lookup
// supplied it. Callers provide a nonnil context and keep optional filter
// pointers immutable, because matching shared loads can continue after one
// waiter cancels.
func (h *DashboardService) GetUsageTrendCached(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	userID, apiKeyID, providerID, groupID, teamID int64,
	model string,
	requestType *int16,
	stream *bool,
	billingType *int8,
	nativeCompactionV2 *bool,
) ([]TrendDataPoint, bool, error) {
	key := mustMarshalDashboardCacheKey(dashboardTrendCacheKey{
		StartTime:          startTime.UTC().Format(time.RFC3339),
		EndTime:            endTime.UTC().Format(time.RFC3339),
		Granularity:        granularity,
		UserID:             userID,
		APIKeyID:           apiKeyID,
		ProviderID:         providerID,
		GroupID:            groupID,
		TeamID:             teamID,
		Model:              model,
		RequestType:        requestType,
		Stream:             stream,
		BillingType:        billingType,
		NativeCompactionV2: nativeCompactionV2,
	})
	entry, hit, err := h.queryCaches.dashboardTrendCache.GetOrLoadContext(ctx, key, func(shared context.Context) (any, error) {
		return h.GetUsageTrendWithUsageFilters(shared, startTime, endTime, granularity, UsageLogFilters{
			UserID: userID, APIKeyID: apiKeyID, ProviderID: providerID, GroupID: groupID, TeamID: teamID,
			Model: model, RequestType: requestType, Stream: stream, BillingType: billingType,
			NativeCompactionV2: nativeCompactionV2,
		})
	})
	if err != nil {
		return nil, hit, err
	}
	trend, err := snapshotPayloadAs[[]TrendDataPoint](entry.Payload)
	return trend, hit, err
}

// GetModelStatsCached returns model-grouped statistics and cache-hit status,
// normalizing the grouping source in its key. Callers provide a nonnil
// context and immutable optional filter pointers while matching shared
// loading can continue.
func (h *DashboardService) GetModelStatsCached(
	ctx context.Context,
	startTime, endTime time.Time,
	userID, apiKeyID, providerID, groupID, teamID int64,
	modelSource string,
	requestType *int16,
	stream *bool,
	billingType *int8,
	nativeCompactionV2 *bool,
) ([]ModelStat, bool, error) {
	key := mustMarshalDashboardCacheKey(dashboardModelGroupCacheKey{
		StartTime:          startTime.UTC().Format(time.RFC3339),
		EndTime:            endTime.UTC().Format(time.RFC3339),
		UserID:             userID,
		APIKeyID:           apiKeyID,
		ProviderID:         providerID,
		GroupID:            groupID,
		TeamID:             teamID,
		ModelSource:        NormalizeModelSource(modelSource),
		RequestType:        requestType,
		Stream:             stream,
		BillingType:        billingType,
		NativeCompactionV2: nativeCompactionV2,
	})
	entry, hit, err := h.queryCaches.dashboardModelStatsCache.GetOrLoadContext(ctx, key, func(shared context.Context) (any, error) {
		return h.GetModelStatsWithUsageFiltersBySource(shared, startTime, endTime, UsageLogFilters{
			UserID: userID, APIKeyID: apiKeyID, ProviderID: providerID, GroupID: groupID, TeamID: teamID,
			RequestType: requestType, Stream: stream, BillingType: billingType,
			NativeCompactionV2: nativeCompactionV2,
		}, modelSource)
	})
	if err != nil {
		return nil, hit, err
	}
	stats, err := snapshotPayloadAs[[]ModelStat](entry.Payload)
	return stats, hit, err
}

// GetGroupStatsCached returns group statistics and reports whether a cache
// lookup supplied the result. Callers provide a nonnil context and keep
// optional selector pointers immutable while a matching shared loader can
// continue after one waiter cancels.
//
// User presentation adds its locale to the encoded selector key. The shared
// loader inherits the first caller's context values, so this extra identity
// separates language-dependent group labels from other user locales and
// ordinary administrator presentation. Repository errors and unexpected
// payload types are returned to the waiting caller.
func (h *DashboardService) GetGroupStatsCached(
	ctx context.Context,
	startTime, endTime time.Time,
	userID, apiKeyID, providerID, groupID, teamID int64,
	requestType *int16,
	stream *bool,
	billingType *int8,
	nativeCompactionV2 *bool,
) ([]GroupStat, bool, error) {
	key := mustMarshalDashboardCacheKey(dashboardModelGroupCacheKey{
		StartTime:          startTime.UTC().Format(time.RFC3339),
		EndTime:            endTime.UTC().Format(time.RFC3339),
		UserID:             userID,
		APIKeyID:           apiKeyID,
		ProviderID:         providerID,
		GroupID:            groupID,
		TeamID:             teamID,
		RequestType:        requestType,
		Stream:             stream,
		BillingType:        billingType,
		NativeCompactionV2: nativeCompactionV2,
	})
	// Shared loaders inherit the first caller's context values. Include user locale
	// when localized labels are requested so one presentation cannot supply another locale's result.
	if locale.UserPresentation(ctx) {
		key = "user:" + locale.FromContext(ctx) + ":" + key
	}
	entry, hit, err := h.queryCaches.dashboardGroupStatsCache.GetOrLoadContext(ctx, key, func(shared context.Context) (any, error) {
		return h.GetGroupStatsWithUsageFilters(shared, startTime, endTime, UsageLogFilters{
			UserID: userID, APIKeyID: apiKeyID, ProviderID: providerID, GroupID: groupID, TeamID: teamID,
			RequestType: requestType, Stream: stream, BillingType: billingType,
			NativeCompactionV2: nativeCompactionV2,
		})
	})
	if err != nil {
		return nil, hit, err
	}
	stats, err := snapshotPayloadAs[[]GroupStat](entry.Payload)
	return stats, hit, err
}

// GetAPIKeyUsageTrendCached returns a cached or shared-loaded key trend and
// cache-hit status. Each caller supplies a nonnil context; canceling its wait
// leaves the query active while another waiter remains.
func (h *DashboardService) GetAPIKeyUsageTrendCached(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) ([]APIKeyUsageTrendPoint, bool, error) {
	key := mustMarshalDashboardCacheKey(dashboardEntityTrendCacheKey{
		StartTime:   startTime.UTC().Format(time.RFC3339),
		EndTime:     endTime.UTC().Format(time.RFC3339),
		Granularity: granularity,
		Limit:       limit,
	})
	entry, hit, err := h.queryCaches.dashboardAPIKeysTrendCache.GetOrLoadContext(ctx, key, func(shared context.Context) (any, error) {
		return h.GetAPIKeyUsageTrend(shared, startTime, endTime, granularity, limit)
	})
	if err != nil {
		return nil, hit, err
	}
	trend, err := snapshotPayloadAs[[]APIKeyUsageTrendPoint](entry.Payload)
	return trend, hit, err
}

// GetUserUsageTrendCached returns a cached or shared-loaded user trend and
// cache-hit status. Each caller supplies a nonnil context; canceling its wait
// leaves the query active while another waiter remains.
func (h *DashboardService) GetUserUsageTrendCached(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) ([]UserUsageTrendPoint, bool, error) {
	key := mustMarshalDashboardCacheKey(dashboardEntityTrendCacheKey{
		StartTime:   startTime.UTC().Format(time.RFC3339),
		EndTime:     endTime.UTC().Format(time.RFC3339),
		Granularity: granularity,
		Limit:       limit,
	})
	entry, hit, err := h.queryCaches.dashboardUsersTrendCache.GetOrLoadContext(ctx, key, func(shared context.Context) (any, error) {
		return h.GetUserUsageTrend(shared, startTime, endTime, granularity, limit)
	})
	if err != nil {
		return nil, hit, err
	}
	trend, err := snapshotPayloadAs[[]UserUsageTrendPoint](entry.Payload)
	return trend, hit, err
}

// newDashboardQueryCaches creates separate dashboard chart and batch result
// caches. Ranking entries last five minutes; chart and batch entries last
// thirty seconds, with expiration and supported payload copying handled by
// querycache.
func newDashboardQueryCaches() *dashboardQueryCaches {
	return &dashboardQueryCaches{dashboardTrendCache: querycache.NewCache(30 * time.Second), dashboardModelStatsCache: querycache.NewCache(30 * time.Second), dashboardGroupStatsCache: querycache.NewCache(30 * time.Second), dashboardUsersTrendCache: querycache.NewCache(30 * time.Second), dashboardAPIKeysTrendCache: querycache.NewCache(30 * time.Second), ranking: querycache.NewCache(5 * time.Minute), batchUsers: querycache.NewCache(30 * time.Second), batchKeys: querycache.NewCache(30 * time.Second)}
}
