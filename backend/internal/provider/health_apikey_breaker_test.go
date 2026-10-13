package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// API Key breaker fixtures supply a forced counter trip and record each pause
// publication call. The tests distinguish disabled settings from accepted
// trips, then check success observations leave both the settings reader and
// failure-accounting cache untouched.

// openAIAPIKeyHealthSettingRepo supplies breaker JSON and records
// runtime-setting reads in tests.
type openAIAPIKeyHealthSettingRepo struct {
	// RuntimeSettingsStore remains unset because this fixture overrides every
	// method its scenario reaches; an unexpected inherited operation fails through
	// the nil interface.
	RuntimeSettingsStore        // unused settings methods
	value                string // stored breaker JSON
	getCalls             int    // settings read calls
}

// openAIAPIKeyHealthProviderRepo records successful pause attempts and their
// serialized reason for assertions.
type openAIAPIKeyHealthProviderRepo struct {
	// HealthStore remains unset because this fixture overrides every method its
	// scenario reaches; an unexpected inherited operation fails through the nil
	// interface.
	HealthStore        // unused health methods
	setCalls    int    // persistent pause calls
	reason      string // stored pause reason
}

// openAIAPIKeyHealthCacheStub selects the trip result and records failure
// accounting and pause publication.
type openAIAPIKeyHealthCacheStub struct {
	// TempUnschedCache remains unset because this fixture overrides every method
	// its scenario reaches; an unexpected inherited operation fails through the
	// nil interface.
	TempUnschedCache     // unused pause methods
	recordCalls      int // failure accounting calls
	setCalls         int // cache publication calls
	// tripped supplies the accounting result directly, letting each scenario
	// choose an accepted trip independently of the fixed returned failure count.
	tripped bool
}

// openAIAPIKeyHealthRuntimeBlocker counts local scheduling-block notifications
// from the breaker command.
type openAIAPIKeyHealthRuntimeBlocker struct {
	calls int // runtime block calls
}

// GetValue records a runtime-setting read and returns the fixture's raw JSON
// successfully. The requested key and context are ignored so disabled and
// enabled scenarios choose policy through value.
func (r *openAIAPIKeyHealthSettingRepo) GetValue(context.Context, string) (string, error) {
	r.getCalls++
	return r.value, nil
}

// SetTempUnschedulable records one persistent pause call and retains its
// serialized reason. It returns success regardless of deadline, letting the
// test observe later runtime and cache publication.
func (r *openAIAPIKeyHealthProviderRepo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, reason string) error {
	r.setCalls++
	r.reason = reason
	return nil
}

// RecordOpenAIAPIKeyHealthFailure records an accounting call and returns count
// three with the configured trip flag. It ignores the supplied window and
// threshold; these tests force accounting outcomes directly.
func (c *openAIAPIKeyHealthCacheStub) RecordOpenAIAPIKeyHealthFailure(context.Context, int64, int, int) (int64, bool, error) {
	c.recordCalls++
	return 3, c.tripped, nil
}

// SetTempUnsched records one cache publication and returns success for the
// fixture. Its input state is unused because the test checks publication count
// and inspects serialized evidence through the provider store.
func (c *openAIAPIKeyHealthCacheStub) SetTempUnsched(context.Context, int64, *TempUnschedState) error {
	c.setCalls++
	return nil
}

// BlockProviderScheduling increments the fixture's count for a runtime
// scheduling-block notification.
func (b *openAIAPIKeyHealthRuntimeBlocker) BlockProviderScheduling(*Record, time.Time, string) {
	b.calls++
}

// ClearProviderSchedulingBlock leaves the block recorder unchanged for this
// breaker fixture.
func (*openAIAPIKeyHealthRuntimeBlocker) ClearProviderSchedulingBlock(int64) {}

// openAIHealthPoolProvider returns a fresh OpenAI API Key fixture with pool
// mode enabled.
func openAIHealthPoolProvider() *Record {
	return &Record{
		ID:       42,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}
}

