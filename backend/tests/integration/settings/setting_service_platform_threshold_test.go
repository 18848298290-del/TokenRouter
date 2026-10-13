package settings_test

import (
	"context"
	"maps"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/settings/composite"
	settingskit "github.com/TokenFlux/TokenRouter/internal/settings/testkit"
)

// Provider scheduling threshold settings retain supported platform values while
// filling missing supported entries with defaults. Map-backed cases compare
// preparation JSON, stored-value parsing, the provider runtime reader,
// omitted-update behavior, invalid historical values, and unavailable
// repository defaults.
//
// The repository fixture protects its values and read observations with a
// mutex. Its reads differ: Get reports missing keys as SettingNotFound, while
// GetValue reports a missing key as empty text with no error. Batch reads omit
// absent keys and return independent maps. Configured single-value errors take
// precedence over stored values after the read attempt has been counted,
// allowing other settings cases to inspect retry and caching observations.

// mockSettingRepo stores mutable settings and read observations for module
// runtime fixture calls.
type mockSettingRepo struct {
	// mu protects data, getValueErr, and getValueCalls during repository methods.
	// Direct test mutations configure those fields before calls can overlap; data
	// is initialized for writes.
	mu   sync.Mutex        // fixture setting access lock
	data map[string]string // retained setting string values
	// getValueErr overrides GetValue after that method increments its observation
	// count. Get and batch reads do not consult this configured single-value
	// failure.
	getValueErr   error
	getValueCalls int // observed single-value read attempts
}

// newSettingServiceForPlatformThresholdTest returns the composite fixture
// configured from a copied seed map. It discards the repository handle, and
// callers use the returned module readers to inspect supported threshold
// values.
func newSettingServiceForPlatformThresholdTest(seed map[string]string) *settingskit.Composite {
	svc, _ := newSettingServiceAndRepoForPlatformThresholdTest(seed)
	return svc
}

// newSettingServiceAndRepoForPlatformThresholdTest returns the composite
// fixture and its newly allocated locked repository. Seed entries are copied
// into repository storage before readers are constructed, and the returned
// repository remains mutable fixture state.
func newSettingServiceAndRepoForPlatformThresholdTest(seed map[string]string) (*settingskit.Composite, *mockSettingRepo) {
	repo := newMockSettingRepo()
	maps.Copy(repo.data, seed)
	return settingskit.NewComposite(repo, &config.Config{}), repo
}

// TestPlatformSchedulingThresholds_RoundTrip_DefaultsAndStoredValues parses
// absent and populated scheduling maps. Missing supported entries use one
// hundred, supplied supported values survive, and the tested Gemini and kiro
// entries are excluded.
func TestPlatformSchedulingThresholds_RoundTrip_DefaultsAndStoredValues(t *testing.T) {
	svc := newSettingServiceForPlatformThresholdTest(nil)

	got := composite.Parse(map[string]string{}, svc.Read)
	require.Equal(t, map[string]int{
		capability.PlatformOpenAI:    100,
		capability.PlatformAnthropic: 100,
		capability.PlatformGrok:      100,
	}, got.ProviderSchedulingThresholds)

	got = composite.Parse(map[string]string{provider.SettingKeyProviderSchedulingThresholds: `{"openai":91,"grok":77,"gemini":85,"kiro":99}`}, svc.Read)
	require.Equal(t, 91, got.ProviderSchedulingThresholds[capability.PlatformOpenAI])
	require.Equal(t, 100, got.ProviderSchedulingThresholds[capability.PlatformAnthropic])
	require.Equal(t, 77, got.ProviderSchedulingThresholds[capability.PlatformGrok])
	require.NotContains(t, got.ProviderSchedulingThresholds, capability.PlatformGemini)
	require.NotContains(t, got.ProviderSchedulingThresholds, "kiro")
}

// TestBuildSystemSettingsUpdates_PersistsProviderSchedulingThresholds prepares
// three supported platform threshold values through the composite settings
// fixture. The resulting repository update value is JSON-equivalent to the
// supplied supported map.
func TestBuildSystemSettingsUpdates_PersistsProviderSchedulingThresholds(t *testing.T) {
	svc := newSettingServiceForPlatformThresholdTest(nil)

	updates, err := composite.Prepare(context.Background(), &composite.Snapshot{
		ProviderSchedulingThresholds: map[string]int{
			capability.PlatformOpenAI:    91,
			capability.PlatformAnthropic: 88,
			capability.PlatformGrok:      77,
		},
	}, svc.Prepare)
	require.NoError(t, err)
	require.JSONEq(t, `{"openai":91,"anthropic":88,"grok":77}`, updates[provider.SettingKeyProviderSchedulingThresholds])
}

