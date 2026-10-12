package usage

import (
	"context"
	"fmt"
	"log/slog"
)

// These keys store usage display settings.
const (
	SettingKeyAllowUserViewErrorRequests  = "allow_user_view_error_requests"
	SettingKeyUsageRankingEnabled         = "usage_ranking_enabled"
	SettingKeyUsageRankingLimit           = "usage_ranking_limit"
	SettingKeyUsageRankingShowActualCost  = "usage_ranking_show_actual_cost"
	SettingKeyUsageRankingShowRequests    = "usage_ranking_show_requests"
	SettingKeyUsageRankingShowTotalTokens = "usage_ranking_show_total_tokens"
	SettingKeyUsageRankingSortBy          = "usage_ranking_sort_by"
)

// RuntimeSettingsStore reads multiple settings in one call.
type RuntimeSettingsStore interface {
	GetMultiple(context.Context, []string) (map[string]string, error)
}

// RuntimeSettings reads and parses ranking and user error-display settings.
type RuntimeSettings struct{ settingRepo RuntimeSettingsStore }

// NewRuntimeSettings creates a reader; settings are fetched when its methods are called.
func NewRuntimeSettings(repo RuntimeSettingsStore) *RuntimeSettings {
	return &RuntimeSettings{settingRepo: repo}
}

// DefaultRankingSettings enables all metrics and sorts by total tokens with the default limit.
func DefaultRankingSettings() UsageRankingSettings {
	return UsageRankingSettings{
		Enabled:         true,
		SortBy:          UsageRankingSortByTotalTokens,
		ShowTotalTokens: true,
		ShowRequests:    true,
		ShowActualCost:  true,
		Limit:           DefaultUsageRankingLimit,
	}
}

// ParseRankingSettings applies defaults, normalizes the limit, and makes the sorting metric visible.
func ParseRankingSettings(values map[string]string) UsageRankingSettings {
	settings := DefaultRankingSettings()
	if values == nil {
		return settings
	}
	settings.Enabled = values[SettingKeyUsageRankingEnabled] != "false"
	settings.SortBy = NormalizeUsageRankingSortByInternal(values[SettingKeyUsageRankingSortBy])
	settings.ShowTotalTokens = values[SettingKeyUsageRankingShowTotalTokens] != "false"
	settings.ShowRequests = values[SettingKeyUsageRankingShowRequests] != "false"
	settings.ShowActualCost = values[SettingKeyUsageRankingShowActualCost] != "false"
	settings.Limit = NormalizeUsageRankingLimitString(values[SettingKeyUsageRankingLimit])
	return NormalizeUsageRankingSettings(settings)
}

// GetUsageRankingSettings reads and parses ranking settings in one batch.
func (s *RuntimeSettings) GetUsageRankingSettings(ctx context.Context) (UsageRankingSettings, error) {
	if s == nil || s.settingRepo == nil {
		return DefaultRankingSettings(), nil
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyUsageRankingLimit,
		SettingKeyUsageRankingEnabled,
		SettingKeyUsageRankingSortBy,
		SettingKeyUsageRankingShowTotalTokens,
		SettingKeyUsageRankingShowRequests,
		SettingKeyUsageRankingShowActualCost,
	})
	if err != nil {
		return UsageRankingSettings{}, fmt.Errorf("get usage ranking settings: %w", err)
	}
	return ParseRankingSettings(values), nil
}

// IsUserErrorViewAllowed reads the error-display setting and returns false on a read failure.
func (s *RuntimeSettings) IsUserErrorViewAllowed(ctx context.Context) bool {
	vals, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyAllowUserViewErrorRequests})
	if err != nil {
		slog.Warn("failed to get allow_user_view_error_requests setting, defaulting to false", "error", err)
		return false
	}
	return vals[SettingKeyAllowUserViewErrorRequests] == "true"
}
