package runtimeconfig

import (
	"context"
	"maps"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// panelRateLimitSettingRepo provides in-memory settings and raw-read counts for panel fixtures.
type panelRateLimitSettingRepo struct {
	// mu protects values, getValueErr, and getValueCalls during fixture methods.
	// Test setup supplies maps before calls begin; counted reads and storage
	// mutations then use that same lock so snapshots see complete method updates.
	mu            sync.Mutex        // fixture storage access lock
	values        map[string]string // raw setting key/value pairs
	getValueErr   error             // forced raw-read failure result
	getValueCalls int               // attempted raw setting reads
}

// Get returns a newly assembled Setting for a stored fixture key.
// Missing keys return ErrSettingNotFound. The supplied context is unused by this
// in-memory fake, and returned string values can be read independently of its map.
func (r *panelRateLimitSettingRepo) Get(_ context.Context, key string) (*settings.Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return nil, settings.ErrSettingNotFound
	}
	return &settings.Setting{Key: key, Value: value}, nil
}

// GetValue counts the raw lookup and returns its fixture string or configured error.
// The injected error takes precedence over key absence; otherwise missing keys
// return ErrSettingNotFound. Context cancellation is unused by this fake storage.
func (r *panelRateLimitSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getValueCalls++
	if r.getValueErr != nil {
		return "", r.getValueErr
	}
	value, ok := r.values[key]
	if !ok {
		return "", settings.ErrSettingNotFound
	}
	return value, nil
}

// Set stores one raw fixture value while holding the shared storage lock.
// A nil map is allocated on the first write. This fake ignores the operation
// context and always returns nil after the value has been assigned.
func (r *panelRateLimitSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	r.values[key] = value
	return nil
}

// GetMultiple copies the requested existing keys into an independently allocated map.
// Missing keys are omitted and empty requests still receive an allocated result.
// The supplied context is unused while the fixture lock protects its snapshot.
func (r *panelRateLimitSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

// SetMultiple copies supplied values into the fixture's stored map under one lock.
// Other fixture methods observe the batch after that lock is released. Nil
// storage is allocated, and the operation context is unused before returning nil.
func (r *panelRateLimitSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	maps.Copy(r.values, settings)
	return nil
}

// GetAll returns an independently allocated copy of all fixture key/value strings.
// Empty storage produces an empty map. The context is unused, and callers may
// change returned entries without changing the fake's subsequently stored values.
func (r *panelRateLimitSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.values))
	maps.Copy(out, r.values)
	return out, nil
}

// Delete removes the fixture key while holding the shared storage lock.
// Missing keys are harmless, and this fake returns nil for either case. The
// supplied operation context is unused by its in-memory deletion.
func (r *panelRateLimitSettingRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

// newPanelRateLimitTestService constructs panel settings backed by the supplied fixture repository.
func newPanelRateLimitTestService(repo PanelSettingsRepository) *PanelSettings {
	return NewPanelSettings(repo)
}

// TestGetPanelRateLimitSettingsDefaults reads from an empty fixture repository.
// The direct getter must succeed and return the complete enabled default object,
// including all three rates and the administrator exemption.
func TestGetPanelRateLimitSettingsDefaults(t *testing.T) {
	svc := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})

	settings, err := svc.GetPanelRateLimitSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, DefaultPanelRateLimitSettings(), settings)
}

// TestGetPanelRateLimitSettingsInvalidJSONFallsBack
// supplies an undecodable stored JSON value.
// The direct getter must succeed with the complete default object, protecting
// recovery from that malformed storage value rather than returning its parse error.
func TestGetPanelRateLimitSettingsInvalidJSONFallsBack(t *testing.T) {
	repo := &panelRateLimitSettingRepo{values: map[string]string{
		SettingKeyPanelRateLimitSettings: "{not-json",
	}}
	svc := newPanelRateLimitTestService(repo)

	settings, err := svc.GetPanelRateLimitSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, DefaultPanelRateLimitSettings(), settings)
}

