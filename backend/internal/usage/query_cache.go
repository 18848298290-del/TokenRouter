package usage

import (
	"context"
	"time"
)

// Administrator usage statistics share a thirty-second snapshot by an encoded
// subset of their query filters. UTC RFC3339 strings make equivalent offsets
// share time keys at second precision, while nil time bounds remain empty and
// optional pointer selectors retain their JSON null or concrete values.
//
// The key represents the administrator handler's filter shape.
// IncludeOwnedTeam, PersonalOnly, ModelFilterSource, and subsecond time
// precision are absent from it, so other callers keep those inputs consistent
// across requests that can share a key. Query execution and per-waiter
// cancellation belong to querycache; callers keep captured filter pointers
// unchanged until the shared load finishes.

// usageStatsCacheKeyData identifies the filters for the 30-second admin usage
// statistics cache.
type usageStatsCacheKeyData struct {
	RequestID      string `json:"request_id"`      // request lookup text selector
	EndpointSource string `json:"endpoint_source"` // selected endpoint chart dimension
	StartTime      string `json:"start_time"`      // UTC RFC3339 range start
	EndTime        string `json:"end_time"`        // UTC RFC3339 range end
	UserID         int64  `json:"user_id"`         // acting user scope selector
	APIKeyID       int64  `json:"api_key_id"`      // API key scope selector
	ProviderID     int64  `json:"provider_id"`     // execution provider scope selector
	GroupID        int64  `json:"group_id"`        // routed group scope selector
	TeamID         int64  `json:"team_id"`         // team scope selector value
	Model          string `json:"model"`           // model text filter value
	BillingMode    string `json:"billing_mode"`    // recorded billing mode selector
	// RequestType, Stream, BillingType, and NativeCompactionV2 retain nil as
	// an absent selector and encode supplied values separately, including false
	// or zero values that still request filtering.
	RequestType        *int16 `json:"request_type"`         // optional request category selector
	Stream             *bool  `json:"stream"`               // optional legacy transport selector
	BillingType        *int8  `json:"billing_type"`         // optional funding type selector
	NativeCompactionV2 *bool  `json:"native_compaction_v2"` // optional remote compaction selector
}

// usageStatsCacheKey encodes the administrator statistics filter shape as JSON.
// Missing time bounds become empty strings, and supplied times convert to UTC
// RFC3339 text at second precision so differing offsets for that instant share
// a key.
//
// Optional pointer filters retain absent versus supplied values. The key omits
// IncludeOwnedTeam, PersonalOnly, ModelFilterSource, and subsecond precision;
// callers requiring those distinctions keep them fixed or choose a separate
// query path. ExactTotal governs listing pagination and is absent from this
// statistics key.
func usageStatsCacheKey(filters UsageLogFilters) string {
	// RFC3339 drops fractional seconds from both bounds; nearby request-time
	// cutoffs can therefore reuse the same administrative snapshot.
	start := ""
	if filters.StartTime != nil {
		start = filters.StartTime.UTC().Format(time.RFC3339)
	}
	end := ""
	if filters.EndTime != nil {
		end = filters.EndTime.UTC().Format(time.RFC3339)
	}
	return mustMarshalDashboardCacheKey(usageStatsCacheKeyData{
		RequestID:          filters.RequestID,
		EndpointSource:     filters.EndpointSource,
		StartTime:          start,
		EndTime:            end,
		UserID:             filters.UserID,
		APIKeyID:           filters.APIKeyID,
		ProviderID:         filters.ProviderID,
		GroupID:            filters.GroupID,
		TeamID:             filters.TeamID,
		Model:              filters.Model,
		BillingMode:        filters.BillingMode,
		RequestType:        filters.RequestType,
		Stream:             filters.Stream,
		BillingType:        filters.BillingType,
		NativeCompactionV2: filters.NativeCompactionV2,
	})
}

// GetStatsCached returns statistics through the service's thirty-second query
// cache and reports its cache-hit observation. The cache shares same-key loads
// while each waiter observes its own context cancellation. Shared work inherits
// the first caller's context values; the final departing waiter cancels an
// unfinished load.
//
// Callers supply nonnil contexts and filters suitable for usageStatsCacheKey.
// Keep optional filter values immutable until loading finishes, because the
// loader captures their pointers. The returned UsageStats, TotalProviderCost
// pointer, and endpoint slices are recursively copied, so callers can modify
// those results independently. Lookup or payload-type failures return errors.
// @project-doc docs/operations/observability_and_data_lifecycle.md#usage_query_contracts
func (h *UsageService) GetStatsCached(ctx context.Context, filters UsageLogFilters) (*UsageStats, bool, error) {
	key := usageStatsCacheKey(filters)
	entry, hit, err := h.statsQueryCache.GetOrLoadContext(ctx, key, func(shared context.Context) (any, error) {
		return h.GetStatsWithFilters(shared, filters)
	})
	if err != nil {
		return nil, hit, err
	}
	stats, err := snapshotPayloadAs[*UsageStats](entry.Payload)
	return stats, hit, err
}
