package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/notification/contract"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Quota dimension labels identify the daily, weekly and lifetime windows
// exercised by the alert suppression and committed-state fixture cases.
const (
	quotaDimDaily  = "daily"  // daily quota alert window
	quotaDimWeekly = "weekly" // weekly quota alert window
	quotaDimTotal  = "total"  // lifetime quota alert window
)

// notifySettingsFixture supplies saved alert values for synchronous
// notification selection tests.
type notifySettingsFixture struct {
	// data is edited by the test before synchronous service calls. GetMultiple
	// copies requested values, while GetValue reads this same stable fixture map.
	data map[string]string // saved alert setting values
}

// unexpectedAlertSender panics when a suppressed-alert fixture reaches either
// delivery method.
type unexpectedAlertSender struct{}

// TestCheckBalanceAfterDeduction_NilUser passes no user to post-deduction
// checking with a panic-on-dispatch runner. The call completes without
// dispatching or dereferencing a user snapshot.
func TestCheckBalanceAfterDeduction_NilUser(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	s.CheckBalanceAfterDeduction(context.Background(), nil, 100, 50)
}

// TestCheckBalanceAfterDeduction_UserNotifyDisabled crosses an enabled global
// balance threshold for a user who disabled notices. The panic-on-dispatch
// fixture protects the user switch's suppression of that alert.
func TestCheckBalanceAfterDeduction_UserNotifyDisabled(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "true"
	repo.data[SettingKeyBalanceLowNotifyThreshold] = "10"
	u := &UserSummary{ID: 1, BalanceNotifyEnabled: false}
	s.CheckBalanceAfterDeduction(context.Background(), u, 20, 15)
}

// TestCheckBalanceAfterDeduction_GlobalDisabled enables user notices while
// disabling the global balance switch. The call completes with the
// panic-on-dispatch fixture, protecting global suppression for this
// observation.
func TestCheckBalanceAfterDeduction_GlobalDisabled(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "false"
	u := &UserSummary{ID: 1, BalanceNotifyEnabled: true}
	s.CheckBalanceAfterDeduction(context.Background(), u, 20, 15)
}

// TestCheckBalanceAfterDeduction_ThresholdZero enables balance notices with a
// zero global threshold. The panic-on-dispatch fixture protects suppression of
// a deduction when no positive threshold is available.
func TestCheckBalanceAfterDeduction_ThresholdZero(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "true"
	repo.data[SettingKeyBalanceLowNotifyThreshold] = "0"
	u := &UserSummary{ID: 1, BalanceNotifyEnabled: true}
	s.CheckBalanceAfterDeduction(context.Background(), u, 20, 15)
}

// TestCheckBalanceAfterDeduction_UserThresholdOverride supplies a user
// threshold of five and a global threshold of one hundred. The deduction ends
// at five, and the panic-on-dispatch fixture requires no send for this input.
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
	s.CheckBalanceAfterDeduction(context.Background(), u, 20, 15)
}

// TestCheckBalanceAfterDeduction_NoCrossingNotFired exercises deductions
// staying above the trigger or already below it. The panic-on-dispatch fixture
// protects suppression of both non-crossing observations.
func TestCheckBalanceAfterDeduction_NoCrossingNotFired(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "true"
	repo.data[SettingKeyBalanceLowNotifyThreshold] = "10"
	u := &UserSummary{ID: 1, BalanceNotifyEnabled: true}

	s.CheckBalanceAfterDeduction(context.Background(), u, 100, 5)
	s.CheckBalanceAfterDeduction(context.Background(), u, 5, 2)
}

// TestCheckProviderQuotaAfterIncrement_NilProvider passes no provider to
// post-increment checking. The call completes with the panic-on-dispatch
// fixture, protecting the absent-provider guard before quota processing.
func TestCheckProviderQuotaAfterIncrement_NilProvider(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	s.CheckProviderQuotaAfterIncrement(context.Background(), nil, 10, nil)
}

// TestCheckProviderQuotaAfterIncrement_ZeroCost passes a provider with a zero
// increment. The call completes with the panic-on-dispatch fixture, protecting
// the positive-cost gate on quota notifications.
func TestCheckProviderQuotaAfterIncrement_ZeroCost(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	a := &QuotaNotifyProvider{ID: 1, Platform: capability.PlatformAnthropic}
	s.CheckProviderQuotaAfterIncrement(context.Background(), a, 0, nil)
}

// TestCheckProviderQuotaAfterIncrement_NegativeCost passes a provider with a
// negative increment. The call completes with the panic-on-dispatch fixture,
// protecting rejection of a nonpositive notification cost.
func TestCheckProviderQuotaAfterIncrement_NegativeCost(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	a := &QuotaNotifyProvider{ID: 1, Platform: capability.PlatformAnthropic}
	s.CheckProviderQuotaAfterIncrement(context.Background(), a, -5, nil)
}

