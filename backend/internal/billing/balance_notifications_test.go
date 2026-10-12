package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/notification/contract"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Quota alert cases cover daily, weekly, and total usage.
const (
	quotaDimDaily  = "daily"
	quotaDimWeekly = "weekly"
	quotaDimTotal  = "total"
)

// notifySettingsFixture supplies alert settings for tests.
type notifySettingsFixture struct{ data map[string]string }

// unexpectedAlertSender panics when a test unexpectedly dispatches an alert.
type unexpectedAlertSender struct{}

func TestCheckBalanceAfterDeduction_NilUser(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	// Should not panic.
	s.CheckBalanceAfterDeduction(context.Background(), nil, 100, 50)
}

func TestCheckBalanceAfterDeduction_UserNotifyDisabled(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "true"
	repo.data[SettingKeyBalanceLowNotifyThreshold] = "10"
	u := &UserSummary{ID: 1, BalanceNotifyEnabled: false}
	// Even with a crossing, disabled flag short-circuits.
	s.CheckBalanceAfterDeduction(context.Background(), u, 20, 15)
}

func TestCheckBalanceAfterDeduction_GlobalDisabled(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "false"
	u := &UserSummary{ID: 1, BalanceNotifyEnabled: true}
	s.CheckBalanceAfterDeduction(context.Background(), u, 20, 15)
}

func TestCheckBalanceAfterDeduction_ThresholdZero(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "true"
	repo.data[SettingKeyBalanceLowNotifyThreshold] = "0"
	u := &UserSummary{ID: 1, BalanceNotifyEnabled: true}
	s.CheckBalanceAfterDeduction(context.Background(), u, 20, 15)
}

func TestCheckBalanceAfterDeduction_UserThresholdOverride(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "true"
	repo.data[SettingKeyBalanceLowNotifyThreshold] = "100" // global default
	customThreshold := 5.0
	u := &UserSummary{
		ID:                     1,
		BalanceNotifyEnabled:   true,
		BalanceNotifyThreshold: &customThreshold,
	}
	// User's 5.0 threshold takes precedence over global 100. 20 -> 15 does not
	// cross 5, so nothing fires (verified by absence of panic).
	s.CheckBalanceAfterDeduction(context.Background(), u, 20, 15)
}

func TestCheckBalanceAfterDeduction_NoCrossingNotFired(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "true"
	repo.data[SettingKeyBalanceLowNotifyThreshold] = "10"
	u := &UserSummary{ID: 1, BalanceNotifyEnabled: true}

	// 100 -> 95, both remain above threshold=10, no crossing.
	s.CheckBalanceAfterDeduction(context.Background(), u, 100, 5)
	// 5 -> 3, both already below threshold, no crossing (only fires on first
	// cross from above-to-below).
	s.CheckBalanceAfterDeduction(context.Background(), u, 5, 2)
}

func TestCheckProviderQuotaAfterIncrement_NilProvider(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	// Should not panic.
	s.CheckProviderQuotaAfterIncrement(context.Background(), nil, 10, nil)
}

func TestCheckProviderQuotaAfterIncrement_ZeroCost(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	a := &QuotaNotifyProvider{ID: 1, Platform: capability.PlatformAnthropic}
	s.CheckProviderQuotaAfterIncrement(context.Background(), a, 0, nil)
}

func TestCheckProviderQuotaAfterIncrement_NegativeCost(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	a := &QuotaNotifyProvider{ID: 1, Platform: capability.PlatformAnthropic}
	s.CheckProviderQuotaAfterIncrement(context.Background(), a, -5, nil)
}

func TestCheckProviderQuotaAfterIncrement_GlobalDisabled(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyProviderQuotaNotifyEnabled] = "false"
	a := &QuotaNotifyProvider{
		ID:       1,
		Platform: capability.PlatformAnthropic,
		Dimensions: []QuotaNotifyDimension{
			{Name: "daily", Enabled: true, Threshold: 100, ThresholdType: "fixed", Limit: 1000, CurrentUsed: 950},
		},
	}
	// Global disabled → no processing even if a dim would cross.
	s.CheckProviderQuotaAfterIncrement(context.Background(), a, 100, nil)
}

