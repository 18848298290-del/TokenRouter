package scheduler

import (
	"context"
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// parameterSourceStub provides scheduler text for parsing and precedence
// tests. Both readers return nil errors, allowing these tests to isolate
// inheritance and recovery from the supplied setting values.
type parameterSourceStub struct {
	// values borrows the configured setting map. Tests finish all map writes
	// before parameter reads and keep this shared storage stable during them.
	values map[string]string // configured setting text values
}

// TestParameters_DBOverridesConfig combines process defaults with selected
// persistent scheduler overrides. It checks effective Top-K, weights,
// independent feedback alphas, and escape settings while omitted factors
// retain process values.
func TestParameters_DBOverridesConfig(t *testing.T) {
	defaults := DefaultParameters()
	defaults.TopK = 11
	defaults.Weights = policy.ScoreWeights{
		Priority:      1,
		Load:          2,
		Queue:         3,
		ErrorRate:     4,
		TTFT:          5,
		Reset:         6,
		QuotaHeadroom: 7,
		Previous:      9,
		SessionSticky: 10,
	}
	repo := &parameterSourceStub{
		values: map[string]string{
			SettingKeyAdvancedSchedulerLBTopK:                 "3",
			SettingKeyAdvancedSchedulerEWMAErrorRateAlpha:     "0.4",
			SettingKeyAdvancedSchedulerEWMATTFTAlpha:          "0.6",
			SettingKeyAdvancedSchedulerStickyEscapeEnabled:    "false",
			SettingKeyAdvancedSchedulerStickyEscapeTTFTMs:     "9000",
			SettingKeyAdvancedSchedulerStickyEscapeErrorRate:  "0.25",
			SettingKeyAdvancedSchedulerWeightPriority:         "2.5",
			SettingKeyAdvancedSchedulerWeightReset:            "0.25",
			SettingKeyAdvancedSchedulerWeightPreviousResponse: "12",
		},
	}
	svc := NewParameters(NewSettingsRuntime(Diagnostics{}), repo, defaults)

	ctx := context.Background()
	require.Equal(t, 3, svc.TopK(ctx))
	weights := svc.Weights(ctx)
	require.Equal(t, 2.5, weights.Priority)
	require.Equal(t, 2.0, weights.Load)
	require.Equal(t, 0.25, weights.Reset)
	require.Equal(t, 12.0, weights.Previous)
	require.Equal(t, 10.0, weights.SessionSticky)
	effective := svc.Effective(ctx, policy.GroupAdvancedSchedulerOverrides{})
	require.InDelta(t, 0.4, effective.Feedback.ErrorRateAlpha, 0.000001)
	require.InDelta(t, 0.6, effective.Feedback.TtftAlpha, 0.000001)
	require.False(t, effective.StickyEscape.Enabled)
	require.InDelta(t, 9000, effective.StickyEscape.TtftMs, 0.000001)
	require.InDelta(t, 0.25, effective.StickyEscape.ErrorRate, 0.000001)
}

// TestParameters_InvalidWeightSumsFallBackToConfig supplies invalid weight
// text and finite overrides whose merged sums overflow. Each case requires the
// configured coefficients; the overflow cases protect recovery of the complete
// weight set. Invalid single text is omitted by settings parsing.
func TestParameters_InvalidWeightSumsFallBackToConfig(t *testing.T) {
	base := policy.ScoreWeights{
		Priority: 1, Load: 2, Queue: 3, ErrorRate: 4, TTFT: 5, Reset: 6,
		QuotaHeadroom: 7, Previous: 9, SessionSticky: 10,
	}
	maxFloat := strconv.FormatFloat(math.MaxFloat64, 'g', -1, 64)
	tests := []struct {
		name   string            // weight recovery scenario name
		values map[string]string // supplied persistent weight overrides
	}{
		{
			name: "invalid single value",
			values: map[string]string{
				SettingKeyAdvancedSchedulerWeightPriority: "NaN",
			},
		},
		{
			name: "base sum overflow",
			values: map[string]string{
				SettingKeyAdvancedSchedulerWeightPriority: maxFloat,
				SettingKeyAdvancedSchedulerWeightLoad:     maxFloat,
			},
		},
		{
			name: "sticky total sum overflow",
			values: map[string]string{
				SettingKeyAdvancedSchedulerWeightPriority:         maxFloat,
				SettingKeyAdvancedSchedulerWeightPreviousResponse: maxFloat,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defaults := DefaultParameters()
			defaults.Weights = base
			repo := &parameterSourceStub{values: tt.values}
			svc := NewParameters(NewSettingsRuntime(Diagnostics{}), repo, defaults)

			require.Equal(t, defaults.Weights, svc.Weights(context.Background()))
		})
	}
}

// GetMultiple returns a fresh map containing requested keys present in the
// fixture. Missing entries stay absent and the method reports no read error,
// letting parameter tests isolate parsing and inheritance behavior.
func (s *parameterSourceStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string)
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

// GetValue returns configured text for one setting or empty text when absent.
// It reports no source failure, so fixture recovery paths interpret the empty
// result through their ordinary inheritance rules.
func (s *parameterSourceStub) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}
