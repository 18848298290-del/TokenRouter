package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// An absent threshold key is an unconfigured state rather than a database
// outage. This fixture distinguishes those cases with a supplied sentinel and
// read counter, protecting default values and normal cache expiration when no
// platform settings have been stored.

// errThresholdSettingMissing selects the absent-key path whose defaults
// should receive the ordinary observation lifetime rather than the shorter
// storage-error lifetime.
var errThresholdSettingMissing = errors.New("missing")

// thresholdSettingsRepo supplies synchronous settings values, injected
// errors, and a counted read for cache assertions.
type thresholdSettingsRepo struct {
	data          map[string]string // synchronous settings fixture map
	getValueErr   error             // injected settings read error
	getValueCalls int               // settings value read count
}

// GetValue increments the fixture read count and returns its stored string
// with the configured error. The fixture is unsynchronized and used by
// completed synchronous threshold reads.
func (r *thresholdSettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	r.getValueCalls++
	return r.data[key], r.getValueErr
}

// Set replaces a scalar settings value for later synchronous fixture reads.
func (r *thresholdSettingsRepo) Set(_ context.Context, key, value string) error {
	r.data[key] = value
	return nil
}

// TestGetProviderSchedulingThresholds_MissingSettingUsesDefaultsAndNormalCacheTTL
// makes the repository report the missing-key sentinel, then changes its
// stored map. It verifies defaults remain cached without another read and
// expiration reflects the normal lifetime.
func TestGetProviderSchedulingThresholds_MissingSettingUsesDefaultsAndNormalCacheTTL(t *testing.T) {
	repo := &thresholdSettingsRepo{data: map[string]string{}}
	svc := NewRuntimeSettings(repo, errThresholdSettingMissing)
	repo.getValueErr = errThresholdSettingMissing

	got := svc.GetProviderSchedulingThresholds(context.Background())
	require.Equal(t, DefaultProviderSchedulingThresholds(), got)
	require.Equal(t, 1, repo.getValueCalls)

	repo.data[SettingKeyProviderSchedulingThresholds] = `{"openai":91}`
	got = svc.GetProviderSchedulingThresholds(context.Background())
	require.Equal(t, 100, got[PlatformOpenAI], "未配置时的默认值应按正常周期保持缓存")
	require.Equal(t, 1, repo.getValueCalls)

	cached, ok := svc.providerSchedulingThresholdsCache.Load().(*cachedProviderSchedulingThresholds)
	require.True(t, ok)
	require.Greater(t, cached.expiresAt, time.Now().Add(providerSchedulingThresholdsCacheTTL-time.Second).UnixNano())
}
