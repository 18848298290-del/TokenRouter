package codec

import (
	"fmt"
	"time"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

type ProviderCodec struct{}

func (ProviderCodec) Encode(value scheduler.SnapshotProvider) ([]byte, []byte, error) {
	v, err := RecordValue(value)
	if err != nil {
		return nil, nil, err
	}
	if v == nil {
		return nil, nil, fmt.Errorf("nil scheduler provider")
	}
	return marshalSchedulerCacheProvider(*v)
}

func (ProviderCodec) Decode(raw any) (scheduler.SnapshotProvider, error) {
	v, err := decodeCachedProvider(raw)
	return WrapRecord(v), err
}

func (ProviderCodec) LastUsedAt(value scheduler.SnapshotProvider) (*time.Time, error) {
	v, err := RecordValue(value)
	if err != nil || v == nil {
		return nil, err
	}
	return v.LastUsedAt, nil
}

func (ProviderCodec) SetLastUsedAt(value scheduler.SnapshotProvider, at *time.Time) error {
	v, err := RecordValue(value)
	if err != nil {
		return err
	}
	if v != nil {
		v.LastUsedAt = at
	}
	return nil
}

func (ProviderCodec) Metadata(value providercore.Record) providercore.Record {
	return buildSchedulerMetadataProvider(value)
}

func decodeCachedProvider(val any) (*providercore.Record, error) {
	var payload []byte
	switch raw := val.(type) {
	case string:
		payload = []byte(raw)
	case []byte:
		payload = raw
	default:
		return nil, fmt.Errorf("unexpected provider cache type: %T", val)
	}
	record, err := UnmarshalProviderRecord(payload)
	return record, err
}

func marshalSchedulerCacheProvider(provider providercore.Record) ([]byte, []byte, error) {
	fullPayload, err := MarshalProviderRecord(&provider)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal provider: %w", err)
	}
	metadata := buildSchedulerMetadataProvider(provider)
	metaPayload, err := MarshalProviderRecord(&metadata)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal provider metadata: %w", err)
	}
	return fullPayload, metaPayload, nil
}

func buildSchedulerMetadataProvider(provider providercore.Record) providercore.Record {
	return providercore.Record{
		ID:                      provider.ID,
		Name:                    provider.Name,
		Platform:                provider.Platform,
		Type:                    provider.Type,
		Concurrency:             provider.Concurrency,
		LoadFactor:              provider.LoadFactor,
		Priority:                provider.Priority,
		RateMultiplier:          provider.RateMultiplier,
		Status:                  provider.Status,
		LastUsedAt:              provider.LastUsedAt,
		ExpiresAt:               provider.ExpiresAt,
		AutoPauseOnExpired:      provider.AutoPauseOnExpired,
		Schedulable:             provider.Schedulable,
		RateLimitedAt:           provider.RateLimitedAt,
		RateLimitResetAt:        provider.RateLimitResetAt,
		OverloadUntil:           provider.OverloadUntil,
		TempUnschedulableUntil:  provider.TempUnschedulableUntil,
		TempUnschedulableReason: provider.TempUnschedulableReason,
		SessionWindowStart:      provider.SessionWindowStart,
		SessionWindowEnd:        provider.SessionWindowEnd,
		SessionWindowStatus:     provider.SessionWindowStatus,
		ParentProviderID:        provider.ParentProviderID,
		QuotaDimension:          provider.QuotaDimension,
		ProviderGroups:          filterSchedulerProviderGroups(provider.ProviderGroups),
		GroupIDs:                filterSchedulerGroupIDs(provider.GroupIDs, provider.ProviderGroups),
		Credentials:             filterSchedulerCredentials(provider.Credentials),
		Extra:                   filterSchedulerExtra(provider.Extra),
	}
}

func filterSchedulerProviderGroups(providerGroups []providercore.GroupMembership) []providercore.GroupMembership {
	if len(providerGroups) == 0 {
		return nil
	}

	filtered := make([]providercore.GroupMembership, 0, len(providerGroups))
	for _, ag := range providerGroups {
		if ag.GroupID <= 0 {
			continue
		}
		filtered = append(filtered, providercore.GroupMembership{
			ProviderID: ag.ProviderID,
			GroupID:    ag.GroupID,
			CreatedAt:  ag.CreatedAt,
		})
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func filterSchedulerGroupIDs(groupIDs []int64, providerGroups []providercore.GroupMembership) []int64 {
	if len(groupIDs) == 0 && len(providerGroups) == 0 {
		return nil
	}

	seen := make(map[int64]struct{}, len(groupIDs)+len(providerGroups))
	filtered := make([]int64, 0, len(groupIDs)+len(providerGroups))
	for _, id := range groupIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		filtered = append(filtered, id)
	}
	for _, ag := range providerGroups {
		if ag.GroupID <= 0 {
			continue
		}
		if _, ok := seen[ag.GroupID]; ok {
			continue
		}
		seen[ag.GroupID] = struct{}{}
		filtered = append(filtered, ag.GroupID)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func filterSchedulerCredentials(credentials map[string]any) map[string]any {
	if len(credentials) == 0 {
		return nil
	}
	keys := []string{
		"model_mapping",
		"compact_model_mapping",
		"model_whitelist",
		"upstream_protocols",
		"auth_mode",
		"openai_auth_mode",
		"provider_mode",
		"api_protocol",
		"openai_workload_capabilities",
		"api_key",
		"project_id",
		"oauth_type",
		// Gemini quota checks run before hydration and need the third-party source and official tier to choose a quota policy.
		"provider_type",
		"tier_id",
		"plan_type",
	}
	filtered := make(map[string]any)
	for _, key := range keys {
		if value, ok := credentials[key]; ok && value != nil {
			filtered[key] = value
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func filterSchedulerExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	keys := []string{
		"quota_limit",
		"quota_used",
		"quota_daily_limit",
		"quota_daily_used",
		"quota_daily_start",
		"quota_daily_reset_mode",
		"quota_daily_reset_hour",
		"quota_weekly_limit",
		"quota_weekly_used",
		"quota_weekly_start",
		"quota_weekly_reset_mode",
		"quota_weekly_reset_day",
		"quota_weekly_reset_hour",
		"quota_reset_timezone",
		"window_cost_limit",
		"window_cost_sticky_reserve",
		"max_sessions",
		"session_idle_timeout_minutes",
		"responses_ws_connection_mode",
		"openai_text_route_mode",
		"openai_compact_mode",
		"openai_native_compaction_v2_mode",
		"openai_responses_continuation_supported",
		// Candidate filtering reads this snapshot, so retain the passthrough mode inputs
		// used when evaluating a provider's protocol and model support.
		// A snapshot missing these settings can disagree with the full provider record.
		"openai_passthrough",
		"openai_oauth_passthrough",
		"codex_fingerprint_mode",
		"codex_fingerprint_seed",
		"codex_5h_used_percent",
		"codex_7d_used_percent",
		"codex_5h_reset_at",
		"codex_7d_reset_at",
		"codex_5h_reset_after_seconds",
		"codex_7d_reset_after_seconds",
		"codex_usage_updated_at",
		"auto_pause_5h_threshold",
		"auto_pause_7d_threshold",
		"auto_pause_5h_disabled",
		"auto_pause_7d_disabled",
		"model_rate_limits",
		// Media eligibility reads both the administrator override and observed billing state.
		providercore.GrokMediaEligibleExtraKey,
		"grok_billing_snapshot",
	}
	filtered := make(map[string]any)
	for _, key := range keys {
		if value, ok := extra[key]; ok && value != nil {
			filtered[key] = value
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}