// TestValidateAndNormalizeProviderSchedulingThresholds_FillsMissingPlatforms
// supplies just the OpenAI threshold to normalization. It retains that value,
// fills Anthropic and Grok defaults, and excludes the tested unsupported
// platform entries.
func TestValidateAndNormalizeProviderSchedulingThresholds_FillsMissingPlatforms(t *testing.T) {
	normalized, err := provider.ValidateAndNormalizeProviderSchedulingThresholds(map[string]int{
		capability.PlatformOpenAI: 91,
	})
	require.NoError(t, err)
	require.Equal(t, 91, normalized[capability.PlatformOpenAI])
	require.Equal(t, 100, normalized[capability.PlatformAnthropic])
	require.Equal(t, 100, normalized[capability.PlatformGrok])
	require.NotContains(t, normalized, capability.PlatformGemini)
	require.NotContains(t, normalized, "kiro")
	require.NotContains(t, normalized, capability.PlatformAntigravity)
}

// TestValidateAndNormalizeProviderSchedulingThresholds_RejectsUnsupportedPlatforms
// supplies a Gemini entry to the provider threshold validator. The call returns
// an error for that unsupported platform choice in this normalization input.
func TestValidateAndNormalizeProviderSchedulingThresholds_RejectsUnsupportedPlatforms(t *testing.T) {
	_, err := provider.ValidateAndNormalizeProviderSchedulingThresholds(map[string]int{
		capability.PlatformGemini: 85,
	})
	require.Error(t, err)
}

// TestUpdateSettings_StoresProviderSchedulingThresholds saves supported
// threshold values through the locked map repository. Reading the stored JSON
// and parsing it returns those values and excludes kiro.
func TestUpdateSettings_StoresProviderSchedulingThresholds(t *testing.T) {
	svc := newSettingServiceForPlatformThresholdTest(nil)

	err := svc.Save(context.Background(), &composite.Snapshot{
		ProviderSchedulingThresholds: map[string]int{
			capability.PlatformOpenAI:    92,
			capability.PlatformAnthropic: 89,
			capability.PlatformGrok:      76,
		},
	})
	require.NoError(t, err)

	stored, err := svc.Store.GetValue(context.Background(), provider.SettingKeyProviderSchedulingThresholds)
	require.NoError(t, err)
	got := composite.Parse(map[string]string{provider.SettingKeyProviderSchedulingThresholds: stored}, svc.Read)
	require.Equal(t, 92, got.ProviderSchedulingThresholds[capability.PlatformOpenAI])
	require.Equal(t, 89, got.ProviderSchedulingThresholds[capability.PlatformAnthropic])
	require.Equal(t, 76, got.ProviderSchedulingThresholds[capability.PlatformGrok])
	require.NotContains(t, got.ProviderSchedulingThresholds, "kiro")
}

// TestGetProviderSchedulingThresholds_ReadsStoredValue seeds stored OpenAI and
// Grok thresholds plus an unsupported kiro entry. The runtime reader retains
// supported values, fills the missing Anthropic default, and excludes kiro.
func TestGetProviderSchedulingThresholds_ReadsStoredValue(t *testing.T) {
	svc := newSettingServiceForPlatformThresholdTest(map[string]string{provider.SettingKeyProviderSchedulingThresholds: `{"openai":93,"grok":88,"kiro":87}`})

	got := svc.Provider.GetProviderSchedulingThresholds(context.Background())

	require.Equal(t, 93, got[capability.PlatformOpenAI])
	require.Equal(t, 100, got[capability.PlatformAnthropic])
	require.Equal(t, 88, got[capability.PlatformGrok])
	require.NotContains(t, got, "kiro")
}

