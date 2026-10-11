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

// runtimeSettingFixture 记录批量读取失败后逐键读取的完整键序列。
type runtimeSettingFixture struct {
	RuntimeSettingSource
	keys  []string
	batch int
	fail  bool
}

type runtimeSettingReadFixture struct {
	values        map[string]string
	batchErr      error
	valueErrors   map[string]error
	batchKeys     []string
	steps         []string
	contexts      []context.Context
	contextErrors []error
}

func (f *runtimeSettingFixture) GetMultiple(context.Context, []string) (map[string]string, error) {
	f.batch++
	if f.fail {
		return nil, errors.New("批量读取失败")
	}
	return map[string]string{SettingKeyAdvancedSchedulerWeightLoad: "0"}, nil
}

func (f *runtimeSettingFixture) GetValue(_ context.Context, key string) (string, error) {
	f.keys = append(f.keys, key)
	if key == SettingKeyAdvancedSchedulerWeightLoad {
		return "0", nil
	}
	return "", nil
}

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
		name        string
		values      map[string]string
		valueErrors map[string]error
		want        policy.RuntimeSettings
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

func (f *runtimeSettingReadFixture) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	f.batchKeys = append([]string(nil), keys...)
	f.steps = append(f.steps, "batch")
	f.contexts = append(f.contexts, ctx)
	f.contextErrors = append(f.contextErrors, ctx.Err())
	return f.values, f.batchErr
}

func (f *runtimeSettingReadFixture) GetValue(ctx context.Context, key string) (string, error) {
	f.steps = append(f.steps, "value:"+key)
	f.contexts = append(f.contexts, ctx)
	f.contextErrors = append(f.contextErrors, ctx.Err())
	return f.values[key], f.valueErrors[key]
}

// TestValidationWeightsReadFreshAndPropagateFailure 检查管理写入校验读取当前设置，并返回批量读取错误。
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
