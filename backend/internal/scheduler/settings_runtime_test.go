package scheduler

import (
	"context"
	"errors"
	"maps"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// runtimeSettingFixture supplies a fixed load weight and records individual
// recovery reads.
type runtimeSettingFixture struct {
	RuntimeSettingSource          // embedded unused setting source
	keys                 []string // individual recovery read sequence
	batch                int      // batch source read count
	fail                 bool     // configured batch failure switch
}

// runtimeSettingReadFixture records setting lookups and their contexts for
// sequential batch and recovery scenarios.
type runtimeSettingReadFixture struct {
	// values is returned directly by GetMultiple. Tests finish configuring the
	// map before calling Load and serialize source operations because this
	// fixture has no internal synchronization.
	values   map[string]string // configured stored setting text
	batchErr error             // configured batch read failure
	// valueErrors is borrowed from the active table case and read by GetValue.
	// Tests configure this map before Load and keep it stable throughout the
	// sequential recovery reads.
	valueErrors map[string]error // configured per-key read failures
	batchKeys   []string         // copied requested batch keys
	steps       []string         // ordered lookup and diagnostic events
	// contexts and contextErrors record the same read calls in matching
	// positions. Context references remain observable after Load cancels its
	// budget, while contextErrors captures each call's earlier state.
	contexts      []context.Context // retained read context references
	contextErrors []error           // context errors captured during reads
}

// GetMultiple counts batch reads and returns a load-weight override of zero.
// Setting fail returns the configured batch-failure scenario, causing
// SettingsRuntime to use the fixture's individual-key methods.
func (f *runtimeSettingFixture) GetMultiple(context.Context, []string) (map[string]string, error) {
	f.batch++
	if f.fail {
		return nil, errors.New("批量读取失败")
	}
	return map[string]string{SettingKeyAdvancedSchedulerWeightLoad: "0"}, nil
}

// GetValue records recovery key order and returns zero text for the load
// weight. Other keys return empty text so the runtime leaves their overrides
// absent or selects applicable defaults.
func (f *runtimeSettingFixture) GetValue(_ context.Context, key string) (string, error) {
	f.keys = append(f.keys, key)
	if key == SettingKeyAdvancedSchedulerWeightLoad {
		return "0", nil
	}
	return "", nil
}

// TestSettingsRuntimePreservesFallbackAndIsolatesValues forces individual
// reads after a batch failure. It checks key order, cache reuse, and
// independent weight maps for returned values and Store input.
func TestSettingsRuntimePreservesFallbackAndIsolatesValues(t *testing.T) {
	source := &runtimeSettingFixture{fail: true}
	runtime := NewSettingsRuntime(Diagnostics{})
	first := runtime.Load(context.Background(), source, policy.RuntimeSettings{})
	require.Equal(t, AdvancedSchedulerRuntimeSettingKeys(), source.keys)
	require.Equal(t, float64(0), first.WeightOverrides["load"])
	first.WeightOverrides["load"] = 7
	second := runtime.Load(context.Background(), source, policy.RuntimeSettings{})
	require.Equal(t, 1, source.batch)
	require.Equal(t, float64(0), second.WeightOverrides["load"])
	value := policy.RuntimeSettings{WeightOverrides: map[string]float64{"priority": 2}, LbTopKOverride: 3}
	runtime.Store(value)
	value.WeightOverrides["priority"] = 9
	third := runtime.Load(context.Background(), source, policy.RuntimeSettings{})
	require.Equal(t, float64(2), third.WeightOverrides["priority"])
	require.Equal(t, 3, third.LbTopKOverride)
}

// TestSettingsRuntimeLoadsBatchAndFallback compares configured, missing, and
// partly failed settings across batch and recovery reads. It checks values,
// presence flags, lookup order, detached canceled-caller contexts, and their
// shared two-second budget.
func TestSettingsRuntimeLoadsBatchAndFallback(t *testing.T) {
	defaults := policy.RuntimeSettings{
		EwmaErrorRateAlpha: 0.2,
		EwmaTTFTAlpha:      0.3,
		StickyEscape:       policy.StickyEscapeConfig{Enabled: true, TtftMs: 7500, ErrorRate: 0.25},
	}
	wantDefaults := policy.RuntimeSettings{
		EwmaErrorRateAlpha:    0.2,
		EwmaTTFTAlpha:         0.3,
		StickyEscapeEnabled:   true,
		StickyEscapeTTFTMs:    7500,
		StickyEscapeErrorRate: 0.25,
		StickyEscape:          defaults.StickyEscape,
	}
	wantKeys := []string{
		SettingKeyAdvancedSchedulerStickyWeightedEnabled,
		SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled,
		SettingKeyAdvancedSchedulerLBTopK,
		SettingKeyAdvancedSchedulerEWMAErrorRateAlpha,
		SettingKeyAdvancedSchedulerEWMATTFTAlpha,
		SettingKeyAdvancedSchedulerStickyEscapeEnabled,
		SettingKeyAdvancedSchedulerStickyEscapeTTFTMs,
		SettingKeyAdvancedSchedulerStickyEscapeErrorRate,
		SettingKeyAdvancedSchedulerWeightPriority,
		SettingKeyAdvancedSchedulerWeightLoad,
		SettingKeyAdvancedSchedulerWeightQueue,
		SettingKeyAdvancedSchedulerWeightErrorRate,
		SettingKeyAdvancedSchedulerWeightTTFT,
		SettingKeyAdvancedSchedulerWeightReset,
		SettingKeyAdvancedSchedulerWeightQuotaHeadroom,
		SettingKeyAdvancedSchedulerWeightPreviousResponse,
		SettingKeyAdvancedSchedulerWeightSessionSticky,
	}
	readErr := errors.New("setting read failed")
	tests := []struct {
		name        string                 // scenario subtest display name
		values      map[string]string      // supplied persistent scheduler text
		valueErrors map[string]error       // configured individual read failures
		want        policy.RuntimeSettings // expected parsed runtime settings
	}{
		{
			name: "configured values",
			values: map[string]string{
				SettingKeyAdvancedSchedulerStickyWeightedEnabled:       " TrUe ",
				SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled: " TRUE ",
				SettingKeyAdvancedSchedulerLBTopK:                      " 3 ",
				SettingKeyAdvancedSchedulerEWMAErrorRateAlpha:          " 0.4 ",
				SettingKeyAdvancedSchedulerEWMATTFTAlpha:               "0.6",
				SettingKeyAdvancedSchedulerStickyEscapeEnabled:         " false ",
				SettingKeyAdvancedSchedulerStickyEscapeTTFTMs:          "9000",
				SettingKeyAdvancedSchedulerStickyEscapeErrorRate:       "0",
				SettingKeyAdvancedSchedulerWeightPriority:              "2.5",
				SettingKeyAdvancedSchedulerWeightLoad:                  " 0 ",
				SettingKeyAdvancedSchedulerWeightQueue:                 "3",
				SettingKeyAdvancedSchedulerWeightErrorRate:             "4",
				SettingKeyAdvancedSchedulerWeightTTFT:                  "5",
				SettingKeyAdvancedSchedulerWeightReset:                 "6",
				SettingKeyAdvancedSchedulerWeightQuotaHeadroom:         "7",
				SettingKeyAdvancedSchedulerWeightPreviousResponse:      "8",
				SettingKeyAdvancedSchedulerWeightSessionSticky:         "9",
			},
			want: policy.RuntimeSettings{
				StickyWeightedEnabled:       true,
				SubscriptionPriorityEnabled: true,
				LbTopKOverride:              3,
				WeightOverrides: map[string]float64{
					"priority": 2.5, "load": 0, "queue": 3, "error_rate": 4, "ttft": 5,
					"reset": 6, "quota_headroom": 7, "previous_response": 8, "session_sticky": 9,
				},
				EwmaErrorRateAlpha:       0.4,
				EwmaErrorRateAlphaSet:    true,
				EwmaTTFTAlpha:            0.6,
				EwmaTTFTAlphaSet:         true,
				StickyEscapeEnabled:      false,
				StickyEscapeEnabledSet:   true,
				StickyEscapeTTFTMs:       9000,
				StickyEscapeTTFTMsSet:    true,
				StickyEscapeErrorRate:    0,
				StickyEscapeErrorRateSet: true,
				StickyEscape:             policy.StickyEscapeConfig{Enabled: false, TtftMs: 9000, ErrorRate: 0},
			},
		},
		{
			name: "partial failures and invalid values",
			values: map[string]string{
				SettingKeyAdvancedSchedulerStickyWeightedEnabled:       "true",
				SettingKeyAdvancedSchedulerSubscriptionPriorityEnabled: "1",
				SettingKeyAdvancedSchedulerLBTopK:                      "-3",
				SettingKeyAdvancedSchedulerEWMAErrorRateAlpha:          "0.4",
				SettingKeyAdvancedSchedulerEWMATTFTAlpha:               "NaN",
				SettingKeyAdvancedSchedulerStickyEscapeEnabled:         "false",
				SettingKeyAdvancedSchedulerStickyEscapeTTFTMs:          "0",
				SettingKeyAdvancedSchedulerStickyEscapeErrorRate:       "2",
				SettingKeyAdvancedSchedulerWeightPriority:              "2.5",
				SettingKeyAdvancedSchedulerWeightQueue:                 "-1",
				SettingKeyAdvancedSchedulerWeightTTFT:                  "+Inf",
				SettingKeyAdvancedSchedulerWeightReset:                 " ",
			},
			valueErrors: map[string]error{
				SettingKeyAdvancedSchedulerStickyWeightedEnabled: readErr,
				SettingKeyAdvancedSchedulerEWMAErrorRateAlpha:    readErr,
				SettingKeyAdvancedSchedulerStickyEscapeEnabled:   readErr,
				SettingKeyAdvancedSchedulerWeightPriority:        readErr,
			},
			want: wantDefaults,
		},
		{name: "missing values", want: wantDefaults},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var batchResult policy.RuntimeSettings
			for _, fallback := range []bool{false, true} {
				mode := "batch"
				if fallback {
					mode = "fallback"
				}
				t.Run(mode, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						source := &runtimeSettingReadFixture{values: maps.Clone(tt.values), valueErrors: tt.valueErrors}
						wantSteps := []string{"batch"}
						if fallback {
							source.batchErr = errors.New("batch read failed")
							wantSteps = append(wantSteps, "warn:advanced_scheduler_settings_batch_load_failed")
							for _, key := range wantKeys {
								wantSteps = append(wantSteps, "value:"+key)
							}
						} else {
							for key := range tt.valueErrors {
								delete(source.values, key)
							}
						}
						runtime := NewSettingsRuntime(Diagnostics{Event: func(level, event string, fields ...any) {
							source.steps = append(source.steps, level+":"+event)
							require.Equal(t, []any{"error", source.batchErr}, fields)
						}})
						// Cancel the initiating request before loading settings. Recorded source contexts
						// must still be live during reads and become canceled when Load returns.
						ctx, cancel := context.WithCancel(context.Background())
						cancel()
						startedAt := time.Now()
						got := runtime.Load(ctx, source, defaults)
						require.Equal(t, tt.want, got)
						if fallback {
							require.Equal(t, batchResult, got)
						} else {
							batchResult = got
						}
						require.Equal(t, wantKeys, source.batchKeys)
						require.Equal(t, wantSteps, source.steps)
						for i, readCtx := range source.contexts {
							require.NoError(t, source.contextErrors[i])
							require.Equal(t, source.contexts[0], readCtx)
							deadline, ok := readCtx.Deadline()
							require.True(t, ok)
							require.Equal(t, startedAt.Add(2*time.Second), deadline)
							require.ErrorIs(t, readCtx.Err(), context.Canceled)
						}
					})
				})
			}
		})
	}
	t.Run("no source", func(t *testing.T) {
		runtime := NewSettingsRuntime(Diagnostics{})
		require.Equal(t, wantDefaults, runtime.Load(context.Background(), nil, defaults))
	})
}

