package provider

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

// Persisted provider policies can be missing, malformed, or outside supported
// ranges even when their JSON decodes. These fixtures exercise real runtime
// readers and setters against controlled stores, checking complete defaults,
// active-policy validation, inactive-policy normalization, and encoded field
// names without invoking health actions.

// errCooldownSettingMissing lets cooldown fixtures distinguish an absent
// policy key from storage success and selects each getter's built-in default
// handling.
var errCooldownSettingMissing = errors.New("setting missing")

// mockSettingRepo provides synchronized settings storage and controlled read
// observations for policy tests.
type mockSettingRepo struct {
	// mu protects data, getValueErr, and getValueCalls during fixture method
	// calls. Direct fixture mutations require the same exclusion while methods
	// run; initialization precedes these tests' synchronous operations.
	mu            sync.Mutex        // settings fixture state lock
	data          map[string]string // stored scalar settings values
	getValueErr   error             // injected settings read failure
	getValueCalls int               // settings value read count
}

// cooldownSettingsStore returns a distinct missing-key sentinel for
// synchronous cooldown policy tests.
type cooldownSettingsStore struct {
	data map[string]string // synchronous cooldown settings map
}

// ollamaSettingsStore provides successful empty reads for the owning Ollama
// settings decoder.
type ollamaSettingsStore struct {
	values map[string]string // synchronous Ollama settings map
}

// newMockSettingRepo allocates a synchronized store with no persisted policy
// keys.
func newMockSettingRepo() *mockSettingRepo {
	return &mockSettingRepo{data: make(map[string]string)}
}

// Get returns a newly allocated setting or the production missing-key
// sentinel under the fixture mutex. The returned scalar fields copy the
// stored value and remain independent of later map replacement.
func (m *mockSettingRepo) Get(_ context.Context, key string) (*settingscore.Setting, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, settingscore.ErrSettingNotFound
	}
	return &settingscore.Setting{Key: key, Value: v}, nil
}

// GetValue increments the read counter and returns the stored string under
// the fixture mutex. A configured error takes precedence; missing entries
// return an empty successful value for getter default handling.
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

// Set replaces one settings value under the fixture mutex and reports
// success.
func (m *mockSettingRepo) Set(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

// GetMultiple copies present requested keys into a new map under the mutex.
// Missing keys remain absent, and callers may modify the returned scalar map
// independently of fixture storage.
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

// SetMultiple merges all supplied scalar settings values under the fixture
// mutex.
func (m *mockSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	maps.Copy(m.data, settings)
	return nil
}

// GetAll returns a copied settings map captured under the fixture mutex.
// Later caller mutation of the result does not change fixture storage, whose
// values are scalar strings.
func (m *mockSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]string, len(m.data))
	maps.Copy(result, m.data)
	return result, nil
}

// Delete removes a settings key under the fixture mutex and reports success.
func (m *mockSettingRepo) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

// newCooldownSettingsStore allocates an empty store for synchronous
// cooldown-setting reads and writes.
func newCooldownSettingsStore() *cooldownSettingsStore {
	return &cooldownSettingsStore{data: map[string]string{}}
}

// GetValue returns a stored cooldown string or errCooldownSettingMissing when
// absent. The fixture is unsynchronized, so tests complete each read or write
// before changing its map.
func (s *cooldownSettingsStore) GetValue(_ context.Context, key string) (string, error) {
	value, ok := s.data[key]
	if !ok {
		return "", errCooldownSettingMissing
	}
	return value, nil
}

// Set stores a scalar cooldown value for the fixture's next synchronous read.
func (s *cooldownSettingsStore) Set(_ context.Context, key, value string) error {
	s.data[key] = value
	return nil
}

// TestOpenAIImagesOAuthUnavailableCooldownSettingsDefaultAndStoredValue
// begins with an empty store and then saves a supported image duration. It
// verifies the default and persisted value through the production reader.
func TestOpenAIImagesOAuthUnavailableCooldownSettingsDefaultAndStoredValue(t *testing.T) {
	repo := newMockSettingRepo()
	svc := NewRuntimeSettings(repo, settingscore.ErrSettingNotFound)

	settings, err := svc.GetOpenAIImagesOAuthUnavailableCooldownSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 30, settings.CooldownMinutes)

	require.NoError(t, svc.SetOpenAIImagesOAuthUnavailableCooldownSettings(context.Background(), &OpenAIImagesOAuthUnavailableCooldownSettings{CooldownMinutes: 7}))
	settings, err = svc.GetOpenAIImagesOAuthUnavailableCooldownSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 7, settings.CooldownMinutes)
}

