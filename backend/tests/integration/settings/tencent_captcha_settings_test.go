package settings_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	settingskit "github.com/TokenFlux/TokenRouter/internal/settings/testkit"
)

// Tencent CAPTCHA settings supply private credentials to the identity reader
// while public settings serialize the enabled flag, app ID, and site region.
// Map-backed cases inspect configured flags and retained runtime secret values,
// public JSON exclusion of three secret values, and the China-region default
// when no region is supplied.

// settingPublicRepoStub exposes selected batch reads for public and private
// CAPTCHA settings cases.
type settingPublicRepoStub struct {
	// values supplies requested keys after the configured error check. Returned
	// maps copy present strings, and callers serialize direct mutations of this
	// fixture storage.
	values map[string]string
	// err takes precedence over all configured values during GetMultiple. A nonnil
	// error returns nil data before this fixture attempts to copy any requested
	// setting.
	err error
}

// TestSettingService_ParseSettingsMasksTencentCaptchaCredentials loads enabled
// Tencent CAPTCHA credentials through the runtime snapshot. It reports
// configured flags and retains each supplied private value in the returned
// in-memory snapshot.
func TestSettingService_ParseSettingsMasksTencentCaptchaCredentials(t *testing.T) {
	svc := settingskit.NewComposite(&settingGetAllRepoStub{values: map[string]string{
		identity.SettingKeyTencentCaptchaEnabled:        "true",
		identity.SettingKeyTencentCaptchaAppID:          "123456789",
		identity.SettingKeyTencentCaptchaAppSecretKey:   "app-secret",
		identity.SettingKeyTencentCaptchaCloudSecretID:  "cloud-secret-id",
		identity.SettingKeyTencentCaptchaCloudSecretKey: "cloud-secret-key",
	}}, &config.Config{})

	settings, err := svc.Runtime.GetAllSettings(context.Background())

	require.NoError(t, err)
	require.True(t, settings.TencentCaptchaEnabled)
	require.Equal(t, "123456789", settings.TencentCaptchaAppID)
	require.True(t, settings.TencentCaptchaAppSecretKeyConfigured)
	require.True(t, settings.TencentCaptchaCloudSecretIDConfigured)
	require.True(t, settings.TencentCaptchaCloudSecretKeyConfigured)
	require.Equal(t, "app-secret", settings.TencentCaptchaAppSecretKey)
	require.Equal(t, "cloud-secret-id", settings.TencentCaptchaCloudSecretID)
	require.Equal(t, "cloud-secret-key", settings.TencentCaptchaCloudSecretKey)
}

// TestSettingService_GetPublicSettingsExposesOnlyTencentCaptchaAppID supplies
// Tencent credentials and international-region configuration to the public
// reader. App ID, enabled state, and region are returned, while serialized JSON
// excludes the supplied secret values.
func TestSettingService_GetPublicSettingsExposesOnlyTencentCaptchaAppID(t *testing.T) {
	svc := settingskit.NewComposite(&settingPublicRepoStub{values: map[string]string{
		identity.SettingKeyTencentCaptchaEnabled:        "true",
		identity.SettingKeyTencentCaptchaAppID:          "123456789",
		identity.SettingKeyTencentCaptchaAppSecretKey:   "app-secret",
		identity.SettingKeyTencentCaptchaCloudSecretID:  "cloud-secret-id",
		identity.SettingKeyTencentCaptchaCloudSecretKey: "cloud-secret-key",
		identity.SettingKeyTencentCaptchaRegion:         identity.TencentCaptchaRegionINTL,
	}}, &config.Config{})

	settings, err := svc.Public.GetPublicSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.TencentCaptchaEnabled)
	require.Equal(t, "123456789", settings.TencentCaptchaAppID)
	// The public region lets the frontend choose the matching SDK script and
	// constructor.
	require.Equal(t, identity.TencentCaptchaRegionINTL, settings.TencentCaptchaRegion)

	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "app-secret")
	require.NotContains(t, string(raw), "cloud-secret-id")
	require.NotContains(t, string(raw), "cloud-secret-key")
}

// TestSettingService_GetTencentCaptchaConfig reads the enabled Tencent CAPTCHA
// fixture and its private credential values. The returned configuration equals
// those values with an unspecified site defaulted to China.
func TestSettingService_GetTencentCaptchaConfig(t *testing.T) {
	repo := &settingPublicRepoStub{values: map[string]string{
		identity.SettingKeyTencentCaptchaEnabled:        "true",
		identity.SettingKeyTencentCaptchaAppID:          "123456789",
		identity.SettingKeyTencentCaptchaAppSecretKey:   "app-secret",
		identity.SettingKeyTencentCaptchaCloudSecretID:  "cloud-secret-id",
		identity.SettingKeyTencentCaptchaCloudSecretKey: "cloud-secret-key",
	}}
	svc := settingskit.NewComposite(repo, &config.Config{})

	got := svc.Identity.GetTencentCaptchaConfig(context.Background())

	require.Equal(t, identity.TencentCaptchaConfig{
		Enabled:        true,
		AppID:          "123456789",
		AppSecretKey:   "app-secret",
		CloudSecretID:  "cloud-secret-id",
		CloudSecretKey: "cloud-secret-key",
		// An unspecified region defaults to the China site.
		Region: identity.TencentCaptchaRegionCN,
	}, got)
}

// Get panics when a public CAPTCHA fixture unexpectedly requests this
// unsupported repository operation.
func (s *settingPublicRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

// GetValue panics when a public CAPTCHA fixture unexpectedly requests this
// unsupported repository operation.
func (s *settingPublicRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	panic("unexpected GetValue call")
}

// Set panics when a public CAPTCHA fixture unexpectedly requests this
// unsupported repository operation.
func (s *settingPublicRepoStub) Set(ctx context.Context, key, value string) error {
	panic("unexpected Set call")
}

// GetMultiple returns a fresh map containing present requested settings or the
// configured error before reading values. Missing keys are omitted, strings are
// copied, and this fixture ignores ctx.
func (s *settingPublicRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

// SetMultiple panics when a public CAPTCHA fixture unexpectedly requests this
// unsupported repository operation.
func (s *settingPublicRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

// GetAll panics when a public CAPTCHA fixture unexpectedly requests this
// unsupported repository operation.
func (s *settingPublicRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

// Delete panics when a public CAPTCHA fixture unexpectedly requests this
// unsupported repository operation.
func (s *settingPublicRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}
