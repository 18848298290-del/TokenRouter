package usage

import (
	"context"
	"time"
)

// usageStatsCacheKeyData identifies the filters for the 30-second admin usage statistics cache.
type usageStatsCacheKeyData struct {
	RequestID          string `json:"request_id"`
	EndpointSource     string `json:"endpoint_source"`
	StartTime          string `json:"start_time"`
	EndTime            string `json:"end_time"`
	UserID             int64  `json:"user_id"`
	APIKeyID           int64  `json:"api_key_id"`
	ProviderID         int64  `json:"provider_id"`
	GroupID            int64  `json:"group_id"`
	TeamID             int64  `json:"team_id"`
	Model              string `json:"model"`
	BillingMode        string `json:"billing_mode"`
	RequestType        *int16 `json:"request_type"`
	Stream             *bool  `json:"stream"`
	BillingType        *int8  `json:"billing_type"`
	NativeCompactionV2 *bool  `json:"native_compaction_v2"`
}

func usageStatsCacheKey(filters UsageLogFilters) string {
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

// GetStatsCached coalesces queries with identical filters while allowing each waiter to cancel independently.
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
