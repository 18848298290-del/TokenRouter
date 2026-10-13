package usage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// Ranking settings can be absent from storage or contain visibility choices
// inconsistent with their sort metric. These cases check compatible defaults,
// maximum-limit normalization and forced visibility of the selected cost
// metric, alongside the stable user-error setting key.

// usageRankingSettingsRepoStub returns configured setting strings for
// RuntimeSettings read tests.
type usageRankingSettingsRepoStub struct {
	settingscore.Repository                   // unused settings repository method slots
	values                  map[string]string // configured persisted setting text map
}

// GetMultiple returns the configured entries for the requested keys in a new
// map. Missing keys stay absent, allowing RuntimeSettings to apply its
// read-time defaults in each fixture.
func (s *usageRankingSettingsRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

// TestGetUsageRankingSettingsUsesCompatibleDefaults reads an empty settings
// map. It requires enabled ranking, token sorting, all metric visibility
// flags and the configured compatible default limit.
func TestGetUsageRankingSettingsUsesCompatibleDefaults(t *testing.T) {
	svc := NewRuntimeSettings(&usageRankingSettingsRepoStub{values: map[string]string{}})

	settings, err := svc.GetUsageRankingSettings(context.Background())

	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, UsageRankingSortByTotalTokens, settings.SortBy)
	require.True(t, settings.ShowTotalTokens)
	require.True(t, settings.ShowRequests)
	require.True(t, settings.ShowActualCost)
	require.Equal(t, DefaultUsageRankingLimit, settings.Limit)
}

// TestGetUsageRankingSettingsForcesSortMetricVisible stores cost sorting with
// all metrics hidden and an oversized limit. It requires only cost visibility
// to become true and the limit to cap.
func TestGetUsageRankingSettingsForcesSortMetricVisible(t *testing.T) {
	svc := NewRuntimeSettings(&usageRankingSettingsRepoStub{values: map[string]string{
		SettingKeyUsageRankingSortBy:          string(UsageRankingSortByActualCost),
		SettingKeyUsageRankingShowTotalTokens: "false",
		SettingKeyUsageRankingShowRequests:    "false",
		SettingKeyUsageRankingShowActualCost:  "false",
		SettingKeyUsageRankingLimit:           "999",
	}})

	settings, err := svc.GetUsageRankingSettings(context.Background())

	require.NoError(t, err)
	require.Equal(t, UsageRankingSortByActualCost, settings.SortBy)
	require.False(t, settings.ShowTotalTokens)
	require.False(t, settings.ShowRequests)
	require.True(t, settings.ShowActualCost)
	require.Equal(t, MaxUsageRankingLimit, settings.Limit)
}

// TestSettingKeyAllowUserViewErrorRequests_Constant reads the exported
// user-error setting key. It requires its persisted text to equal the
// expected literal used by settings storage.
func TestSettingKeyAllowUserViewErrorRequests_Constant(t *testing.T) {
	if SettingKeyAllowUserViewErrorRequests != "allow_user_view_error_requests" {
		t.Fatalf("unexpected key: %s", SettingKeyAllowUserViewErrorRequests)
	}
}
