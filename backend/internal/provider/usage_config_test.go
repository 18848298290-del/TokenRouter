package provider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Usage configuration tests exercise default inheritance, scalar normalization
// and rejection of credential or template fields. The same fixture also checks
// unsafe root formatting and the ordered administrator adapter choices, tying
// effective reads to persisted configuration validation.

// TestEffectiveUpstreamUsageConfigDefaultsAndNormalization exercises absent,
// disabled and malformed usage choices alongside adapter selection. It checks
// normalized fields, structured errors for unsupported and sensitive input,
// unsafe URL rejection and manual catalog order.
func TestEffectiveUpstreamUsageConfigDefaultsAndNormalization(t *testing.T) {
	provider := &Record{Type: capability.ProviderTypeAPIKey}
	config, err := EffectiveUpstreamUsageConfig(provider)
	require.NoError(t, err)
	require.Equal(t, UpstreamUsageQueryConfig{Enabled: true, Adapter: UpstreamUsageAdapterSub2API}, config)

	extra := map[string]any{UpstreamUsageQueryExtraKey: map[string]any{
		"enabled":  false,
		"adapter":  UpstreamUsageAdapterNewAPI,
		"base_url": "https://usage.example/v1",
	}}
	require.NoError(t, NormalizeUpstreamUsageExtra(extra))
	require.Equal(t, map[string]any{
		"enabled":  false,
		"adapter":  UpstreamUsageAdapterNewAPI,
		"base_url": "https://usage.example/v1",
	}, extra[UpstreamUsageQueryExtraKey])

	bad := map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"api_key": "secret"}}
	require.Error(t, NormalizeUpstreamUsageExtra(bad))

	disabledProvider := &Record{Extra: map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"enabled": false}}}
	disabled, err := EffectiveUpstreamUsageConfig(disabledProvider)
	require.NoError(t, err)
	require.False(t, disabled.Enabled)
	require.Equal(t, UpstreamUsageAdapterSub2API, disabled.Adapter)

	unknown := map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"adapter": "custom-script"}}
	require.ErrorIs(t, NormalizeUpstreamUsageExtra(unknown), ErrUpstreamUsageConfigInvalid)
	_, err = EffectiveUpstreamUsageConfig(&Record{Extra: unknown})
	require.ErrorIs(t, err, ErrUpstreamUsageUnsupported)

	unsafeURL := map[string]any{UpstreamUsageQueryExtraKey: map[string]any{
		"base_url": "https://user:secret@gateway.example/v1?token=secret",
	}}
	require.ErrorIs(t, NormalizeUpstreamUsageExtra(unsafeURL), ErrUpstreamUsageConfigInvalid)
	_, ok := NormalizedUpstreamUsageConfigValue(map[string]any{"api_key": "secret"})
	require.False(t, ok)

	require.Equal(t, []UpstreamUsageAdapterOption{
		{Name: UpstreamUsageAdapterSub2API, Label: "Sub2API / TokenRouter"},
		{Name: UpstreamUsageAdapterNewAPI, Label: "New API"},
		{Name: UpstreamUsageAdapterZivv, Label: "Zivv"},
		{Name: UpstreamUsageAdapterDerouter, Label: "derouter.ai (Account Key)"},
	}, UpstreamUsageAdapterOptions())
}

// TestEffectiveUpstreamUsageConfigDerouter resolves an omitted management root
// and a custom override. Saved configuration and forwarding credentials remain
// unchanged while the effective query uses the correct account endpoint.
func TestEffectiveUpstreamUsageConfigDerouter(t *testing.T) {
	for _, override := range []string{"", "https://management.example.test/prefix"} {
		extra := map[string]any{UpstreamUsageQueryExtraKey: map[string]any{"adapter": UpstreamUsageAdapterDerouter, "base_url": override}}
		require.NoError(t, NormalizeUpstreamUsageExtra(extra))
		p := &Record{Type: ProviderTypeAPIKey, Platform: PlatformAnthropic, Credentials: map[string]any{"base_url": "https://forward.example.test/anthropic"}, Extra: extra}
		original := CloneRecord(p)
		got, err := EffectiveUpstreamUsageConfig(p)
		require.NoError(t, err)
		want := override
		if want == "" {
			want = DerouterUsageDefaultBaseURL
		}
		require.Equal(t, want, got.BaseURL)
		require.Equal(t, UpstreamUsageAdapterDerouter, got.Adapter)
		require.Equal(t, original, p)
		require.True(t, BalanceAlertSupported(p))
	}
}