// GetMultiple records copied batch keys, read order, context identity, and the
// current context error. It returns the fixture's borrowed value map and
// configured batch error for the active scenario.
func (f *runtimeSettingReadFixture) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	f.batchKeys = append([]string(nil), keys...)
	f.steps = append(f.steps, "batch")
	f.contexts = append(f.contexts, ctx)
	f.contextErrors = append(f.contextErrors, ctx.Err())
	return f.values, f.batchErr
}

// GetValue records one recovery lookup with its context and current
// cancellation state. Missing map entries return empty text or nil errors,
// matching the fixture's absent-setting scenarios.
func (f *runtimeSettingReadFixture) GetValue(ctx context.Context, key string) (string, error) {
	f.steps = append(f.steps, "value:"+key)
	f.contexts = append(f.contexts, ctx)
	f.contextErrors = append(f.contextErrors, ctx.Err())
	return f.values[key], f.valueErrors[key]
}

// TestValidationWeightsReadFreshAndPropagateFailure performs repeated
// administrative validation reads followed by a batch error. It checks fresh
// lookups, explicit zero weight overrides, and error propagation
// without individual-key recovery.
func TestValidationWeightsReadFreshAndPropagateFailure(t *testing.T) {
	source := &runtimeSettingFixture{}
	defaults := DefaultAdminSettingsDefaults()
	first, err := LoadValidationWeights(context.Background(), source, defaults)
	require.NoError(t, err)
	require.Zero(t, first.Load, "显式零权重必须覆盖启动值")
	require.Equal(t, defaults.Weights.Priority, first.Priority)
	_, err = LoadValidationWeights(context.Background(), source, defaults)
	require.NoError(t, err)
	require.Equal(t, 2, source.batch, "每次管理校验都读取最新值")
	source.fail = true
	_, err = LoadValidationWeights(context.Background(), source, defaults)
	require.ErrorContains(t, err, "load advanced scheduler settings: 批量读取失败")
	require.Empty(t, source.keys, "管理校验不使用热路径的逐键降级")
}