// TestSetOpenAIImagesOAuthUnavailableCooldownSettingsBoundaries submits
// values at and outside the supported image-pause range. It verifies boundary
// acceptance and the administrator-facing rejection for zero, negative, and
// excessive values.
func TestSetOpenAIImagesOAuthUnavailableCooldownSettingsBoundaries(t *testing.T) {
	svc := NewRuntimeSettings(newMockSettingRepo(), settingscore.ErrSettingNotFound)

	for _, minutes := range []int{1, OpenAIImagesOAuthUnavailableMaxCooldownMinutes} {
		err := svc.SetOpenAIImagesOAuthUnavailableCooldownSettings(context.Background(), &OpenAIImagesOAuthUnavailableCooldownSettings{CooldownMinutes: minutes})
		require.NoError(t, err, "should accept cooldown_minutes=%d", minutes)
	}

	for _, minutes := range []int{0, -1, OpenAIImagesOAuthUnavailableMaxCooldownMinutes + 1} {
		err := svc.SetOpenAIImagesOAuthUnavailableCooldownSettings(context.Background(), &OpenAIImagesOAuthUnavailableCooldownSettings{CooldownMinutes: minutes})
		require.ErrorContains(t, err, "cooldown_minutes must be between 1-120")
	}
}

// TestOpenAIImagesOAuthUnavailableCooldownSettingsRejectsOverflowingValue
// supplies the platform's largest int as an image duration. It verifies
// writes reject it and reads of excessive persisted values return the
// built-in cooldown.
func TestOpenAIImagesOAuthUnavailableCooldownSettingsRejectsOverflowingValue(t *testing.T) {
	// Use the host int ceiling to exercise validation before any duration
	// conversion could turn an excessive setting into a misleading pause.
	maxInt := int(^uint(0) >> 1)
	repo := newMockSettingRepo()
	svc := NewRuntimeSettings(repo, settingscore.ErrSettingNotFound)

	err := svc.SetOpenAIImagesOAuthUnavailableCooldownSettings(context.Background(), &OpenAIImagesOAuthUnavailableCooldownSettings{CooldownMinutes: maxInt})
	require.ErrorContains(t, err, "cooldown_minutes must be between 1-120")

	for _, minutes := range []int{OpenAIImagesOAuthUnavailableMaxCooldownMinutes + 1, maxInt} {
		data, marshalErr := json.Marshal(OpenAIImagesOAuthUnavailableCooldownSettings{CooldownMinutes: minutes})
		require.NoError(t, marshalErr)
		repo.data[SettingKeyOpenAIImagesOAuthUnavailableCooldownSettings] = string(data)

		settings, getErr := svc.GetOpenAIImagesOAuthUnavailableCooldownSettings(context.Background())
		require.NoError(t, getErr)
		require.Equal(t, OpenAIImagesOAuthUnavailableDefaultCooldownMinutes, settings.CooldownMinutes)
	}
}

// GetValue returns the stored Ollama string, including an empty successful
// value for missing keys. The fixture is used synchronously and expects
// callers to keep its map stable during reads.
func (s *ollamaSettingsStore) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

// Set stores encoded Ollama configuration for the fixture's next synchronous
// read.
func (s *ollamaSettingsStore) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

