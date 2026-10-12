package scheduler

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// ParameterDefaults contains static scheduling settings supplied by app.
type ParameterDefaults struct {
	TopK    int
	Weights policy.ScoreWeights
	Runtime policy.RuntimeSettings
}

// Parameters binds setting sources at construction and caches dynamic values in SettingsRuntime.
type Parameters struct {
	defaults ParameterDefaults
	source   RuntimeSettingSource
	runtime  *SettingsRuntime
}

// DefaultParameters returns defaults for use when process settings are absent.
func DefaultParameters() ParameterDefaults {
	return ParameterDefaults{TopK: 7, Weights: policy.ScoreWeights{Priority: 1, Load: 1, Queue: .7, ErrorRate: .8, TTFT: .5, Previous: 5, SessionSticky: 3}, Runtime: policy.RuntimeSettings{EwmaErrorRateAlpha: DefaultErrorRateAlpha, EwmaTTFTAlpha: DefaultTTFTAlpha, StickyEscape: policy.NormalizeStickyEscape(policy.StickyEscapeConfig{Enabled: true, TtftMs: 15000, ErrorRate: .5})}}
}

func NewParameters(runtime *SettingsRuntime, source RuntimeSettingSource, defaults ParameterDefaults) *Parameters {
	return &Parameters{runtime: runtime, source: source, defaults: defaults}
}

// Defaults returns static settings by value so runtime overrides can be applied to a copy.
func (p *Parameters) Defaults() ParameterDefaults {
	if p == nil {
		return DefaultParameters()
	}
	return p.defaults
}

// Runtime reads dynamic settings through the shared TTL cache, using defaults for a nil receiver.
func (p *Parameters) Runtime(ctx context.Context) policy.RuntimeSettings {
	if p == nil {
		return (&SettingsRuntime{}).Load(ctx, nil, DefaultParameters().Runtime)
	}
	return p.runtime.Load(ctx, p.source, p.defaults.Runtime)
}

// Effective applies the supplied group overrides to runtime settings.
func (p *Parameters) Effective(ctx context.Context, overrides policy.GroupAdvancedSchedulerOverrides) policy.EffectiveSettings {
	if ctx == nil {
		ctx = context.Background()
	}
	defaults := p.Defaults()
	return policy.ResolveEffective(defaults.TopK, defaults.Weights, p.Runtime(ctx), overrides)
}

// TopK returns the global candidate count used by diagnostics.
func (p *Parameters) TopK(ctx context.Context) int {
	base := p.Defaults().TopK
	if base <= 0 {
		base = 7
	}
	if settings := p.Runtime(ctx); settings.LbTopKOverride > 0 {
		return settings.LbTopKOverride
	}
	return base
}

func (p *Parameters) Weights(ctx context.Context) policy.ScoreWeights {
	base := p.Defaults().Weights
	overridden := policy.ApplyGlobalWeightOverrides(base, p.Runtime(ctx).WeightOverrides)
	if !overridden.ValidGlobal() {
		return base
	}
	return overridden
}