func TestGetBalanceNotifyConfig_AllFields(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "true"
	repo.data[SettingKeyBalanceLowNotifyThreshold] = "12.5"
	repo.data[SettingKeyBalanceLowNotifyRechargeURL] = "https://example.com/pay"

	enabled, threshold, url := s.GetBalanceNotifyConfig(context.Background())
	require.True(t, enabled)
	require.Equal(t, 12.5, threshold)
	require.Equal(t, "https://example.com/pay", url)
}

func TestGetBalanceNotifyConfig_Disabled(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "false"

	enabled, _, _ := s.GetBalanceNotifyConfig(context.Background())
	require.False(t, enabled)
}

func TestGetBalanceNotifyConfig_InvalidThreshold(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "true"
	repo.data[SettingKeyBalanceLowNotifyThreshold] = "not-a-number"

	enabled, threshold, _ := s.GetBalanceNotifyConfig(context.Background())
	require.True(t, enabled)
	require.Equal(t, 0.0, threshold)
}

func TestIsProviderQuotaNotifyEnabled(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()

	// Missing key → false
	require.False(t, s.IsProviderQuotaNotifyEnabled(context.Background()))

	// Explicit "false"
	repo.data[SettingKeyProviderQuotaNotifyEnabled] = "false"
	require.False(t, s.IsProviderQuotaNotifyEnabled(context.Background()))

	// Explicit "true"
	repo.data[SettingKeyProviderQuotaNotifyEnabled] = "true"
	require.True(t, s.IsProviderQuotaNotifyEnabled(context.Background()))
}

func TestGetSiteName_FallsBackToDefault(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	name := s.GetSiteName(context.Background())
	require.Equal(t, defaultSiteName, name)
}

func TestGetSiteName_Configured(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeySiteName] = "My Site"
	require.Equal(t, "My Site", s.GetSiteName(context.Background()))
}

func TestCheckQuotaDimCrossings_NoDimensions(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
	// Empty dims → no crossing, no panic.
	s.CheckQuotaDimCrossings(provider, nil, 10, []string{"admin@example.com"}, "TestSite")
	s.CheckQuotaDimCrossings(provider, []QuotaNotifyDimension{}, 10, []string{"admin@example.com"}, "TestSite")
}

func TestCheckQuotaDimCrossings_DisabledDimension(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
	dims := []QuotaNotifyDimension{
		{
			Name:          quotaDimDaily,
			Enabled:       false, // disabled
			Threshold:     100,
			ThresholdType: thresholdTypeFixed,
			CurrentUsed:   950,
			Limit:         1000,
		},
	}
	// Disabled dimension should be skipped even if crossing would occur.
	s.CheckQuotaDimCrossings(provider, dims, 50, []string{"admin@example.com"}, "TestSite")
}

func TestCheckQuotaDimCrossings_ZeroThresholdSkipped(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
	dims := []QuotaNotifyDimension{
		{
			Name:          quotaDimDaily,
			Enabled:       true,
			Threshold:     0, // zero threshold
			ThresholdType: thresholdTypeFixed,
			CurrentUsed:   950,
			Limit:         1000,
		},
	}
	// Zero threshold → skipped.
	s.CheckQuotaDimCrossings(provider, dims, 50, []string{"admin@example.com"}, "TestSite")
}

func TestCheckQuotaDimCrossings_NoCrossing_BothBelowThreshold(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
	// threshold=400 remaining, limit=1000 → effectiveThreshold = 600 (usage trigger)
	// currentUsed=300 (after), oldUsed=300-50=250 (before). Both < 600, no crossing.
	dims := []QuotaNotifyDimension{
		{
			Name:          quotaDimDaily,
			Enabled:       true,
			Threshold:     400,
			ThresholdType: thresholdTypeFixed,
			CurrentUsed:   300,
			Limit:         1000,
		},
	}
	s.CheckQuotaDimCrossings(provider, dims, 50, []string{"admin@example.com"}, "TestSite")
}