// TestOllamaCloudUsageSettingsDefaultOffAndValidation exercises disabled
// defaults, timing ranges, omitted debounce, and interval ordering. It
// verifies rejected inputs, normalized persistence, and a stored older JSON
// object inheriting the one-minute debounce.
func TestOllamaCloudUsageSettingsDefaultOffAndValidation(t *testing.T) {
	repo := &ollamaSettingsStore{values: map[string]string{}}
	settingsService := NewRuntimeSettings(repo, errors.New("missing"))
	settings, err := settingsService.GetOllamaCloudUsageSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Enabled)
	require.Equal(t, 60, settings.IntervalMinutes)
	require.Equal(t, 1, settings.DebounceMinutes)

	err = settingsService.SetOllamaCloudUsageSettings(context.Background(), &OllamaCloudUsageSettings{Enabled: true, IntervalMinutes: 14, DebounceMinutes: 1})
	require.Error(t, err)
	err = settingsService.SetOllamaCloudUsageSettings(context.Background(), &OllamaCloudUsageSettings{Enabled: true, IntervalMinutes: 90, DebounceMinutes: 61})
	require.Error(t, err)
	// Zero debounce models an older client's omitted field. Successful saving
	// should supply the default before the production decoder reads it back.
	err = settingsService.SetOllamaCloudUsageSettings(context.Background(), &OllamaCloudUsageSettings{Enabled: true, IntervalMinutes: 90, DebounceMinutes: 0})
	require.NoError(t, err)
	settings, err = settingsService.GetOllamaCloudUsageSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, settings.DebounceMinutes)
	err = settingsService.SetOllamaCloudUsageSettings(context.Background(), &OllamaCloudUsageSettings{Enabled: true, IntervalMinutes: 90, DebounceMinutes: 2})
	require.NoError(t, err)
	settings, err = settingsService.GetOllamaCloudUsageSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 90, settings.IntervalMinutes)
	require.Equal(t, 2, settings.DebounceMinutes)

	// Recent activity occurs after the reference attempt, so a debounce at least
	// as long as maximum wait cannot select an earlier activity deadline;
	// exercise this cross-field rejection at and beyond equality.
	err = settingsService.SetOllamaCloudUsageSettings(context.Background(), &OllamaCloudUsageSettings{Enabled: true, IntervalMinutes: 15, DebounceMinutes: 15})
	require.Error(t, err, "debounce equal to interval must be rejected")
	err = settingsService.SetOllamaCloudUsageSettings(context.Background(), &OllamaCloudUsageSettings{Enabled: true, IntervalMinutes: 15, DebounceMinutes: 60})
	require.Error(t, err, "debounce greater than interval must be rejected")
	err = settingsService.SetOllamaCloudUsageSettings(context.Background(), &OllamaCloudUsageSettings{Enabled: true, IntervalMinutes: 16, DebounceMinutes: 15})
	require.NoError(t, err, "debounce below interval stays valid")

	// An older persisted object can also omit debounce. Read its initialized
	// default without sending it through administrator write-time validation.
	repo.values[SettingKeyOllamaCloudUsageSettings] = `{"enabled":true,"interval_minutes":45}`
	settings, err = settingsService.GetOllamaCloudUsageSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 45, settings.IntervalMinutes)
	require.Equal(t, 1, settings.DebounceMinutes)
}

// TestGetOverloadCooldownSettings_DefaultsWhenNotSet reads an absent overload
// policy from the sentinel-returning fixture. It verifies the reader returns
// an enabled ten-minute default with a nil read error.
func TestGetOverloadCooldownSettings_DefaultsWhenNotSet(t *testing.T) {
	repo := newCooldownSettingsStore()
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	settings, err := svc.GetOverloadCooldownSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 10, settings.CooldownMinutes)
}

// TestGetOverloadCooldownSettings_ReadsFromDB stores an encoded disabled
// policy with a supported duration. It verifies the reader retains both the
// enabled flag and saved minutes.
func TestGetOverloadCooldownSettings_ReadsFromDB(t *testing.T) {
	repo := newCooldownSettingsStore()
	data, err := json.Marshal(OverloadCooldownSettings{Enabled: false, CooldownMinutes: 30})
	require.NoError(t, err)
	repo.data[SettingKeyOverloadCooldownSettings] = string(data)
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	settings, err := svc.GetOverloadCooldownSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Enabled)
	require.Equal(t, 30, settings.CooldownMinutes)
}

// TestGetOverloadCooldownSettings_ClampsMinValue stores valid JSON with zero
// cooldown minutes. It verifies stored range correction raises that duration
// to the minimum of one minute.
func TestGetOverloadCooldownSettings_ClampsMinValue(t *testing.T) {
	repo := newCooldownSettingsStore()
	data, err := json.Marshal(OverloadCooldownSettings{Enabled: true, CooldownMinutes: 0})
	require.NoError(t, err)
	repo.data[SettingKeyOverloadCooldownSettings] = string(data)
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	settings, err := svc.GetOverloadCooldownSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, settings.CooldownMinutes)
}

