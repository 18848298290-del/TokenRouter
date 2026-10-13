package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Quota settings consumers share the Ops advanced-settings JSON while needing
// different read timing. These external-package tests exercise the real Ops
// decoder through synchronous warmup, immediate cold reads, and direct cache
// publication so the provider cache's public entry points retain their
// distinct results.

// quotaReadFixture supplies immutable settings values to synchronous and
// detached quota-cache reads.
type quotaReadFixture struct {
	// values is read after a cold Get can return. Tests populate it before
	// invoking the cache and retain stable entries for detached refreshes.
	values map[string]string // immutable settings fixture map
}

// TestGetOpenAIQuotaAutoPauseSettings_ReadsDefaultsFromOpsAdvancedSettings
// stores both ratios inside the Ops JSON. It verifies synchronous warmup
// decodes them and a later ordinary read returns the warmed values.
func TestGetOpenAIQuotaAutoPauseSettings_ReadsDefaultsFromOpsAdvancedSettings(t *testing.T) {
	repo := newQuotaReadFixture()
	repo.values[ops.SettingKeyOpsAdvancedSettings] = `{"openai_provider_quota_auto_pause":{"default_threshold_5h":0.95,"default_threshold_7d":0.9}}`
	svc := provider.NewQuotaSettingsCache(repo, settings.ErrSettingNotFound, ops.ParseRuntimeQuotaAutoPauseSettings)

	// Complete the storage read before inspecting decoded values. Ordinary Get
	// would return zero on this cold cache even if its detached refresh finished
	// before the assertion.
	settings := svc.WarmOpenAIQuotaAutoPauseSettings(context.Background())
	if settings.DefaultThreshold5h != 0.95 {
		t.Fatalf("DefaultThreshold5h = %v, want 0.95", settings.DefaultThreshold5h)
	}
	if settings.DefaultThreshold7d != 0.9 {
		t.Fatalf("DefaultThreshold7d = %v, want 0.9", settings.DefaultThreshold7d)
	}

	cached := svc.GetOpenAIQuotaAutoPauseSettings(context.Background())
	if cached.DefaultThreshold5h != 0.95 || cached.DefaultThreshold7d != 0.9 {
		t.Fatalf("cached read = %+v, want {0.95, 0.9}", cached)
	}
}

// TestGetOpenAIQuotaAutoPauseSettings_ColdCacheNonBlocking starts with a
// populated store and an empty process cache. It verifies the first read
// returns zero thresholds within the observed timing limit regardless of
// detached refresh completion.
func TestGetOpenAIQuotaAutoPauseSettings_ColdCacheNonBlocking(t *testing.T) {
	repo := newQuotaReadFixture()
	repo.values[ops.SettingKeyOpsAdvancedSettings] = `{"openai_provider_quota_auto_pause":{"default_threshold_5h":0.7}}`
	svc := provider.NewQuotaSettingsCache(repo, settings.ErrSettingNotFound, ops.ParseRuntimeQuotaAutoPauseSettings)

	start := time.Now()
	settings := svc.GetOpenAIQuotaAutoPauseSettings(context.Background())
	elapsed := time.Since(start)
	// The elapsed ceiling checks prompt return, while the zero-value assertion
	// separately checks which cold-cache observation this call returned.
	if elapsed > 50*time.Millisecond {
		t.Fatalf("cold-cache Get must be non-blocking, took %v", elapsed)
	}
	// Cold Get selected its return from the empty initial observation. The
	// background task may finish independently without changing that returned
	// scalar value.
	if settings.DefaultThreshold5h != 0 || settings.DefaultThreshold7d != 0 {
		t.Fatalf("cold-cache Get = %+v, want zeroes", settings)
	}
}

// TestSetOpenAIQuotaAutoPauseSettings_VisibleImmediately publishes two ratios
// into a new cache. It verifies the next ordinary read sees both values
// immediately from that published observation.
func TestSetOpenAIQuotaAutoPauseSettings_VisibleImmediately(t *testing.T) {
	svc := provider.NewQuotaSettingsCache(newQuotaReadFixture(), settings.ErrSettingNotFound, ops.ParseRuntimeQuotaAutoPauseSettings)

	svc.SetOpenAIQuotaAutoPauseSettings(ops.OpsOpenAIProviderQuotaAutoPauseSettings{
		DefaultThreshold5h: 0.88,
		DefaultThreshold7d: 0.77,
	})

	got := svc.GetOpenAIQuotaAutoPauseSettings(context.Background())
	if got.DefaultThreshold5h != 0.88 || got.DefaultThreshold7d != 0.77 {
		t.Fatalf("after Set, Get = %+v, want {0.88, 0.77}", got)
	}
}

// newQuotaReadFixture allocates an empty settings map for quota-cache
// entry-point tests.
func newQuotaReadFixture() *quotaReadFixture { return &quotaReadFixture{values: map[string]string{}} }

// GetValue returns a stored settings string or the production missing-key
// sentinel. Populate the map before starting cache reads and keep it
// unchanged while detached refreshes may still access it.
func (r *quotaReadFixture) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", settings.ErrSettingNotFound
}
