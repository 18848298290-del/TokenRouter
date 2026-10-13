package usage

import (
	"context"
	"fmt"
	"log/slog"
)

// Missing ranking settings keep the public ranking enabled with all counters
// visible. Stored false values opt out of individual options, while viewing
// error records uses the stricter exact-true rule and returns false when
// settings cannot be read.

// These persisted keys connect administrator settings writes with runtime
// ranking visibility, ordering, row-limit, and user error-view reads.
const (
	SettingKeyAllowUserViewErrorRequests  = "allow_user_view_error_requests"
	SettingKeyUsageRankingEnabled         = "usage_ranking_enabled"
	SettingKeyUsageRankingLimit           = "usage_ranking_limit"
	SettingKeyUsageRankingShowActualCost  = "usage_ranking_show_actual_cost"
	SettingKeyUsageRankingShowRequests    = "usage_ranking_show_requests"
	SettingKeyUsageRankingShowTotalTokens = "usage_ranking_show_total_tokens"
	SettingKeyUsageRankingSortBy          = "usage_ranking_sort_by"
)

// RuntimeSettingsStore supplies the persisted text values needed for usage
// display decisions.
type RuntimeSettingsStore interface {
	// GetMultiple returns persisted text for requested keys in one repository
	// call. Missing keys may be absent; ParseRankingSettings then selects defaults
	// while exact boolean text remains available for its comparisons.
	GetMultiple(context.Context, []string) (map[string]string, error)
}

// RuntimeSettings reads and parses ranking and user error-display settings.
type RuntimeSettings struct {
	settingRepo RuntimeSettingsStore /* persisted usage settings reader */
}

// NewRuntimeSettings binds a repository for call-time usage setting reads.
func NewRuntimeSettings(repo RuntimeSettingsStore) *RuntimeSettings {
	return &RuntimeSettings{settingRepo: repo}
}

// DefaultRankingSettings returns the enabled public ranking with every counter
// visible.
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

// ParseRankingSettings parses persisted ranking text and normalizes the
// resulting display choices. Only the exact string false disables boolean
// settings; missing values retain defaults, and the selected sorting metric
// becomes visible.
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

// GetUsageRankingSettings reads the six ranking settings in one batch and
// applies normalization. A nil service or repository returns defaults;
// configured repository errors are returned to the caller.
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

// IsUserErrorViewAllowed reports whether the persisted error-view switch is
// exactly true. Callers supply a service with a repository; a read failure is
// logged and denies visibility for this request.
func (s *RuntimeSettings) IsUserErrorViewAllowed(ctx context.Context) bool {
	vals, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyAllowUserViewErrorRequests})
	if err != nil {
		// A settings failure cannot establish permission to expose error records.
		// Report the read failure and deny this optional view for the current request.
		slog.Warn("failed to get allow_user_view_error_requests setting, defaulting to false", "error", err)
		return false
	}
	return vals[SettingKeyAllowUserViewErrorRequests] == "true"
}
