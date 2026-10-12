package media

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// An Alpha Search attempt can retry the same provider before selecting
// another, but an observed output change closes that window. Same-provider
// counts belong to provider IDs, while a separate switch count limits new
// selections and a failure record carries the last attempt into the HTTP
// error adapter.
//
// Each acquired slot surrounds only ForwardAlpha and is released before
// scheduling feedback or usage completion. Failure reporting precedes the
// output-change decision, and client departure or failed slot acquisition
// returns nil because the adapter already owns that response. Switching
// excludes the selected provider before checking exhaustion; nonpositive
// configured allowance selects three switches.

// AlphaResult carries search response observations into scheduling and
// usage completion.
type AlphaResult struct {
	// RequestID identifies upstream execution, Model names the client
	// response, UpstreamModel names provider execution, and UpstreamEndpoint
	// records the selected search route. These values describe the same
	// attempt for completion.
	RequestID, Model, UpstreamModel, UpstreamEndpoint string // upstream request identifier, client response model, provider execution model and selected upstream endpoint
	// Headers and ResponseHeaders retain mutable header maps and
	// slices supplied by ForwardAlpha. The loop passes their references to
	// report and completion callbacks without cloning.
	Headers, ResponseHeaders map[string][]string // upstream header evidence and returned response headers
	Duration                 time.Duration       // observed attempt elapsed time
	Calls                    int                 // observed billable search calls
}

// AlphaSelection pairs the selected provider snapshot with its
// same-provider retry limit.
type AlphaSelection struct {
	Provider   provider.ProviderSnapshot // selected search provider snapshot
	RetryLimit int                       // same-provider retry policy limit
}

// AlphaOutcome carries search observations and recovery classification from
// one forwarding call.
type AlphaOutcome struct {
	// Result may accompany an error or be absent on success. RunAlphaSearch
	// passes it to reporting, and invokes successful completion only when the
	// pointer is nonnil.
	Result *AlphaResult // optional search response observations
	Err    error        // forwarding result error value
	// Failure is required for the loop to consider recovery after Err. A nil
	// classification returns a forward-stage failure regardless of the
	// output-change flag.
	Failure *failover.FailureInfo // classified attempt recovery evidence
	// OutputChanged ends classified-error recovery after scheduling feedback
	// has been sent. The adapter measures writer changes; this flag does not
	// establish receipt by the client.
	OutputChanged bool // observed downstream output change
}

// AlphaEvent records the active search-loop stage and its available
// observations.
type AlphaEvent struct {
	Kind     string                    // search execution stage label
	Provider provider.ProviderSnapshot // stage-selected provider snapshot
	Outcome  AlphaOutcome              // shared forwarding outcome evidence
	// Switches and MaxSwitches describe provider transitions; RetryLimit and
	// RetryCount describe repeated attempts for one provider. Observers
	// interpret counters according to Kind because unrelated event fields
	// remain zero.
	Switches, MaxSwitches, RetryLimit, RetryCount int           // used provider transitions, allowed provider transitions, same-provider retry limit and used same-provider retries
	Elapsed, RetryDelay                           time.Duration // observed phase elapsed time and selected same-provider retry delay
}

// AlphaPorts binds Alpha Search selection, output policy, scheduling
// feedback and completion.
type AlphaPorts interface {
	// SelectAlpha chooses a candidate using ctx and the excluded provider IDs.
	// An error or absent candidate ends selection; the loop consults
	// AlphaClientGone before returning a selection failure.
	SelectAlpha(context.Context, map[int64]struct{}) (AlphaSelection, bool, error)
	// AcquireAlpha waits for the selected provider slot and reports admission.
	// False ends execution with adapter-owned handling; a nonnil release is
	// invoked after ForwardAlpha, before any feedback or completion.
	AcquireAlpha(context.Context, AlphaSelection) (func(), bool)
	// ForwardAlpha performs one search attempt with the shared request body
	// and selected provider. It returns observed output and recovery
	// classification; the loop leaves byte ownership and response emission
	// with the adapter.
	ForwardAlpha(context.Context, AlphaSelection, []byte) AlphaOutcome
	// ReportAlpha records scheduling feedback after the attempt slot has been
	// released. It receives optional result evidence, a success flag and the
	// error, including failures whose output already changed.
	ReportAlpha(context.Context, AlphaSelection, *AlphaResult, bool, error)
	// CompleteAlpha submits successful search observations after slot release
	// and scheduling feedback. The loop calls it only for a nonnil result; the
	// implementation owns usage recording and its failures.
	CompleteAlpha(context.Context, AlphaSelection, *AlphaResult)
	// SwitchAlpha applies adapter bookkeeping before excluding the current
	// provider from future selection. It also runs when the switch allowance
	// is already exhausted, before the loop returns its terminal failure.
	SwitchAlpha(AlphaSelection)
	// StopAlpha429 decides whether a classified status ends switching after
	// the switch counter advances. It receives the selected provider and
	// updated count, and true returns an exhausted failure before another
	// selection.
	StopAlpha429(AlphaSelection, int, int) bool
	// ObserveAlpha receives an execution event synchronously at the indicated
	// stage. Event result pointers and provider data retain their supplied
	// references; observers arrange any copying needed for later processing.
	ObserveAlpha(AlphaEvent)
	// AlphaClientGone reports whether the request adapter considers the client
	// unavailable. The loop uses it to suppress final selection/forwarding
	// failure handling and to stop recovery after reporting a classified
	// failure.
	AlphaClientGone() bool
}

// AlphaFailure identifies terminal search state for the HTTP error adapter.
type AlphaFailure struct {
	Stage    string       // selection, forward or exhausted stage
	Err      error        // direct selection or forwarding error
	Outcome  AlphaOutcome // terminal or prior attempt evidence
	Excluded int          // excluded provider count for selection
}

// RunAlphaSearch performs admitted search attempts with separate
// same-provider retry and provider-switch accounting. Nonpositive
// maxSwitches uses three; each available release callback runs immediately
// after forwarding, before feedback and completion. Nil can mean success,
// client departure or adapter-handled admission failure.
//
// A classified failure is reported before OutputChanged stops recovery.
// Same-provider retries retain the provider's count and wait under ctx
// cancellation; later switching records exclusion before testing the switch
// allowance and status-specific stop rule. Selection failures include the
// prior switched outcome, and a nonnil successful result alone reaches
// CompleteAlpha.
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
		// When selection fails or yields no candidate, client departure
		// suppresses final HTTP error handling because the adapter no longer has
		// a usable recipient.
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
		// Keep the acquired slot around forwarding only; deferred release finishes
		// before report and completion callbacks can perform their independent
		// scheduling or usage work.
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
		// Scheduling has already observed this failed attempt. Changed output
		// prevents retrying its body through another attempt that could append an
		// incompatible response.
		if outcome.OutputChanged {
			return &AlphaFailure{Stage: "exhausted", Outcome: outcome}
		}
		// After failure feedback, client departure stops recovery and suppresses
		// a terminal response; another attempt would have no client to consume
		// its search output.
		if ports.AlphaClientGone() {
			ports.ObserveAlpha(AlphaEvent{Kind: "forward_canceled", Provider: selected, Outcome: outcome})
			return nil
		}
		// Retryable classification and remaining provider allowance select a wait
		// on delay and ctx.Done; when cancellation wins that select, return
		// before the next selection.
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
		// Apply switch bookkeeping and record exclusion even when the remaining
		// allowance is exhausted, retaining this outcome for a later selection
		// failure.
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
