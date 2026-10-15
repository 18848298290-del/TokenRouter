package scheduler

import (
	"context"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// validation_weights.go loads current global score weights for administrative
// group validation.
// Administrative group validation needs coefficients read from persistence at
// the time of the update. Its batch read bypasses the request-side settings
// cache, so a cached hot-path recovery result cannot stand in for the current
// stored values. A read error remains an administrative validation failure.
//
// The baseline comes from accepted process or built-in admin defaults. Parsed
// dynamic overrides replace individual coefficients, then their combined
// global sums are checked. Invalid strings contribute no override, and an
// invalid merged set returns the baseline. Group validation applies its sparse
// values afterward, using the separate policy rules that permit a deliberately
// zero group base.

// ValidationWeightSource reads current persisted weights in one batch for admin validation.
type ValidationWeightSource interface {
	// GetMultiple returns persisted values for the requested scheduler
	// setting keys.
	GetMultiple(context.Context, []string) (map[string]string, error)
}

// LoadValidationWeights reads and resolves global coefficients for an
// administrative validation operation. Nil source uses process defaults, and
// each supplied source is read afresh in one batch.
//
// A batch error is returned with diagnostic context instead of retrying keys
// or using a cached value. Invalid individual override strings are skipped by
// parsing, and an invalid merged global set reverts to the process defaults.
// This keeps group validation separate from the hot-path cache recovery
// policy.
func LoadValidationWeights(ctx context.Context, source ValidationWeightSource, defaults AdminDefaults) (policy.ScoreWeights, error) {
	w := EffectiveAdminWeights(defaults)
	base := policy.ScoreWeights{Priority: w.Priority, Load: w.Load, Queue: w.Queue, ErrorRate: w.ErrorRate, TTFT: w.TTFT, Reset: w.Reset, QuotaHeadroom: w.QuotaHeadroom, Previous: w.PreviousResponse, SessionSticky: w.SessionSticky}
	if source == nil {
		return base, nil
	}
	values, err := source.GetMultiple(ctx, AdvancedSchedulerRuntimeSettingKeys())
	if err != nil {
		return policy.ScoreWeights{}, fmt.Errorf("load advanced scheduler settings: %w", err)
	}
	value := policy.ApplyGlobalWeightOverrides(base, ParseAdvancedSchedulerWeightOverrides(values))
	configured := policy.ConfigScoreWeights{Priority: value.Priority, Load: value.Load, Queue: value.Queue, ErrorRate: value.ErrorRate, TTFT: value.TTFT, Reset: value.Reset, QuotaHeadroom: value.QuotaHeadroom, PreviousResponse: value.Previous, SessionSticky: value.SessionSticky}
	if !configured.IsValid() {
		// The merged dynamic set is unusable for global scoring.
		// Validate groups against the accepted process coefficients
		// while retaining fresh-read errors as failures.
		return base, nil
	}
	return value, nil
}