// TestUpdateSettings_OmittedProviderSchedulingThresholdsDoesNotCacheDefaults
// saves an unrelated frontend URL with threshold values already stored. The
// runtime reader keeps those supported values after the omitted threshold
// update.
func TestUpdateSettings_OmittedProviderSchedulingThresholdsDoesNotCacheDefaults(t *testing.T) {
	svc := newSettingServiceForPlatformThresholdTest(map[string]string{provider.SettingKeyProviderSchedulingThresholds: `{"openai":85,"grok":88,"kiro":87}`})

	err := svc.Save(context.Background(), &composite.Snapshot{
		FrontendURL: "https://example.test",
	})
	require.NoError(t, err)

	got := svc.Provider.GetProviderSchedulingThresholds(context.Background())
	require.Equal(t, 85, got[capability.PlatformOpenAI])
	require.Equal(t, 88, got[capability.PlatformGrok])
	require.NotContains(t, got, "kiro")
}

// TestProviderSchedulingThresholds_InvalidStoredValueUsesSameDefaultsInSettingsAndCache
// supplies an invalid OpenAI threshold beside valid Grok data. Composite
// parsing and the cached reader agree, default OpenAI, retain Grok, and exclude
// kiro.
func TestProviderSchedulingThresholds_InvalidStoredValueUsesSameDefaultsInSettingsAndCache(t *testing.T) {
	svc := newSettingServiceForPlatformThresholdTest(map[string]string{provider.SettingKeyProviderSchedulingThresholds: `{"openai":0,"grok":88,"kiro":87}`})

	settings := composite.Parse(map[string]string{provider.SettingKeyProviderSchedulingThresholds: `{"openai":0,"grok":88,"kiro":87}`}, svc.Read)
	cached := svc.Provider.GetProviderSchedulingThresholds(context.Background())

	require.Equal(t, settings.ProviderSchedulingThresholds, cached)
	require.Equal(t, 100, cached[capability.PlatformOpenAI])
	require.Equal(t, 88, cached[capability.PlatformGrok])
	require.NotContains(t, cached, "kiro")
}

// TestGetProviderSchedulingThresholds_NilRepoReturnsDefaults constructs a
// provider runtime reader with no repository. It returns the expected OpenAI,
// Anthropic, and Grok map with each threshold equal to one hundred.
func TestGetProviderSchedulingThresholds_NilRepoReturnsDefaults(t *testing.T) {
	svc := provider.NewRuntimeSettings(nil, nil)
	got := svc.GetProviderSchedulingThresholds(context.Background())
	require.Equal(t, map[string]int{
		capability.PlatformOpenAI:    100,
		capability.PlatformAnthropic: 100,
		capability.PlatformGrok:      100,
	}, got)
}

// newMockSettingRepo returns a settings repository fixture with newly allocated
// empty string storage.
func newMockSettingRepo() *mockSettingRepo {
	return &mockSettingRepo{data: make(map[string]string)}
}

// Get returns a fresh setting object copied from the locked fixture map.
// Missing keys return ErrSettingNotFound, and the supplied context is ignored
// by this in-memory read.
func (m *mockSettingRepo) Get(_ context.Context, key string) (*settingscore.Setting, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, settingscore.ErrSettingNotFound
	}
	return &settingscore.Setting{Key: key, Value: v}, nil
}

// GetValue counts a single-value read under the mutex before returning
// configured failure or stored text. Missing keys return empty text with no
// error, and the supplied context is ignored.
func (m *mockSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getValueCalls++
	if m.getValueErr != nil {
		return "", m.getValueErr
	}
	v, ok := m.data[key]
	if !ok {
		return "", nil
	}
	return v, nil
}

// Set updates one entry under the fixture mutex and reports success. Callers
// initialize data before writes; the supplied context is ignored, and the map
// retains other setting values.
func (m *mockSettingRepo) Set(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

// GetMultiple copies present requested entries into a newly allocated map under
// the mutex. Absent keys are omitted, duplicate keys address one entry, and
// this fixture reports success while ignoring context.
func (m *mockSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]string)
	for _, k := range keys {
		if v, ok := m.data[k]; ok {
			result[k] = v
		}
	}
	return result, nil
}

// SetMultiple merges the supplied entries into data under the fixture mutex and
// reports success. Existing unrelated keys remain, the caller initializes data
// before writes, and the supplied context is ignored.
func (m *mockSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	maps.Copy(m.data, settings)
	return nil
}

// GetAll returns a newly allocated copy of all fixture values under the mutex
// and reports success. The returned map can be modified independently, and the
// supplied context is ignored.
func (m *mockSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]string, len(m.data))
	maps.Copy(result, m.data)
	return result, nil
}

// Delete removes the supplied key under the fixture mutex and reports success
// even when absent. It ignores context and retains other entries for subsequent
// fixture setting reads.
func (m *mockSettingRepo) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}
