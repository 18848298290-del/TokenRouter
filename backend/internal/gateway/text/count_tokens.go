package text

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
)

// Anthropic-style token counting can retry provider failures while rebuilding
// its payload for each selected target. The loop performs no request-slot
// acquisition or completion submission, leaving one-count execution and its
// resources to the adapter.
//
// Failover state maintains exclusions, delays and switch allowance after a
// classified error. Every failed Forward releases the captured session before
// another selection or terminal cancellation/exhaustion handling; an ordinary
// unclassified error is observed before that release. Preparation rejection and
// successful forwarding return through their adapter-owned paths. Selection
// failure receives the preceding classified failure so the HTTP layer can
// distinguish initial unavailability from exhausted counting attempts.

// CountPorts supplies slot-free counting selection, fresh payload preparation,
// execution and failed-session release. Its adapter owns request output and
// upstream resources; RunCountTokens owns failover accounting and calls cleanup
// on failed forwarding.
type CountPorts interface {
	// Context returns the current nonnil counting request context for failover and
	// waiting. Selection and forwarding retain their own cancellation handling,
	// while the loop uses this context after classified failures.
	Context() context.Context
	// Select captures a counting target using excluded provider identifiers and
	// returns its candidate metadata. It acquires no request slot; failed
	// selection is reported with the last classified attempt evidence.
	Select(map[int64]struct{}) (Selection, error)
	// SelectionFailed handles a counting selection error and any preceding
	// classified failure. It owns final output for initial or exhausted selection,
	// and the loop ends without forwarding another target.
	SelectionFailed(error, *AttemptFailure)
	// Prepare rebuilds the count payload for the selected target and reports
	// whether forwarding can proceed. False ends this sequence; the adapter owns
	// any parse/admission error response and preparation cleanup.
	Prepare(Selection) bool
	// Forward performs one count operation using the captured target and newly
	// prepared payload. Nil failure ends successfully; nonnil failure
	// distinguishes ordinary errors from policy-driven failover, with upstream
	// resources owned by the adapter.
	Forward(Selection) *AttemptFailure
	// ForwardFailed observes an ordinary count error using selected metadata
	// before ReleaseSession. The adapter owns any output already produced by
	// Forward, and the loop stops after releasing that failed session.
	ForwardFailed(Selection, error)
	// ReleaseSession releases the reservation associated with a failed counting
	// target. It runs after failed Forward and before reselection or terminal
	// handling; concrete HTTP cleanup uses an independent context when request
	// cancellation has occurred.
	ReleaseSession(Selection)
	// Exhausted handles the final classified count failure using the last selected
	// target. The session has already been released, and the adapter owns error
	// formatting for this exhausted request.
	Exhausted(Selection, *AttemptFailure)
	// Canceled handles cancellation recognized after a classified count failure.
	// Failed-session release runs first, and the loop then returns without another
	// preparation, selection or forwarding operation.
	Canceled()
	failover.TempUnscheduler[*AttemptFailure] // classified provider temporary-exclusion operations
}

// RunCountTokens serially selects and prepares token-count attempts using
// failover's configured switch allowance. Adapters supply usable operations and
// a nonnil current context, own upstream resources, and handle local request
// output.
//
// A classified failure enters failover accounting before the failed session is
// released, then continuation rebuilds payload for another selection.
// Cancellation and exhaustion invoke their terminal operations after that same
// release. Ordinary errors are observed and released directly; successful
// forwarding and preparation rejection return through adapter-owned handling.
// The loop acquires no request slot and submits no generation completion or
// funds work.
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
		// Retire the failed count session before terminal handling or reselection,
		// including canceled requests whose concrete adapter performs detached cleanup.
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