func TestCheckQuotaDimCrossings_NoCrossing_BothAboveThreshold(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
	// threshold=400 remaining, limit=1000 → effectiveThreshold = 600 (usage trigger)
	// currentUsed=800 (after), oldUsed=800-50=750 (before). Both >= 600, no crossing.
	dims := []QuotaNotifyDimension{
		{
			Name:          quotaDimDaily,
			Enabled:       true,
			Threshold:     400,
			ThresholdType: thresholdTypeFixed,
			CurrentUsed:   800,
			Limit:         1000,
		},
	}
	s.CheckQuotaDimCrossings(provider, dims, 50, []string{"admin@example.com"}, "TestSite")
}

func TestCheckQuotaDimCrossings_NegativeResolvedThreshold_Skipped(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
	// threshold=1200 remaining, limit=1000 → effectiveThreshold = 1000-1200 = -200
	// Negative resolved threshold → skipped.
	dims := []QuotaNotifyDimension{
		{
			Name:          quotaDimDaily,
			Enabled:       true,
			Threshold:     1200,
			ThresholdType: thresholdTypeFixed,
			CurrentUsed:   950,
			Limit:         1000,
		},
	}
	s.CheckQuotaDimCrossings(provider, dims, 50, []string{"admin@example.com"}, "TestSite")
}

func TestCheckQuotaDimCrossings_PercentageThreshold_NoCrossing(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
	// threshold=30%, limit=1000 → effectiveThreshold = 1000 * (1 - 0.30) = 700
	// currentUsed=500, oldUsed=500-50=450. Both < 700, no crossing.
	dims := []QuotaNotifyDimension{
		{
			Name:          quotaDimWeekly,
			Enabled:       true,
			Threshold:     30,
			ThresholdType: thresholdTypePercentage,
			CurrentUsed:   500,
			Limit:         1000,
		},
	}
	s.CheckQuotaDimCrossings(provider, dims, 50, []string{"admin@example.com"}, "TestSite")
}

func TestCheckQuotaDimCrossings_ZeroLimit_Skipped(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
	// limit=0 → resolvedThreshold returns 0 → skipped.
	dims := []QuotaNotifyDimension{
		{
			Name:          quotaDimTotal,
			Enabled:       true,
			Threshold:     100,
			ThresholdType: thresholdTypeFixed,
			CurrentUsed:   50,
			Limit:         0,
		},
	}
	s.CheckQuotaDimCrossings(provider, dims, 50, []string{"admin@example.com"}, "TestSite")
}

func TestCheckQuotaDimCrossings_MultipleDims_MixedResults(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
	// dim1: no crossing (both below effective threshold)
	// dim2: disabled (skipped)
	// dim3: zero threshold (skipped)
	dims := []QuotaNotifyDimension{
		{
			Name:          quotaDimDaily,
			Enabled:       true,
			Threshold:     400,
			ThresholdType: thresholdTypeFixed,
			CurrentUsed:   300, // oldUsed=250, effectiveThreshold=600, both below
			Limit:         1000,
		},
		{
			Name:          quotaDimWeekly,
			Enabled:       false,
			Threshold:     100,
			ThresholdType: thresholdTypeFixed,
			CurrentUsed:   900,
			Limit:         1000,
		},
		{
			Name:          quotaDimTotal,
			Enabled:       true,
			Threshold:     0,
			ThresholdType: thresholdTypeFixed,
			CurrentUsed:   500,
			Limit:         1000,
		},
	}
	// None should trigger. No panic expected.
	s.CheckQuotaDimCrossings(provider, dims, 50, []string{"admin@example.com"}, "TestSite")
}

