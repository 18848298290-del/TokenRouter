package text

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
)

// InputTokensPorts supplies counting operations without user slots or billing completion.
// Forward must release any provider slot returned by selection.
type InputTokensPorts interface {
	Context() context.Context
	Select(map[int64]struct{}) (Selection, bool, error)
	SelectionFailed(error, *AttemptFailure, bool)
	Forward(Selection) *AttemptFailure
	ForwardFailed(Selection, error)
	Exhausted(*AttemptFailure)
}

// RunInputTokens selects providers and counts input tokens within the counting endpoint's retry budget.
func RunInputTokens(p InputTokensPorts, maxSwitches int) {
	excluded := make(map[int64]struct{})
	same := make(map[int64]int)
	var last *AttemptFailure
	switches := 0
	for {
		selected, available, err := p.Select(excluded)
		if err != nil || !available {
			p.SelectionFailed(err, last, available)
			return
		}
		failure := p.Forward(selected)
		if failure == nil {
			return
		}
		if failure.Policy == nil {
			p.ForwardFailed(selected, failure.Cause)
			return
		}
		if !failure.Policy.RetryNext {
			p.Exhausted(failure)
			return
		}
		if failure.Policy.RetryableOnSameProvider && same[selected.Provider.ID] < selected.RetryLimit {
			same[selected.Provider.ID]++
			if !failover.SleepWithContext(p.Context(), failover.SameProviderRetryDelay) {
				return
			}
			continue
		}
		excluded[selected.Provider.ID] = struct{}{}
		last = failure
		if switches >= maxSwitches {
			p.Exhausted(failure)
			return
		}
		switches++
	}
}
