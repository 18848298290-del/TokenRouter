package scheduler

import (
	"context"
	"fmt"

	"github.com/TokenFlux/TokenRouter/internal/scheduler/policy"
)

// ValidationWeightSource reads current persisted weights in one batch for admin validation.
type ValidationWeightSource interface {
	GetMultiple(context.Context, []string) (map[string]string, error)
}

// LoadValidationWeights merges runtime overrides with startup weights and uses startup values for
// invalid overrides.
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
		return base, nil
	}
	return value, nil
}
