package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/ops"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// quotaReadFixture supplies settings for quota tests and background cache refreshes.
type quotaReadFixture struct{ values map[string]string }

func TestGetOpenAIQuotaAutoPauseSettings_ReadsDefaultsFromOpsAdvancedSettings(t *testing.T) {
	repo := newQuotaReadFixture()
	repo.values[ops.SettingKeyOpsAdvancedSettings] = `{"openai_provider_quota_auto_pause":{"default_threshold_5h":0.95,"default_threshold_7d":0.9}}`
	svc := provider.NewQuotaSettingsCache(repo, settings.ErrSettingNotFound, ops.ParseRuntimeQuotaAutoPauseSettings)

	// Warm the cache synchronously before asserting its contents.
	// GetOpenAIQuotaAutoPauseSettings returns immediately and refreshes asynchronously;
	// Warm provides the synchronous entry point used by tests and startup.
	settings := svc.WarmOpenAIQuotaAutoPauseSettings(context.Background())
	if settings.DefaultThreshold5h != 0.95 {
		t.Fatalf("DefaultThreshold5h = %v, want 0.95", settings.DefaultThreshold5h)
	}
	if settings.DefaultThreshold7d != 0.9 {
		t.Fatalf("DefaultThreshold7d = %v, want 0.9", settings.DefaultThreshold7d)
	}

	// A subsequent Get returns the warmed thresholds.
	cached := svc.GetOpenAIQuotaAutoPauseSettings(context.Background())
	if cached.DefaultThreshold5h != 0.95 || cached.DefaultThreshold7d != 0.9 {
		t.Fatalf("cached read = %+v, want {0.95, 0.9}", cached)
	}
}

// TestGetOpenAIQuotaAutoPauseSettings_ColdCacheNonBlocking checks that a cold read returns zero defaults promptly.
// The background refresh supplies thresholds for later calls.
func TestGetOpenAIQuotaAutoPauseSettings_ColdCacheNonBlocking(t *testing.T) {
	repo := newQuotaReadFixture()
	repo.values[ops.SettingKeyOpsAdvancedSettings] = `{"openai_provider_quota_auto_pause":{"default_threshold_5h":0.7}}`
	svc := provider.NewQuotaSettingsCache(repo, settings.ErrSettingNotFound, ops.ParseRuntimeQuotaAutoPauseSettings)

	start := time.Now()
	settings := svc.GetOpenAIQuotaAutoPauseSettings(context.Background())
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("cold-cache Get must be non-blocking, took %v", elapsed)
	}
	// The initial read returns zero defaults even if the background refresh finishes quickly.
	if settings.DefaultThreshold5h != 0 || settings.DefaultThreshold7d != 0 {
		t.Fatalf("cold-cache Get = %+v, want zeroes", settings)
	}
}

// TestSetOpenAIQuotaAutoPauseSettings_VisibleImmediately checks that Set publishes thresholds
// for the next read without a database round trip.
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

func newQuotaReadFixture() *quotaReadFixture { return &quotaReadFixture{values: map[string]string{}} }

func (r *quotaReadFixture) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", settings.ErrSettingNotFound
}