// TestGetOverloadCooldownSettings_ClampsMaxValue stores valid JSON with
// excessive cooldown minutes. It verifies stored range correction caps that
// duration at one hundred twenty minutes.
func TestGetOverloadCooldownSettings_ClampsMaxValue(t *testing.T) {
	repo := newCooldownSettingsStore()
	data, err := json.Marshal(OverloadCooldownSettings{Enabled: true, CooldownMinutes: 999})
	require.NoError(t, err)
	repo.data[SettingKeyOverloadCooldownSettings] = string(data)
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	settings, err := svc.GetOverloadCooldownSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 120, settings.CooldownMinutes)
}

// TestGetOverloadCooldownSettings_InvalidJSON_ReturnsDefaults stores a
// malformed policy value. It verifies decoding failure returns the complete
// enabled ten-minute default without exposing an error.
func TestGetOverloadCooldownSettings_InvalidJSON_ReturnsDefaults(t *testing.T) {
	repo := newCooldownSettingsStore()
	repo.data[SettingKeyOverloadCooldownSettings] = "not-json"
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	settings, err := svc.GetOverloadCooldownSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 10, settings.CooldownMinutes)
}

// TestGetOverloadCooldownSettings_EmptyValue_ReturnsDefaults stores an empty
// successful policy value in the cooldown fixture. It verifies the read
// succeeds and supplies the complete enabled ten-minute overload default.
func TestGetOverloadCooldownSettings_EmptyValue_ReturnsDefaults(t *testing.T) {
	repo := newCooldownSettingsStore()
	repo.data[SettingKeyOverloadCooldownSettings] = ""
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	settings, err := svc.GetOverloadCooldownSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 10, settings.CooldownMinutes)
}

// TestSetOverloadCooldownSettings_Success saves a disabled policy with a
// supported duration. It verifies reading the encoded settings returns the
// same enabled flag and cooldown.
func TestSetOverloadCooldownSettings_Success(t *testing.T) {
	repo := newCooldownSettingsStore()
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	err := svc.SetOverloadCooldownSettings(context.Background(), &OverloadCooldownSettings{
		Enabled:         false,
		CooldownMinutes: 25,
	})
	require.NoError(t, err)

	settings, err := svc.GetOverloadCooldownSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Enabled)
	require.Equal(t, 25, settings.CooldownMinutes)
}

// TestSetOverloadCooldownSettings_RejectsNil submits no policy object to the
// setter through a configured fixture store. It verifies validation reports
// an error for the missing input.
func TestSetOverloadCooldownSettings_RejectsNil(t *testing.T) {
	svc := NewRuntimeSettings(newCooldownSettingsStore(), errCooldownSettingMissing)
	err := svc.SetOverloadCooldownSettings(context.Background(), nil)
	require.Error(t, err)
}

// TestSetOverloadCooldownSettings_EnabledRejectsOutOfRange submits zero,
// negative, and excessive durations for an active policy. It verifies each
// input returns the expected supported-range validation error.
func TestSetOverloadCooldownSettings_EnabledRejectsOutOfRange(t *testing.T) {
	svc := NewRuntimeSettings(newCooldownSettingsStore(), errCooldownSettingMissing)

	for _, minutes := range []int{0, -1, 121, 999} {
		err := svc.SetOverloadCooldownSettings(context.Background(), &OverloadCooldownSettings{
			Enabled: true, CooldownMinutes: minutes,
		})
		require.Error(t, err, "should reject enabled=true + cooldown_minutes=%d", minutes)
		require.Contains(t, err.Error(), "cooldown_minutes must be between 1-120")
	}
}