// TestCheckProviderQuotaAfterIncrement_GlobalDisabled supplies a dimension
// that crosses its trigger while global quota alerts are disabled. The
// panic-on-dispatch fixture protects global suppression of that eligible
// crossing.
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
	s.CheckProviderQuotaAfterIncrement(context.Background(), a, 100, nil)
}

// TestGetBalanceNotifyConfig_AllFields stores an enabled switch, decimal
// threshold and recharge URL. It checks all three returned fields so one
// settings read supplies the intended alert configuration.
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

// TestGetBalanceNotifyConfig_Disabled stores the false switch in the settings
// fixture. It checks that the resulting configuration remains disabled when
// the other fields are absent.
func TestGetBalanceNotifyConfig_Disabled(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "false"

	enabled, _, _ := s.GetBalanceNotifyConfig(context.Background())
	require.False(t, enabled)
}

// TestGetBalanceNotifyConfig_InvalidThreshold stores an enabled switch and
// malformed amount text. It checks that enablement remains true while the
// unusable threshold becomes zero for later suppression.
func TestGetBalanceNotifyConfig_InvalidThreshold(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeyBalanceLowNotifyEnabled] = "true"
	repo.data[SettingKeyBalanceLowNotifyThreshold] = "not-a-number"

	enabled, threshold, _ := s.GetBalanceNotifyConfig(context.Background())
	require.True(t, enabled)
	require.Equal(t, 0.0, threshold)
}

// TestIsProviderQuotaNotifyEnabled reads an absent switch, then stored false
// and true values. It checks that only the supplied true value enables quota
// notifications in these cases.
func TestIsProviderQuotaNotifyEnabled(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()

	require.False(t, s.IsProviderQuotaNotifyEnabled(context.Background()))

	repo.data[SettingKeyProviderQuotaNotifyEnabled] = "false"
	require.False(t, s.IsProviderQuotaNotifyEnabled(context.Background()))

	repo.data[SettingKeyProviderQuotaNotifyEnabled] = "true"
	require.True(t, s.IsProviderQuotaNotifyEnabled(context.Background()))
}

// TestGetSiteName_FallsBackToDefault leaves alert branding absent from the
// settings fixture. It checks the built-in site name supplies usable branding
// after the missing-setting read.
func TestGetSiteName_FallsBackToDefault(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	name := s.GetSiteName(context.Background())
	require.Equal(t, defaultSiteName, name)
}

// TestGetSiteName_Configured stores a custom site name for alert branding. It
// checks the supplied label is returned so rendered notices can use the
// configured name.
func TestGetSiteName_Configured(t *testing.T) {
	s, repo := newBalanceNotifyServiceForTest()
	repo.data[SettingKeySiteName] = "My Site"
	require.Equal(t, "My Site", s.GetSiteName(context.Background()))
}

// TestCheckQuotaDimCrossings_NoDimensions passes both nil and allocated empty
// dimension lists. The panic-on-dispatch fixture checks that neither list
// produces an alert for the supplied provider.
func TestCheckQuotaDimCrossings_NoDimensions(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
	s.CheckQuotaDimCrossings(provider, nil, 10, []string{"admin@example.com"}, "TestSite")
	s.CheckQuotaDimCrossings(provider, []QuotaNotifyDimension{}, 10, []string{"admin@example.com"}, "TestSite")
}

// TestCheckQuotaDimCrossings_DisabledDimension supplies a disabled daily
// dimension with prior usage already at its trigger. The panic-on-dispatch
// fixture requires no send for this input; the case does not isolate
// enablement from the crossing predicate.
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
	s.CheckQuotaDimCrossings(provider, dims, 50, []string{"admin@example.com"}, "TestSite")
}

// TestCheckQuotaDimCrossings_ZeroThresholdSkipped enables a dimension with a
// zero configured threshold. The panic-on-dispatch fixture checks that this
// absent positive trigger produces no alert.
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
	s.CheckQuotaDimCrossings(provider, dims, 50, []string{"admin@example.com"}, "TestSite")
}

