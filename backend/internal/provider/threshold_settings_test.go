package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var errThresholdSettingMissing = errors.New("missing")

// thresholdSettingsRepo counts reads and can simulate a missing setting.
type thresholdSettingsRepo struct {
	data          map[string]string
	getValueErr   error
	getValueCalls int
}

func (r *thresholdSettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	r.getValueCalls++
	return r.data[key], r.getValueErr
}

func (r *thresholdSettingsRepo) Set(_ context.Context, key, value string) error {
	r.data[key] = value
	return nil
}

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