func TestBuildQuotaDimsFromState_UsesStateValues(t *testing.T) {
	// Use the committed usage returned by settlement.
	a := &QuotaNotifyProvider{
		Dimensions: []QuotaNotifyDimension{
			{Name: "daily", Enabled: true, Threshold: 100, ThresholdType: "fixed", CurrentUsed: 999, Limit: 999},
			{Name: "weekly"},
			{Name: "total"},
		},
	}
	state := &ProviderQuotaState{
		DailyUsed:   77.0,
		DailyLimit:  500.0,
		WeeklyUsed:  88.0,
		WeeklyLimit: 2000.0,
		TotalUsed:   99.0,
		TotalLimit:  10000.0,
	}
	dims := quotaDimsFromCommitted(a, state)
	require.Len(t, dims, 3)
	// Settings from provider (enabled, threshold, thresholdType)
	require.True(t, dims[0].Enabled)
	require.Equal(t, 100.0, dims[0].Threshold)
	// Usage from state
	require.Equal(t, 77.0, dims[0].CurrentUsed)
	require.Equal(t, 500.0, dims[0].Limit)
	require.Equal(t, 88.0, dims[1].CurrentUsed)
	require.Equal(t, 2000.0, dims[1].Limit)
	require.Equal(t, 99.0, dims[2].CurrentUsed)
	require.Equal(t, 10000.0, dims[2].Limit)
}

func TestCollectBalanceNotifyRecipients_Empty(t *testing.T) {
	s := &BalanceNotifyService{}
	u := &UserSummary{BalanceNotifyExtraEmails: nil}
	require.Empty(t, s.CollectBalanceNotifyRecipients(u))
}

func TestCollectBalanceNotifyRecipients_FiltersDisabledAndUnverified(t *testing.T) {
	s := &BalanceNotifyService{}
	u := &UserSummary{
		BalanceNotifyExtraEmails: []NotifyEmailSummary{
			{Email: "a@example.com", Verified: true, Disabled: false},
			{Email: "b@example.com", Verified: true, Disabled: true},   // disabled
			{Email: "c@example.com", Verified: false, Disabled: false}, // unverified
			{Email: "d@example.com", Verified: true, Disabled: false},
		},
	}
	got := s.CollectBalanceNotifyRecipients(u)
	require.Equal(t, []string{"a@example.com", "d@example.com"}, got)
}

func TestCollectBalanceNotifyRecipients_DeduplicatesCaseInsensitive(t *testing.T) {
	s := &BalanceNotifyService{}
	u := &UserSummary{
		BalanceNotifyExtraEmails: []NotifyEmailSummary{
			{Email: "User@Example.com", Verified: true},
			{Email: "user@example.com", Verified: true},
			{Email: "USER@EXAMPLE.COM", Verified: true},
		},
	}
	got := s.CollectBalanceNotifyRecipients(u)
	require.Len(t, got, 1)
	// The original casing of the first entry is preserved.
	require.Equal(t, "User@Example.com", got[0])
}

func TestCollectBalanceNotifyRecipients_SkipsEmpty(t *testing.T) {
	s := &BalanceNotifyService{}
	u := &UserSummary{
		BalanceNotifyExtraEmails: []NotifyEmailSummary{
			{Email: "  ", Verified: true},
			{Email: "", Verified: true},
			{Email: "valid@example.com", Verified: true},
		},
	}
	got := s.CollectBalanceNotifyRecipients(u)
	require.Equal(t, []string{"valid@example.com"}, got)
}

func TestCollectBalanceNotifyRecipients_TrimsWhitespace(t *testing.T) {
	s := &BalanceNotifyService{}
	u := &UserSummary{
		BalanceNotifyExtraEmails: []NotifyEmailSummary{
			{Email: "  trimmed@example.com  ", Verified: true},
		},
	}
	got := s.CollectBalanceNotifyRecipients(u)
	require.Equal(t, []string{"trimmed@example.com"}, got)
}

func (s *notifySettingsFixture) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.data[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (s *notifySettingsFixture) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := s.data[key]; ok {
		return value, nil
	}
	return "", errors.New("setting not found")
}

func (unexpectedAlertSender) SendBalanceLowEmails([]string, int64, string, string, float64, float64, string, string) {
	panic("unexpected balance notification")
}

func (unexpectedAlertSender) SendQuotaAlertEmails([]string, int64, string, string, contract.QuotaDimension, float64, string) {
	panic("unexpected quota notification")
}

func newBalanceNotifyServiceForTest() (*BalanceNotifyService, *notifySettingsFixture) {
	settings := &notifySettingsFixture{data: make(map[string]string)}
	return NewBalanceNotifyService(unexpectedAlertSender{}, settings, nil, func(string, func()) {
		panic("unexpected notification dispatch")
	}), settings
}
