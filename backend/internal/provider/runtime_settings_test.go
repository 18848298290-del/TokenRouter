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

var errCooldownSettingMissing = errors.New("setting missing")

type mockSettingRepo struct {
	mu            sync.Mutex
	data          map[string]string
	getValueErr   error
	getValueCalls int
}

// cooldownSettingsStore supplies single-key reads and writes while tests use the production settings cache.
type cooldownSettingsStore struct{ data map[string]string }

// ollamaSettingsStore holds key-value settings for configuration tests.
type ollamaSettingsStore struct{ values map[string]string }

func newMockSettingRepo() *mockSettingRepo {
	return &mockSettingRepo{data: make(map[string]string)}
}

func (m *mockSettingRepo) Get(_ context.Context, key string) (*settingscore.Setting, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, settingscore.ErrSettingNotFound
	}
	return &settingscore.Setting{Key: key, Value: v}, nil
}

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

func (m *mockSettingRepo) Set(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

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

func (m *mockSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	maps.Copy(m.data, settings)
	return nil
}

func (m *mockSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]string, len(m.data))
	maps.Copy(result, m.data)
	return result, nil
}

func (m *mockSettingRepo) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func newCooldownSettingsStore() *cooldownSettingsStore {
	return &cooldownSettingsStore{data: map[string]string{}}
}

func (s *cooldownSettingsStore) GetValue(_ context.Context, key string) (string, error) {
	value, ok := s.data[key]
	if !ok {
		return "", errCooldownSettingMissing
	}
	return value, nil
}

func (s *cooldownSettingsStore) Set(_ context.Context, key, value string) error {
	s.data[key] = value
	return nil
}

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

func TestOpenAIImagesOAuthUnavailableCooldownSettingsRejectsOverflowingValue(t *testing.T) {
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

func (s *ollamaSettingsStore) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *ollamaSettingsStore) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

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
	// Zero represents a field omitted by an older client; saving supplies the one-minute default.
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

	// A debounce at least as long as the interval cannot win the deadline calculation
	// min(lastUsed+debounce, fetchedAt+maxWait), so configuration rejects it.
	err = settingsService.SetOllamaCloudUsageSettings(context.Background(), &OllamaCloudUsageSettings{Enabled: true, IntervalMinutes: 15, DebounceMinutes: 15})
	require.Error(t, err, "debounce equal to interval must be rejected")
	err = settingsService.SetOllamaCloudUsageSettings(context.Background(), &OllamaCloudUsageSettings{Enabled: true, IntervalMinutes: 15, DebounceMinutes: 60})
	require.Error(t, err, "debounce greater than interval must be rejected")
	err = settingsService.SetOllamaCloudUsageSettings(context.Background(), &OllamaCloudUsageSettings{Enabled: true, IntervalMinutes: 16, DebounceMinutes: 15})
	require.NoError(t, err, "debounce below interval stays valid")

	// Missing debounce_minutes in stored JSON defaults to one minute.
	repo.values[SettingKeyOllamaCloudUsageSettings] = `{"enabled":true,"interval_minutes":45}`
	settings, err = settingsService.GetOllamaCloudUsageSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 45, settings.IntervalMinutes)
	require.Equal(t, 1, settings.DebounceMinutes)
}

func TestGetOverloadCooldownSettings_DefaultsWhenNotSet(t *testing.T) {
	repo := newCooldownSettingsStore()
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	settings, err := svc.GetOverloadCooldownSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 10, settings.CooldownMinutes)
}

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

func TestGetOverloadCooldownSettings_InvalidJSON_ReturnsDefaults(t *testing.T) {
	repo := newCooldownSettingsStore()
	repo.data[SettingKeyOverloadCooldownSettings] = "not-json"
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	settings, err := svc.GetOverloadCooldownSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 10, settings.CooldownMinutes)
}

func TestGetOverloadCooldownSettings_EmptyValue_ReturnsDefaults(t *testing.T) {
	repo := newCooldownSettingsStore()
	repo.data[SettingKeyOverloadCooldownSettings] = ""
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	settings, err := svc.GetOverloadCooldownSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 10, settings.CooldownMinutes)
}

func TestSetOverloadCooldownSettings_Success(t *testing.T) {
	repo := newCooldownSettingsStore()
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	err := svc.SetOverloadCooldownSettings(context.Background(), &OverloadCooldownSettings{
		Enabled:         false,
		CooldownMinutes: 25,
	})
	require.NoError(t, err)

	// Check settings after JSON encoding and decoding.
	settings, err := svc.GetOverloadCooldownSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Enabled)
	require.Equal(t, 25, settings.CooldownMinutes)
}

func TestSetOverloadCooldownSettings_RejectsNil(t *testing.T) {
	svc := NewRuntimeSettings(newCooldownSettingsStore(), errCooldownSettingMissing)
	err := svc.SetOverloadCooldownSettings(context.Background(), nil)
	require.Error(t, err)
}

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

func TestSetOverloadCooldownSettings_DisabledNormalizesOutOfRange(t *testing.T) {
	repo := newCooldownSettingsStore()
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	// Disabled cooldowns accept zero and normalize it to ten minutes.
	err := svc.SetOverloadCooldownSettings(context.Background(), &OverloadCooldownSettings{
		Enabled: false, CooldownMinutes: 0,
	})
	require.NoError(t, err, "disabled with invalid minutes should NOT be rejected")

	// Read back the persisted normalized value.
	settings, err := svc.GetOverloadCooldownSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Enabled)
	require.Equal(t, 10, settings.CooldownMinutes, "should be normalized to default")
}

func TestSetOverloadCooldownSettings_AcceptsBoundaries(t *testing.T) {
	svc := NewRuntimeSettings(newCooldownSettingsStore(), errCooldownSettingMissing)

	for _, minutes := range []int{1, 60, 120} {
		err := svc.SetOverloadCooldownSettings(context.Background(), &OverloadCooldownSettings{
			Enabled: true, CooldownMinutes: minutes,
		})
		require.NoError(t, err, "should accept cooldown_minutes=%d", minutes)
	}
}

func TestDefaultOverloadCooldownSettings(t *testing.T) {
	d := DefaultOverloadCooldownSettings()
	require.True(t, d.Enabled)
	require.Equal(t, 10, d.CooldownMinutes)
}

func TestOverloadCooldownSettings_JSONRoundTrip(t *testing.T) {
	original := OverloadCooldownSettings{Enabled: false, CooldownMinutes: 42}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded OverloadCooldownSettings
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, original, decoded)

	// Check the snake_case JSON field names.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	_, hasEnabled := raw["enabled"]
	_, hasCooldown := raw["cooldown_minutes"]
	require.True(t, hasEnabled, "JSON must use 'enabled'")
	require.True(t, hasCooldown, "JSON must use 'cooldown_minutes'")
}

func TestGetRateLimit429CooldownSettings_DefaultsWhenNotSet(t *testing.T) {
	repo := newCooldownSettingsStore()
	svc := NewRuntimeSettings(repo, errCooldownSettingMissing)

	settings, err := svc.GetRateLimit429CooldownSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 5, settings.CooldownSeconds)
}

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
