package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// Threshold reads select defaults or persisted percentages and publish an
// immutable cache entry after storage work finishes. These fixtures distinguish
// missing keys, storage failures, and parsing failures, protecting warning
// selection, cache lifetimes, detached read contexts, and caller-owned maps.

// errThresholdSettingMissing selects the absent-key path whose defaults
// should receive the ordinary observation lifetime rather than the shorter
// storage-error lifetime.
var errThresholdSettingMissing = errors.New("missing")

// thresholdSettingsRepo supplies synchronous settings values, injected
// errors, and a counted read for cache assertions. Its optional callback observes
// the read context and controls lookup completion within a virtual-time test.
type thresholdSettingsRepo struct {
	data          map[string]string // synchronous settings fixture map
	getValueErr   error             // injected settings read error
	getValueCalls int               // settings value read count
	// getValue overrides stored fixture data when present, allowing each
	// synchronous test to observe the read context and delay its result.
	getValue func(context.Context, string) (string, error)
}

// GetValue increments the fixture read count and invokes the optional callback
// or returns its stored string with the configured error. The fixture is
// unsynchronized and used by completed synchronous threshold reads.
func (r *thresholdSettingsRepo) GetValue(ctx context.Context, key string) (string, error) {
	r.getValueCalls++
	if r.getValue != nil {
		return r.getValue(ctx, key)
	}
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

// TestGetProviderSchedulingThresholds_ReadAndCacheBranches delays each settings
// outcome while the caller context is cancelled. It protects warning selection,
// publication-time expiration, detached context values and cleanup, and map
// isolation across fresh, cached, and expired reads.
func TestGetProviderSchedulingThresholds_ReadAndCacheBranches(t *testing.T) {
	storageErr := errors.New("settings store unavailable")
	tests := []struct {
		name    string         // descriptive settings outcome name
		raw     string         // persisted JSON test input
		err     error          // settings repository read error
		want    map[string]int // expected scheduling percentage map
		ttl     time.Duration  // selected cache entry lifetime
		warning string         // expected warning message text
	}{
		{
			name: "missing",
			raw:  `{"openai":19}`,
			err:  errThresholdSettingMissing,
			want: DefaultProviderSchedulingThresholds(),
			ttl:  providerSchedulingThresholdsCacheTTL,
		},
		{
			name: "wrapped_missing",
			raw:  `{"openai":19}`,
			err:  fmt.Errorf("lookup setting: %w", errThresholdSettingMissing),
			want: DefaultProviderSchedulingThresholds(),
			ttl:  providerSchedulingThresholdsCacheTTL,
		},
		{
			name:    "storage_error_with_valid_json",
			raw:     `{"openai":19}`,
			err:     storageErr,
			want:    DefaultProviderSchedulingThresholds(),
			ttl:     providerSchedulingThresholdsErrorTTL,
			warning: "failed to get provider scheduling thresholds, falling back to defaults",
		},
		{
			name:    "storage_error_with_invalid_json",
			raw:     `{"openai":79,"anthropic":`,
			err:     storageErr,
			want:    DefaultProviderSchedulingThresholds(),
			ttl:     providerSchedulingThresholdsErrorTTL,
			warning: "failed to get provider scheduling thresholds, falling back to defaults",
		},
		{
			name:    "invalid_json",
			raw:     `{"openai":79,"anthropic":`,
			want:    DefaultProviderSchedulingThresholds(),
			ttl:     providerSchedulingThresholdsCacheTTL,
			warning: "failed to parse provider scheduling thresholds, falling back to defaults",
		},
		{
			name: "blank",
			raw:  " \n\t ",
			want: DefaultProviderSchedulingThresholds(),
			ttl:  providerSchedulingThresholdsCacheTTL,
		},
		{
			name: "valid_json",
			raw:  " \n" + `{"openai":91,"kimi":42}` + " \t",
			want: map[string]int{
				PlatformOpenAI:    91,
				PlatformAnthropic: 100,
				PlatformGrok:      100,
				PlatformKimi:      42,
			},
			ttl: providerSchedulingThresholdsCacheTTL,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var warnings bytes.Buffer
				previousLogger := slog.Default()
				// Capture warnings for this synchronous read and restore the process
				// logger afterward so later tests keep their own logging setup.
				slog.SetDefault(slog.New(slog.NewJSONHandler(&warnings, nil)))
				t.Cleanup(func() { slog.SetDefault(previousLogger) })

				contextKey := struct{}{}
				callerCtx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey, "scheduling-read"))
				cancel()
				lookupStarted := time.Now()
				var readContext context.Context
				var lookupFinished time.Time
				repo := &thresholdSettingsRepo{
					getValue: func(ctx context.Context, key string) (string, error) {
						readContext = ctx
						require.Equal(t, SettingKeyProviderSchedulingThresholds, key)
						require.NoError(t, ctx.Err())
						require.Equal(t, "scheduling-read", ctx.Value(contextKey))
						deadline, ok := ctx.Deadline()
						require.True(t, ok)
						require.Equal(t, lookupStarted.Add(providerSchedulingThresholdsDBTimeout), deadline)
						// Advance the read clock before returning stored data so expiry
						// assertions detect lifetimes that start during lookup.
						time.Sleep(2 * time.Second)
						require.NoError(t, ctx.Err())
						lookupFinished = time.Now()
						return tc.raw, tc.err
					},
				}
				svc := NewRuntimeSettings(repo, errThresholdSettingMissing)

				got := svc.GetProviderSchedulingThresholds(callerCtx)
				require.Equal(t, tc.want, got)
				require.Equal(t, 1, repo.getValueCalls)
				require.ErrorIs(t, readContext.Err(), context.Canceled)
				cached, ok := svc.providerSchedulingThresholdsCache.Load().(*cachedProviderSchedulingThresholds)
				require.True(t, ok)
				require.Equal(t, tc.want, cached.thresholds)
				require.Equal(t, lookupFinished.Add(tc.ttl).UnixNano(), cached.expiresAt)

				if tc.warning == "" {
					require.Empty(t, warnings.String())
				} else {
					var record map[string]any
					require.NoError(t, json.Unmarshal(warnings.Bytes(), &record))
					require.Equal(t, "WARN", record["level"])
					require.Equal(t, tc.warning, record["msg"])
					require.NotEmpty(t, record["error"])
					if tc.err != nil {
						require.Equal(t, tc.err.Error(), record["error"])
					}
				}
				warnings.Reset()

				got[PlatformOpenAI] = 1
				delete(got, PlatformGrok)
				got[PlatformZhipu] = 3
				repo.getValue = nil
				repo.data = map[string]string{SettingKeyProviderSchedulingThresholds: `{"openai":73}`}
				got = svc.GetProviderSchedulingThresholds(callerCtx)
				require.Equal(t, tc.want, got)
				require.Equal(t, 1, repo.getValueCalls)
				got[PlatformOpenAI] = 2
				require.Equal(t, tc.want, cached.thresholds)

				// Reach the stored expiration to exercise the next read at its exact
				// refresh point for both ordinary observations and storage-error defaults.
				time.Sleep(tc.ttl)
				got = svc.GetProviderSchedulingThresholds(callerCtx)
				require.Equal(t, map[string]int{
					PlatformOpenAI:    73,
					PlatformAnthropic: 100,
					PlatformGrok:      100,
				}, got)
				require.Equal(t, 2, repo.getValueCalls)
				require.Empty(t, warnings.String())
			})
		})
	}
}
