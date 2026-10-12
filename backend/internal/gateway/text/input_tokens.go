package text

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
)

// Responses input-token counting maintains its own retry counters independently
// of generation completion. Forwarding owns any acquired provider slot, so each
// count finishes its resources before the loop waits or selects another
// provider.
//
// A policy can permit same-provider retries up to the selected limit before
// that provider enters the excluded set. Ordinary switching consumes its
// separate allowance after recording the failed target, while canceled retry
// waiting ends the loop directly. Initial selection still occurs with a zero
// switch allowance, and selection exhaustion receives the last classified
// failure for HTTP formatting. Unclassified forwarding errors end immediately
// through their adapter operation.

// InputTokensPorts supplies input-count selection, forwarding and terminal
// output operations. Forward releases any selected provider resource before
// returning; the loop performs no user-slot acquisition or generation
// completion submission.
type InputTokensPorts interface {
	// Context returns the current nonnil counting request context used by
	// same-provider waiting. Forward and selection own their earlier cancellation
	// checks; the loop returns directly when its wait observes cancellation.
	Context() context.Context
	// Select captures a usable count target using excluded provider IDs and
	// reports availability. It can return provider resources owned by Forward, and
	// selection failure receives the last classified attempt for terminal
	// formatting.
	Select(map[int64]struct{}) (Selection, bool, error)
	// SelectionFailed handles selection error or unavailable target alongside
	// preceding failure and availability evidence. The adapter owns final output;
	// the loop performs no new Forward after this callback.
	SelectionFailed(error, *AttemptFailure, bool)
	// Forward counts the supplied selection and releases any selected provider
	// resource before returning. Nil failure ends successfully; nonnil failure
	// selects ordinary terminal handling or retry policy without generating
	// inference completion work.
	Forward(Selection) *AttemptFailure
	// ForwardFailed handles an unclassified count failure with selected provider
	// metadata. Forward has already retired its resource, and the loop ends
	// without consuming another retry or switch opportunity.
	ForwardFailed(Selection, error)
	// Exhausted formats the classified count failure when retry is disallowed or
	// switching allowance ends. The adapter owns the response, and every selected
	// Forward has already handled its acquired provider resource.
	Exhausted(*AttemptFailure)
}

// RunInputTokens coordinates serial count attempts using selected same-provider
// limits and a separate switch allowance. Callers supply resource-owning
// Forward and usable context/output operations; initial execution remains
// possible with a nonpositive switch allowance.
//
// Policy-free errors stop immediately and RetryNext false produces exhaustion.
// Same-provider counts increase before a context-aware fixed delay, whose
// observed cancellation stops the loop without another terminal callback. After that
// allowance ends, the provider is excluded and the last classified failure is
// retained for selection errors. This loop uses the selected RetryLimit
// directly and leaves provider health effects, request output and resources to
// adapters.
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
			// Forward has already released its selected provider resource. When the wait
			// observes cancellation, return before selecting or forwarding another count attempt.
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
