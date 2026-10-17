package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBalanceAlertConfiguration checks saved defaults, invalid inputs, and wallet eligibility.
// It keeps fractional thresholds valid while rejecting malformed policies before any
// provider is persisted or a periodic query can be scheduled.
func TestBalanceAlertConfiguration(t *testing.T) {
	p := &Record{Type: ProviderTypeAPIKey, Platform: PlatformDeepseek}
	c, err := BalanceAlertConfiguration(p)
	require.NoError(t, err)
	require.Equal(t, BalanceAlertConfig{Threshold: 1000, IntervalMinutes: 60}, c)
	for _, raw := range []any{nil, "bad", map[string]any{"enabled": true}, map[string]any{"threshold": -1}, map[string]any{"interval_minutes": 4}, map[string]any{"interval_minutes": 1441}, map[string]any{"interval_minutes": 5.5}, map[string]any{"extra": true}, map[string]any{"threshold": nil}} {
		p.Extra = map[string]any{UpstreamBalanceAlertExtraKey: raw}
		_, err := BalanceAlertConfiguration(p)
		require.Error(t, err)
	}
	p.Extra = map[string]any{UpstreamBalanceAlertExtraKey: BalanceAlertConfig{Enabled: true, Threshold: 1000.5, IntervalMinutes: 5, RecipientEmail: "alerts@example.com"}}
	_, err = BalanceAlertConfiguration(p)
	require.NoError(t, err)
	p.Type = ProviderTypeOAuth
	_, err = BalanceAlertConfiguration(p)
	require.Error(t, err)
	p.Type, p.Platform = ProviderTypeAPIKey, PlatformKimi
	p.Credentials = map[string]any{"provider_mode": "coding"}
	_, err = BalanceAlertConfiguration(p)
	require.Error(t, err)
}

// TestBalanceAlertConfigurationMerge checks reminder retention during unrelated edits.
// The locked record's reminder policy survives an omitted object, while explicit
// invalid replacements are rejected before storage can persist them.
func TestBalanceAlertConfigurationMerge(t *testing.T) {
	c := BalanceAlertConfig{Enabled: true, Threshold: 1000, IntervalMinutes: 60, RecipientEmail: "alerts@example.com"}
	current := &Record{Type: ProviderTypeAPIKey, Platform: PlatformDeepseek, Extra: map[string]any{UpstreamBalanceAlertExtraKey: c}}
	desired := CloneRecord(current)
	desired.Extra = map[string]any{"quota_limit": 100}
	merged, err := ApplyConfigurationChange(current, desired, ConfigurationChange{Fields: ConfigExtra, PreserveExtraKeys: []string{UpstreamBalanceAlertExtraKey}})
	require.NoError(t, err)
	actual, err := BalanceAlertConfiguration(merged)
	require.NoError(t, err)
	require.Equal(t, c, actual)
	desired.Extra[UpstreamBalanceAlertExtraKey] = map[string]any{"enabled": true, "recipient_email": "invalid"}
	_, err = ApplyConfigurationChange(current, desired, ConfigurationChange{Fields: ConfigExtra})
	require.ErrorIs(t, err, ErrBalanceAlertConfigInvalid)
}

// TestBalanceAlertConfigurationDerouter accepts account balance reminders with
// an implicit management root. Reminder validation uses the registered wallet
// adapter and keeps the administrator's threshold and delivery settings.
func TestBalanceAlertConfigurationDerouter(t *testing.T) {
	p := &Record{
		Type: ProviderTypeAPIKey, Platform: PlatformAnthropic,
		Extra: map[string]any{
			UpstreamUsageQueryExtraKey: map[string]any{"adapter": UpstreamUsageAdapterDerouter},
			UpstreamBalanceAlertExtraKey: map[string]any{
				"enabled": true, "threshold": 100.0, "interval_minutes": 60, "recipient_email": "ops@example.com",
			},
		},
	}
	config, err := BalanceAlertConfiguration(p)
	require.NoError(t, err)
	require.True(t, config.Enabled)
	require.Equal(t, 100.0, config.Threshold)
	require.Equal(t, "ops@example.com", config.RecipientEmail)
}
