package text

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
)

// CountPorts supplies slot-free selection, one token-count attempt, and failure cleanup.
type CountPorts interface {
	Context() context.Context
	Select(map[int64]struct{}) (Selection, error)
	SelectionFailed(error, *AttemptFailure)
	Prepare(Selection) bool
	Forward(Selection) *AttemptFailure
	ForwardFailed(Selection, error)
	ReleaseSession(Selection)
	Exhausted(Selection, *AttemptFailure)
	Canceled()
	failover.TempUnscheduler[*AttemptFailure]
}

// RunCountTokens retries auxiliary counting with a rebuilt payload per attempt and no request slot.
func RunCountTokens(p CountPorts, maxSwitches int, observe failover.Observe) {
	state := failover.NewFailoverState[*AttemptFailure](maxSwitches, false, observe)
	for {
		selected, err := p.Select(state.FailedProviderIDs)
		if err != nil {
			p.SelectionFailed(err, state.LastFailoverErr)
			return
		}
		if !p.Prepare(selected) {
			return
		}
		failure := p.Forward(selected)
		if failure == nil {
			return
		}
		if failure.Policy == nil {
			p.ForwardFailed(selected, failure.Cause)
			p.ReleaseSession(selected)
			return
		}
		action := state.HandleFailoverError(p.Context(), p, selected.Provider.ID, selected.Provider.Platform, selected.RetryLimit, failure)
		p.ReleaseSession(selected)
		switch action {
		case failover.FailoverContinue:
			continue
		case failover.FailoverCanceled:
			p.Canceled()
			return
		default:
			p.Exhausted(selected, state.LastFailoverErr)
			return
		}
	}
}