// TestSetOverloadCooldownSettings_DisabledNormalizesOutOfRange saves an
// inactive policy with zero duration. It verifies the write succeeds and the
// persisted policy remains disabled with ten normalized minutes.
func TestSetOverloadCooldownSettings_DisabledNormalizesOutOfRange(t *testing.T) {
	repo := newCooldownSettingsStore()
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	// Inactive policies normalize invalid input before persistence, allowing
	// later activation to begin with a supported stored cooldown duration.
	err := svc.SetOverloadCooldownSettings(context.Background(), &OverloadCooldownSettings{
		Enabled: false, CooldownMinutes: 0,
	})
	require.NoError(t, err, "disabled with invalid minutes should NOT be rejected")

	settings, err := svc.GetOverloadCooldownSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Enabled)
	require.Equal(t, 10, settings.CooldownMinutes, "should be normalized to default")
}

// TestSetOverloadCooldownSettings_AcceptsBoundaries submits active policies
// at both range ends and one interior duration. It verifies all three inputs
// are accepted by the setter.
func TestSetOverloadCooldownSettings_AcceptsBoundaries(t *testing.T) {
	svc := NewRuntimeSettings(newCooldownSettingsStore(), errCooldownSettingMissing)

	for _, minutes := range []int{1, 60, 120} {
		err := svc.SetOverloadCooldownSettings(context.Background(), &OverloadCooldownSettings{
			Enabled: true, CooldownMinutes: minutes,
		})
		require.NoError(t, err, "should accept cooldown_minutes=%d", minutes)
	}
}

// TestDefaultOverloadCooldownSettings constructs the built-in policy directly
// for comparison with reader defaults. It verifies overload cooldown starts
// enabled and uses ten minutes.
func TestDefaultOverloadCooldownSettings(t *testing.T) {
	d := DefaultOverloadCooldownSettings()
	require.True(t, d.Enabled)
	require.Equal(t, 10, d.CooldownMinutes)
}

// TestOverloadCooldownSettings_JSONRoundTrip encodes and decodes a disabled
// policy with a supported duration. It verifies scalar equality and the
// enabled and cooldown_minutes JSON field names.
func TestOverloadCooldownSettings_JSONRoundTrip(t *testing.T) {
	original := OverloadCooldownSettings{Enabled: false, CooldownMinutes: 42}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded OverloadCooldownSettings
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, original, decoded)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	_, hasEnabled := raw["enabled"]
	_, hasCooldown := raw["cooldown_minutes"]
	require.True(t, hasEnabled, "JSON must use 'enabled'")
	require.True(t, hasCooldown, "JSON must use 'cooldown_minutes'")
}

// TestGetRateLimit429CooldownSettings_DefaultsWhenNotSet reads an absent 429
// cooldown key from the sentinel-returning fixture. It verifies the read
// succeeds with the enabled policy and five-second duration.
func TestGetRateLimit429CooldownSettings_DefaultsWhenNotSet(t *testing.T) {
	repo := newCooldownSettingsStore()
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	settings, err := svc.GetRateLimit429CooldownSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 5, settings.CooldownSeconds)
}

// TestGetRateLimit429CooldownSettings_ReadsFromDB stores an encoded disabled
// 429 policy with twelve cooldown seconds. It verifies an error-free read
// retains the saved enabled flag and duration.
func TestGetRateLimit429CooldownSettings_ReadsFromDB(t *testing.T) {
	repo := newCooldownSettingsStore()
	data, err := json.Marshal(RateLimit429CooldownSettings{Enabled: false, CooldownSeconds: 12})
	require.NoError(t, err)
	repo.data[SettingKeyRateLimit429CooldownSettings] = string(data)
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	settings, err := svc.GetRateLimit429CooldownSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Enabled)
	require.Equal(t, 12, settings.CooldownSeconds)
}

// TestSetRateLimit429CooldownSettings_EnabledRejectsOutOfRange submits active
// policies outside the supported second range. It verifies zero, negative,
// and excessive values receive the expected validation error.
func TestSetRateLimit429CooldownSettings_EnabledRejectsOutOfRange(t *testing.T) {
	svc := NewRuntimeSettings(newCooldownSettingsStore(), errCooldownSettingMissing)

	for _, seconds := range []int{0, -1, 7201, 99999} {
		err := svc.SetRateLimit429CooldownSettings(context.Background(), &RateLimit429CooldownSettings{
			Enabled: true, CooldownSeconds: seconds,
		})
		require.Error(t, err, "should reject enabled=true + cooldown_seconds=%d", seconds)
		require.Contains(t, err.Error(), "cooldown_seconds must be between 1-7200")
	}
}