// TestOpenAIAPIKeyHealthBreakerDefaultDisabled supplies missing settings and a
// cache ready to report a trip. It checks the command returns false before any
// failure-accounting call.
func TestOpenAIAPIKeyHealthBreakerDefaultDisabled(t *testing.T) {
	settings := NewRuntimeSettings(&openAIAPIKeyHealthSettingRepo{}, nil)
	cache := &openAIAPIKeyHealthCacheStub{tripped: true}
	svc := NewHealthService(&openAIAPIKeyHealthProviderRepo{}, cache, HealthOptions{APIKeyHealthCounter: cache, APIKeyHealthSettings: settings.GetOpenAIAPIKeyHealthBreakerSettings})

	require.False(t, svc.ApplyAPIKeyHealthFailure(context.Background(), openAIHealthPoolProvider(), http.StatusBadGateway, nil, true))
	require.Zero(t, cache.recordCalls)
}

// TestOpenAIAPIKeyHealthBreakerTripsPersistedAndRuntimeState forces an enabled
// three-failure trip. It checks one accounting, persistent, runtime and cache
// call, a request-record deadline and the breaker reason in storage.
func TestOpenAIAPIKeyHealthBreakerTripsPersistedAndRuntimeState(t *testing.T) {
	encoded, err := json.Marshal(OpenAIAPIKeyHealthBreakerSettings{Enabled: true, WindowMinutes: 1, FailureThreshold: 3, CooldownMinutes: 5})
	require.NoError(t, err)
	settings := NewRuntimeSettings(&openAIAPIKeyHealthSettingRepo{value: string(encoded)}, nil)
	cache := &openAIAPIKeyHealthCacheStub{tripped: true}
	repo := &openAIAPIKeyHealthProviderRepo{}
	blocker := &openAIAPIKeyHealthRuntimeBlocker{}
	svc := NewHealthService(repo, cache, HealthOptions{APIKeyHealthCounter: cache, APIKeyHealthSettings: settings.GetOpenAIAPIKeyHealthBreakerSettings, Block: blocker.BlockProviderScheduling})
	provider := openAIHealthPoolProvider()

	require.True(t, svc.ApplyAPIKeyHealthFailure(context.Background(), provider, http.StatusBadGateway, []byte(`{"error":"upstream"}`), true))
	require.Equal(t, 1, cache.recordCalls)
	require.Equal(t, 1, cache.setCalls)
	require.Equal(t, 1, repo.setCalls)
	require.Equal(t, 1, blocker.calls)
	require.NotNil(t, provider.TempUnschedulableUntil)
	require.Contains(t, repo.reason, OpenAIAPIKeyHealthBreakerReason)
}

// TestOpenAIAPIKeyHealthSuccessDoesNotTouchSettingsOrCache observes success for
// pool and ordinary OpenAI API Keys. It checks no settings reads or
// failure-accounting calls occur for either success.
func TestOpenAIAPIKeyHealthSuccessDoesNotTouchSettingsOrCache(t *testing.T) {
	encoded, err := json.Marshal(OpenAIAPIKeyHealthBreakerSettings{Enabled: true, WindowMinutes: 1, FailureThreshold: 3, CooldownMinutes: 5})
	require.NoError(t, err)
	settingRepo := &openAIAPIKeyHealthSettingRepo{value: string(encoded)}
	settings := NewRuntimeSettings(settingRepo, nil)
	cache := &openAIAPIKeyHealthCacheStub{}
	svc := NewHealthService(&openAIAPIKeyHealthProviderRepo{}, cache, HealthOptions{APIKeyHealthCounter: cache, APIKeyHealthSettings: settings.GetOpenAIAPIKeyHealthBreakerSettings})

	svc.ObserveAPIKeyHealthSuccess(context.Background(), openAIHealthPoolProvider())
	svc.ObserveAPIKeyHealthSuccess(context.Background(), &Record{ID: 43, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeAPIKey})
	require.Zero(t, settingRepo.getCalls)
	require.Zero(t, cache.recordCalls)
}