// TestCheckQuotaDimCrossings_NoCrossing_BothBelowThreshold reconstructs old
// and new usage below a fixed used-quota trigger. The panic-on-dispatch
// fixture checks that the increment does not produce a crossing.
func TestCheckQuotaDimCrossings_NoCrossing_BothBelowThreshold(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
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

// TestCheckQuotaDimCrossings_NoCrossing_BothAboveThreshold reconstructs old
// and new usage already above the fixed trigger. The panic-on-dispatch fixture
// protects suppression of a repeated after-trigger increment.
func TestCheckQuotaDimCrossings_NoCrossing_BothAboveThreshold(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
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

// TestCheckQuotaDimCrossings_NegativeResolvedThreshold_Skipped configures
// remaining quota greater than its limit. The panic-on-dispatch fixture checks
// that the resulting negative used trigger suppresses this quota notice.
func TestCheckQuotaDimCrossings_NegativeResolvedThreshold_Skipped(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
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

// TestCheckQuotaDimCrossings_PercentageThreshold_NoCrossing keeps
// reconstructed usage below the percentage-derived trigger. The
// panic-on-dispatch fixture checks that this enabled dimension does not
// produce an alert.
func TestCheckQuotaDimCrossings_PercentageThreshold_NoCrossing(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
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

// TestCheckQuotaDimCrossings_ZeroLimit_Skipped supplies a dimension with no
// positive quota limit. The panic-on-dispatch fixture checks that its zero
// computed trigger suppresses notification.
func TestCheckQuotaDimCrossings_ZeroLimit_Skipped(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
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

// TestCheckQuotaDimCrossings_MultipleDims_MixedResults combines a non-crossing
// dimension, a disabled dimension and a zero threshold. The panic-on-dispatch
// fixture checks that the mixed list produces no sends.
func TestCheckQuotaDimCrossings_MultipleDims_MixedResults(t *testing.T) {
	s, _ := newBalanceNotifyServiceForTest()
	provider := &QuotaNotifyProvider{ID: 1, Name: "test", Platform: capability.PlatformAnthropic}
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
	s.CheckQuotaDimCrossings(provider, dims, 50, []string{"admin@example.com"}, "TestSite")
}

// TestBuildQuotaDimsFromState_UsesStateValues supplies provider alert settings
// alongside committed daily, weekly and total usage. It checks returned
// usage/limits come from the transaction state while the asserted daily
// settings remain configured.
func TestBuildQuotaDimsFromState_UsesStateValues(t *testing.T) {
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
	require.True(t, dims[0].Enabled)
	require.Equal(t, 100.0, dims[0].Threshold)
	require.Equal(t, 77.0, dims[0].CurrentUsed)
	require.Equal(t, 500.0, dims[0].Limit)
	require.Equal(t, 88.0, dims[1].CurrentUsed)
	require.Equal(t, 2000.0, dims[1].Limit)
	require.Equal(t, 99.0, dims[2].CurrentUsed)
	require.Equal(t, 10000.0, dims[2].Limit)
}

// TestCollectBalanceNotifyRecipients_Empty supplies no saved extra contacts
// for a user. It checks the collected recipient list is empty so no address is
// fabricated by selection.
func TestCollectBalanceNotifyRecipients_Empty(t *testing.T) {
	s := &BalanceNotifyService{}
	u := &UserSummary{BalanceNotifyExtraEmails: nil}
	require.Empty(t, s.CollectBalanceNotifyRecipients(u))
}

// TestCollectBalanceNotifyRecipients_FiltersDisabledAndUnverified mixes
// verified, disabled and unverified saved contacts. It checks that the two
// verified enabled addresses remain in their supplied order.
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

// TestCollectBalanceNotifyRecipients_DeduplicatesCaseInsensitive supplies
// three spellings of the same verified address. It checks one recipient
// remains with the first accepted spelling for delivery.
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
	require.Equal(t, "User@Example.com", got[0])
}

// TestCollectBalanceNotifyRecipients_SkipsEmpty mixes verified empty and
// whitespace-only addresses with a valid address. It checks only the nonempty
// trimmed mailbox remains eligible.
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

// TestCollectBalanceNotifyRecipients_TrimsWhitespace supplies one verified
// address surrounded by spaces. It checks the recipient uses the trimmed
// mailbox text before downstream delivery.
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

// GetMultiple copies present requested keys into a new settings result map.
// Missing keys are omitted and no error is injected, so callers can observe
// per-field defaults in these alert tests.
func (s *notifySettingsFixture) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.data[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

// GetValue returns the saved setting or a missing-setting error. The empty
// companion value on absence exercises notification defaults; configured
// values are returned directly without normalization.
func (s *notifySettingsFixture) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := s.data[key]; ok {
		return value, nil
	}
	return "", errors.New("setting not found")
}

// SendBalanceLowEmails panics when an alert test reaches a balance send. These
// cases expect suppressed dispatch, so the panic identifies a selected balance
// event outside their expected path.
func (unexpectedAlertSender) SendBalanceLowEmails([]string, int64, string, string, float64, float64, string, string) {
	panic("unexpected balance notification")
}

// SendQuotaAlertEmails panics when an alert test reaches a quota send. These
// cases exercise suppressed quota decisions, so the panic identifies an
// unexpected delivery attempt for this fixture.
func (unexpectedAlertSender) SendQuotaAlertEmails([]string, int64, string, string, contract.QuotaDimension, float64, string) {
	panic("unexpected quota notification")
}

// newBalanceNotifyServiceForTest returns a service and editable settings
// fixture for suppressed-alert tests. Both its sender and runner panic on
// dispatch, while the absent provider reader leaves snapshot-based quota
// checks available.
func newBalanceNotifyServiceForTest() (*BalanceNotifyService, *notifySettingsFixture) {
	settings := &notifySettingsFixture{data: make(map[string]string)}
	return NewBalanceNotifyService(unexpectedAlertSender{}, settings, nil, func(string, func()) {
		panic("unexpected notification dispatch")
	}), settings
}
