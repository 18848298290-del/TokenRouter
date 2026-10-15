package scheduler

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// parameters.go resolves scheduler scoring parameters from process defaults,
// runtime settings, and group policy.
// Advanced selection uses process weights until dynamic settings supply
// individual replacements. A valid merged set becomes the global scoring
// baseline, while an invalid set falls back as a whole to process weights.
// Checking combined sums matters because individually finite coefficients can
// overflow when added to inherited values.
//
// Parameters retains construction defaults and reads runtime changes through
// the shared SettingsRuntime cache. Scoring diagnostics use this same parameter
// source, and group resolution delegates sparse-field precedence to
// policy.ResolveEffective. Weight recovery returns the caller's process set as
// supplied, so callers remain responsible for its validity before using it for
// scheduling.

// ParameterDefaults holds process settings used before dynamic and group
// overrides are applied.
type ParameterDefaults struct {
	// TopK supplies the process candidate limit. Parameters.TopK and
	// policy.ResolveEffective use their default candidate count when this
	// value is nonpositive.
	TopK    int                    // sampled candidate count
	Weights policy.ScoreWeights    // process score coefficients
	Runtime policy.RuntimeSettings // process feedback and escape defaults
}

// Parameters binds setting sources at construction and caches dynamic values in SettingsRuntime.
type Parameters struct {
	// defaults captures process values at construction. Defaults returns a
	// value copy; any maps inside its Runtime member remain shared unless
	// the caller clones them.
	defaults ParameterDefaults
	source   RuntimeSettingSource // optional dynamic setting reader
	// runtime is shared with settings publication. A constructed
	// Parameters instance needs a non-nil runtime cache because Runtime
	// calls its Load method directly.
	runtime *SettingsRuntime
}

// DefaultParameters returns process-independent score, feedback, and escape
// defaults for scheduling.
func DefaultParameters() ParameterDefaults {
	return ParameterDefaults{TopK: 7, Weights: policy.ScoreWeights{Priority: 1, Load: 1, Queue: .7, ErrorRate: .8, TTFT: .5, Previous: 5, SessionSticky: 3}, Runtime: policy.RuntimeSettings{EwmaErrorRateAlpha: DefaultErrorRateAlpha, EwmaTTFTAlpha: DefaultTTFTAlpha, StickyEscape: policy.NormalizeStickyEscape(policy.StickyEscapeConfig{Enabled: true, TtftMs: 15000, ErrorRate: .5})}}
}

// NewParameters binds the shared runtime cache, optional settings source, and
// process defaults. Construction starts no background work; non-nil Parameters
// needs a usable runtime cache, and defaults are retained by value.
func NewParameters(runtime *SettingsRuntime, source RuntimeSettingSource, defaults ParameterDefaults) *Parameters {
	return &Parameters{runtime: runtime, source: source, defaults: defaults}
}

// Defaults returns the process parameter values, using scheduler defaults for
// a nil receiver. The returned struct is a value copy; maps inside Runtime
// retain their storage unless separately cloned.
func (p *Parameters) Defaults() ParameterDefaults {
	if p == nil {
		return DefaultParameters()
	}
	return p.defaults
}

// Runtime reads parsed dynamic settings through the bound shared TTL cache. A
// nil receiver loads scheduler defaults without a source; constructed
// instances require the non-nil cache supplied to NewParameters.
//
// WeightOverrides is copied for each returned value and is nil when no entries
// exist. Allocate a map before adding entries to that empty result.
func (p *Parameters) Runtime(ctx context.Context) policy.RuntimeSettings {
	if p == nil {
		return (&SettingsRuntime{}).Load(ctx, nil, DefaultParameters().Runtime)
	}
	return p.runtime.Load(ctx, p.source, p.defaults.Runtime)
}

// Effective resolves group overrides over the current global and process
// parameters. Nil ctx uses a background context; policy.ResolveEffective
// preserves supplied false and zero values and normalizes invalid merged
// weights.
func (p *Parameters) Effective(ctx context.Context, overrides policy.GroupAdvancedSchedulerOverrides) policy.EffectiveSettings {
	if ctx == nil {
		ctx = context.Background()
	}
	defaults := p.Defaults()
	return policy.ResolveEffective(defaults.TopK, defaults.Weights, p.Runtime(ctx), overrides)
}

// TopK returns the effective global candidate count for diagnostics and
// selection. A nonpositive process count uses seven, and a positive runtime
// override takes precedence; this method applies no group override.
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

// Weights returns global score coefficients after applying current dynamic
// overrides to process values. Each supplied field replaces its process
// coefficient, and the merged set needs finite nonnegative coefficients, finite
// combined sums, and a positive base sum.
//
// Rejection returns the complete process weight set because that is the
// configured baseline available to this global reader. Those process defaults
// pass through as supplied, so callers remain responsible for their validity on
// that path. Effective resolves group overrides separately.
func (p *Parameters) Weights(ctx context.Context) policy.ScoreWeights {
	base := p.Defaults().Weights
	overridden := policy.ApplyGlobalWeightOverrides(base, p.Runtime(ctx).WeightOverrides)
	if !overridden.ValidGlobal() {
		// Dynamic coefficients failed global validation. Retain the
		// process set so callers receive their configured baseline
		// instead of the unusable merged values.
		return base
	}
	return overridden
}
