package media

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// AlphaResult holds successful search call counts and the endpoint used.
type AlphaResult struct {
	RequestID, Model, UpstreamModel, UpstreamEndpoint string
	Headers, ResponseHeaders                          map[string][]string
	Duration                                          time.Duration
	Calls                                             int
}
type AlphaSelection struct {
	Provider   provider.ProviderSnapshot
	RetryLimit int
}
type AlphaOutcome struct {
	Result        *AlphaResult
	Err           error
	Failure       *failover.FailureInfo
	OutputChanged bool
}
type AlphaEvent struct {
	Kind                                          string
	Provider                                      provider.ProviderSnapshot
	Outcome                                       AlphaOutcome
	Switches, MaxSwitches, RetryLimit, RetryCount int
	Elapsed, RetryDelay                           time.Duration
}
type AlphaPorts interface {
	SelectAlpha(context.Context, map[int64]struct{}) (AlphaSelection, bool, error)
	AcquireAlpha(context.Context, AlphaSelection) (func(), bool)
	ForwardAlpha(context.Context, AlphaSelection, []byte) AlphaOutcome
	ReportAlpha(context.Context, AlphaSelection, *AlphaResult, bool, error)
	CompleteAlpha(context.Context, AlphaSelection, *AlphaResult)
	SwitchAlpha(AlphaSelection)
	StopAlpha429(AlphaSelection, int, int) bool
	ObserveAlpha(AlphaEvent)
	AlphaClientGone() bool
}
type AlphaFailure struct {
	Stage    string
	Err      error
	Outcome  AlphaOutcome
	Excluded int
}

// RunAlphaSearch manages separate same-provider retry and provider-switch budgets; output closes the retry window.
func RunAlphaSearch(ctx context.Context, body []byte, maxSwitches int, ports AlphaPorts) *AlphaFailure {
	if maxSwitches <= 0 {
		maxSwitches = 3
	}
	excluded := make(map[int64]struct{})
	retries := make(map[int64]int)
	var last AlphaOutcome
	switches := 0
	routingStarted := time.Now()
	for {
		selection, present, err := ports.SelectAlpha(ctx, excluded)
		if err != nil || !present {
			if ports.AlphaClientGone() {
				ports.ObserveAlpha(AlphaEvent{Kind: "select_canceled", Outcome: AlphaOutcome{Err: err}})
				return nil
			}
			return &AlphaFailure{Stage: "selection", Err: err, Outcome: last, Excluded: len(excluded)}
		}
		selected := selection.Provider
		ports.ObserveAlpha(AlphaEvent{Kind: "selected", Provider: selected})
		release, acquired := ports.AcquireAlpha(ctx, selection)
		if !acquired {
			return nil
		}
		ports.ObserveAlpha(AlphaEvent{Kind: "routing", Elapsed: time.Since(routingStarted)})
		started := time.Now()
		outcome := func() AlphaOutcome {
			if release != nil {
				defer release()
			}
			return ports.ForwardAlpha(ctx, selection, body)
		}()
		ports.ObserveAlpha(AlphaEvent{Kind: "response", Elapsed: time.Since(started)})
		if outcome.Err == nil {
			ports.ReportAlpha(ctx, selection, outcome.Result, true, nil)
			if outcome.Result != nil {
				ports.CompleteAlpha(ctx, selection, outcome.Result)
			}
			return nil
		}
		ports.ReportAlpha(ctx, selection, outcome.Result, false, outcome.Err)
		if outcome.Failure == nil {
			return &AlphaFailure{Stage: "forward", Err: outcome.Err, Outcome: outcome}
		}
		if outcome.OutputChanged {
			return &AlphaFailure{Stage: "exhausted", Outcome: outcome}
		}
		if ports.AlphaClientGone() {
			ports.ObserveAlpha(AlphaEvent{Kind: "forward_canceled", Provider: selected, Outcome: outcome})
			return nil
		}
		if outcome.Failure.RetryableOnSameProvider && failover.SameProviderRetryAllowed(outcome.Failure, retries[selected.ID], selection.RetryLimit) {
			retries[selected.ID]++
			delay := failover.SameProviderRetryDelayFor(outcome.Failure, retries[selected.ID])
			ports.ObserveAlpha(AlphaEvent{Kind: "retry", Provider: selected, Outcome: outcome, RetryLimit: selection.RetryLimit, RetryCount: retries[selected.ID], RetryDelay: delay})
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(delay):
			}
			continue
		}
		ports.SwitchAlpha(selection)
		excluded[selected.ID] = struct{}{}
		last = outcome
		if switches >= maxSwitches {
			return &AlphaFailure{Stage: "exhausted", Outcome: outcome}
		}
		switches++
		if ports.StopAlpha429(selection, outcome.Failure.StatusCode, switches) {
			return &AlphaFailure{Stage: "exhausted", Outcome: outcome}
		}
		ports.ObserveAlpha(AlphaEvent{Kind: "switch", Provider: selected, Outcome: outcome, Switches: switches, MaxSwitches: maxSwitches})
	}
}