// TestGetPanelRateLimitSettingsNormalizesValues
// combines a negative user rate and oversized heavy rate.
// Reading must clamp those counts while retaining the valid public rate and
// the fixture's supplied enable and administrator-exemption flags.
func TestGetPanelRateLimitSettingsNormalizesValues(t *testing.T) {
	repo := &panelRateLimitSettingRepo{values: map[string]string{
		SettingKeyPanelRateLimitSettings: `{"enabled":true,"user_rpm":-5,"heavy_rpm":999999999,"exempt_admin":false,"public_ip_rpm":10}`,
	}}
	svc := newPanelRateLimitTestService(repo)

	settings, err := svc.GetPanelRateLimitSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 0, settings.UserRPM)
	require.Equal(t, panelRateLimitRPMMax, settings.HeavyRPM)
	require.Equal(t, 10, settings.PublicIPRPM)
	require.False(t, settings.ExemptAdmin)
}

// TestSetPanelRateLimitSettingsValidation
// supplies nil input, a negative user rate, and an oversized heavy rate.
// Each call must return an error for the supplied invalid object before the
// fixture proceeds to its next validation case.
func TestSetPanelRateLimitSettingsValidation(t *testing.T) {
	svc := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})

	require.Error(t, svc.SetPanelRateLimitSettings(context.Background(), nil))
	require.Error(t, svc.SetPanelRateLimitSettings(context.Background(), &PanelRateLimitSettings{UserRPM: -1}))
	require.Error(t, svc.SetPanelRateLimitSettings(context.Background(), &PanelRateLimitSettings{HeavyRPM: panelRateLimitRPMMax + 1}))
}

// TestSetPanelRateLimitSettingsRoundTripAndCacheRefresh
// primes defaults, saves another object, and reads it serially.
// The cached and direct reads must both return the saved primitive values,
// establishing local publication and persistent round-trip behavior in this fixture.
func TestSetPanelRateLimitSettingsRoundTripAndCacheRefresh(t *testing.T) {
	repo := &panelRateLimitSettingRepo{}
	svc := newPanelRateLimitTestService(repo)

	// Prime the cache with defaults before the write.
	cached := svc.GetPanelRateLimitSettingsCached(context.Background())
	require.Equal(t, *DefaultPanelRateLimitSettings(), cached)

	want := &PanelRateLimitSettings{
		Enabled:     true,
		UserRPM:     120,
		HeavyRPM:    30,
		ExemptAdmin: false,
		PublicIPRPM: 60,
	}
	require.NoError(t, svc.SetPanelRateLimitSettings(context.Background(), want))

	// Read the local entry published by the preceding successful save.
	cached = svc.GetPanelRateLimitSettingsCached(context.Background())
	require.Equal(t, *want, cached)

	// A direct read confirms that the settings were persisted.
	stored, err := svc.GetPanelRateLimitSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, want, stored)
}

// TestGetPanelRateLimitSettingsCachedAvoidsRepeatedDBReads
// repeats five reads of one configured key.
// Each result must contain its user rate, while the fake's raw-read counter must
// record one lookup across those successive cache consumers.
func TestGetPanelRateLimitSettingsCachedAvoidsRepeatedDBReads(t *testing.T) {
	repo := &panelRateLimitSettingRepo{values: map[string]string{
		SettingKeyPanelRateLimitSettings: `{"enabled":true,"user_rpm":100,"heavy_rpm":20,"exempt_admin":true,"public_ip_rpm":50}`,
	}}
	svc := newPanelRateLimitTestService(repo)

	for range 5 {
		settings := svc.GetPanelRateLimitSettingsCached(context.Background())
		require.Equal(t, 100, settings.UserRPM)
	}

	repo.mu.Lock()
	calls := repo.getValueCalls
	repo.mu.Unlock()
	require.Equal(t, 1, calls, "TTL 内应只读一次 DB")
}
