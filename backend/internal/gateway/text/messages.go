package text

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// Messages execution may change providers while keeping one request's output
// and completion decisions coherent. A group fallback restarts candidate state,
// while changed output ends replay and partial-result completion follows the
// entry-specific option.
//
// The loop invokes adapter operations in preparation, selection, interception,
// acquisition and forwarding order. It delegates provider-resource ownership
// and HTTP error formatting to those operations, and failover state owns
// same-provider delays, temporary exclusions and switch accounting. Stop and
// skipped outcomes take precedence over ordinary error handling; policy refusal
// ends immediately, and prompt-too-long recovery relies on the adapter honoring
// the passed fallback-used flag. Finish receives the served decision after the
// selected completion path has run.

// Failure kinds select local refusal, prompt-size recovery, or ordinary error
// handling after an adapter returns a failed forwarding outcome.
const (
	FailureOther         FailureKind = iota // ordinary forwarding error handling
	FailurePolicy                           // local or upstream policy refusal
	FailurePromptTooLong                    // prompt-size group fallback opportunity
)

// AttemptFailure carries a forwarding cause and the adapter's optional retry
// policy. Failover reads Policy, while Error and Unwrap expose Cause; callers
// supply a nonnil cause before formatting this error.
type AttemptFailure struct {
	Cause  error                 // underlying forwarding failure cause
	Policy *failover.FailureInfo // optional parsed retry policy
}

// FailureKind chooses the Messages loop's post-forwarding error branch.
type FailureKind uint8

// Selection captures one chosen provider, optional route plan, and
// same-provider retry limit.
type Selection struct {
	// Plan and PlanProvided are captured together. PlanProvided tells consumers
	// whether the adapter supplied an admitted candidate; the loop retains the
	// value even when that flag is false.
	Plan         routing.CandidatePlan     // selected candidate route plan
	PlanProvided bool                      // candidate plan presence flag
	Provider     provider.ProviderSnapshot // selected provider snapshot value
	RetryLimit   int                       // same-provider retry allowance
}

// Outcome combines one forwarding result with terminal, skip, error, and replay
// evidence for Messages execution. Stop and Skip are checked first, while
// partial completion uses HasResult after an ordinary error.
type Outcome struct {
	// Err, Failure, and Kind describe different error decisions. Stop or Skip
	// takes precedence; OutputChanged prevents classified failure replay, while
	// HasResult determines Finish's served value on enabled ordinary partial completion.
	Attempt       upstream.AttemptResult // observed upstream attempt result
	Err           error                  // forwarding error for branch selection
	Failure       *AttemptFailure        // classified retry failure evidence
	Kind          FailureKind            // post-forwarding error branch kind
	OutputChanged bool                   // actual output closed replay window
	HasResult     bool                   // partial result presence for Finish
	Stop          bool                   // stop before outcome accounting
	Skip          bool                   // exclude candidate and select again
}

// AttemptState passes current switch accounting and cache-billing policy into
// forwarding and completion.
type AttemptState struct {
	SwitchCount       int  // current provider-switch count
	ForceCacheBilling bool // current forced-cache billing choice
}

// MessageOptions captures entry-specific retry, cancellation, and
// partial-completion decisions for the Messages loop.
type MessageOptions struct {
	// MaxSwitches applies to each group's fresh failover state. Zero or negative
	// values still allow initial execution but stop ordinary switching after its
	// failed attempt.
	MaxSwitches int // ordinary provider-switch budget
	// CompletePartialFailure invokes Complete on ordinary forwarding errors and
	// uses HasResult for Finish's served decision. Classified retry failures and
	// policy refusals use their earlier terminal paths.
	CompletePartialFailure bool // complete ordinary partial-error results
	// StopOnCanceledContext checks the current context before preparation and
	// stops silently when canceled. Disabled checking leaves cancellation
	// recognition with the selected adapter and failover operations.
	StopOnCanceledContext bool // check cancellation before each preparation
	// HasBoundSession seeds each new failover state with captured session
	// affinity. Group restart reuses this option, so callers choose the evidence
	// appropriate to their entry policy.
	HasBoundSession bool             // initial bound-session failover evidence
	Observe         failover.Observe // optional failover observation callback
}

// MessagePorts supplies request operations called serially by RunMessages.
// Adapters own selected targets, request output and acquired resources, and
// Fallback honors the passed used flag when deciding whether to restart group
// execution.
type MessagePorts interface {
	// Context returns the current nonnil request context for cancellation and
	// retry budgets. A group fallback can update request state, so the loop reads
	// this operation again rather than freezing an earlier context.
	Context() context.Context
	// Begin starts adapter request-lifecycle observation before preparation or
	// selection. RunMessages registers Finish first and invokes Begin once,
	// keeping the final served decision available to that deferred lifecycle
	// operation.
	Begin()
	// Finish closes adapter request-lifecycle handling with the loop's served
	// decision. It runs once on return, after success completion or the configured
	// partial-error completion path has set that decision.
	Finish(bool)
	// PrepareAttempt rebuilds request-local input for the next selection and
	// reports whether execution can continue. False stops the loop, leaving the
	// adapter responsible for any specific rejection output or cleanup.
	PrepareAttempt() bool
	// Select chooses a candidate using the supplied excluded-provider set and
	// captures its target in the adapter. The loop reads that set across retries;
	// selection errors enter initial-failure or exhaustion handling according to
	// prior exclusions.
	Select(map[int64]struct{}) (Selection, error)
	// FirstSelectionFailure handles selection failure for the current group before
	// any excluded provider is present. It receives the group-fallback-used flag
	// and owns the client response for that request-stage failure.
	FirstSelectionFailure(error, bool)
	// SingleProviderRetry observes that failover reopened a single-provider
	// selection opportunity. The loop prepares and selects again afterward, while
	// delay and budget decisions remain in failover state.
	SingleProviderRetry()
	// Canceled handles cancellation observed by failover state during retry or
	// selection recovery. The adapter owns any final output and resource handling,
	// and the loop returns immediately after this operation.
	Canceled()
	// Exhausted writes the final classified failure using optional platform and
	// changed-output evidence. A true final bool requires output-safe handling
	// because the loop has closed the replay opportunity.
	Exhausted(*AttemptFailure, string, bool)
	// Intercept reports whether entry-specific local handling ended the request
	// after selection. True stops before provider acquisition or forwarding,
	// leaving the intercepted response with the adapter.
	Intercept() bool
	// Acquire obtains resources for the captured selected target and reports
	// whether forwarding can proceed. False stops execution; the adapter owns
	// acquired-resource release on its subsequent forwarding and exit paths.
	Acquire() bool
	// Forward executes one captured provider attempt using switch and
	// cache-billing evidence. It returns observed output and failure decisions,
	// while the adapter retains responsibility for the attempt's acquired
	// resources.
	Forward(AttemptState) Outcome
	// PolicyFailure handles a classified policy refusal before retry or ordinary
	// error handling. It owns the refusal response, and the Messages loop ends
	// after the operation returns.
	PolicyFailure(error)
	// Fallback attempts prompt-size group recovery using the supplied already-used
	// flag. True permits a fresh candidate loop; implementations reject repeated
	// or unsafe recovery and write the appropriate response when declining it.
	Fallback(error, bool) bool
	// OtherFailure handles an ordinary forwarding error after classified failover
	// paths are finished. The configured partial-completion option can still
	// invoke Complete afterward, so this operation does not establish completion
	// eligibility by itself.
	OtherFailure(error)
	// Complete captures/submits completion for the current result using attempt
	// accounting. RunMessages calls it on success or enabled ordinary partial
	// error, and completion eligibility and snapshot ownership belong to the
	// adapter.
	Complete(AttemptState)
	// Success records successful forwarding before completion is captured for this
	// request. Its provider feedback is separate from funds persistence, which
	// runs through the completion adapter and recorder.
	Success()
	// Switched observes an allowed failover continuation before abandoning the
	// preceding provider. The loop then prepares another attempt using the
	// exclusion and accounting state selected by failover.
	Switched()
	// Abandon releases or clears adapter state associated with the preceding
	// provider ID. It follows an allowed continuation and precedes another
	// attempt, so implementations retire resources before new target preparation.
	Abandon(int64)
	// TempUnscheduleRetryableError applies failover's temporary provider exclusion
	// using the classified failure. The supplied request context bounds that
	// effect, while retry and switch accounting stay with failover state.
	TempUnscheduleRetryableError(context.Context, int64, *AttemptFailure)
}

// Error returns Cause's error text for a classified attempt failure. Callers
// provide a nonnil receiver and Cause before formatting; this method performs
// no nil recovery or message rewriting.
func (e *AttemptFailure) Error() string { return e.Cause.Error() }

// Unwrap exposes the underlying forwarding Cause to error-chain inspection.
// Callers provide a nonnil receiver, while a nil Cause is returned unchanged
// for the chain walker.
func (e *AttemptFailure) Unwrap() error { return e.Cause }

// RetryFailure returns the adapter's retry policy for failover decisions. Nil
// receivers and absent policy return nil, distinguishing an ordinary forwarding
// error from a classified recovery opportunity.
func (e *AttemptFailure) RetryFailure() *failover.FailureInfo {
	if e == nil {
		return nil
	}
	return e.Policy
}

// RunMessages serially coordinates provider attempts, group recovery and
// entry-specific partial completion. Callers supply usable adapter operations
// and resources; Finish receives the final served decision after the loop
// returns.
//
// Each new group loop creates fresh failover state, while the adapter honors
// fallbackUsed to enforce its one-use recovery rule. Changed output stops
// classified replay, and skipped candidates are excluded before error or
// completion handling. A successful attempt calls Success then Complete and
// reports served true; an ordinary error invokes Complete only when configured,
// using HasResult for served. Policy refusal, exhausted failover, cancellation,
// stop, and rejected preparation/acquisition end through their adapter paths.
func RunMessages(options MessageOptions, p MessagePorts) {
	served := false
	defer func() { p.Finish(served) }()
	p.Begin()
	fallbackUsed := false
	for {
		state := failover.NewFailoverState[*AttemptFailure](options.MaxSwitches, options.HasBoundSession, options.Observe)
		retryWithFallback := false
		for {
			if options.StopOnCanceledContext && p.Context().Err() != nil {
				return
			}
			if !p.PrepareAttempt() {
				return
			}
			selected, err := p.Select(state.FailedProviderIDs)
			if err != nil {
				if len(state.FailedProviderIDs) == 0 {
					p.FirstSelectionFailure(err, fallbackUsed)
					return
				}
				// Failover can reopen excluded candidates after a temporary 503. Give
				// that bounded recovery its decision before formatting final selection exhaustion.
				switch state.HandleSelectionExhausted(p.Context()) {
				case failover.FailoverContinue:
					p.SingleProviderRetry()
					continue
				case failover.FailoverCanceled:
					p.Canceled()
					return
				default:
					p.Exhausted(state.LastFailoverErr, "", false)
					return
				}
			}
			if p.Intercept() {
				return
			}
			if !p.Acquire() {
				return
			}
			attempt := AttemptState{SwitchCount: state.SwitchCount, ForceCacheBilling: state.ForceCacheBilling}
			outcome := p.Forward(attempt)
			if outcome.Stop {
				return
			}
			// A skipped candidate has no ordinary failed attempt to account. Exclude
			// it before selecting again, leaving the switch budget with failover decisions.
			if outcome.Skip {
				state.FailedProviderIDs[selected.Provider.ID] = struct{}{}
				continue
			}
			if outcome.Err != nil {
				switch outcome.Kind {
				case FailurePolicy:
					p.PolicyFailure(outcome.Err)
					return
				case FailurePromptTooLong:
					if p.Fallback(outcome.Err, fallbackUsed) {
						fallbackUsed = true
						retryWithFallback = true
					}
					// An accepted group recovery restarts preparation with fresh candidate
					// exclusions and retry state; the adapter has already replaced the admitted group.
					if retryWithFallback {
						break
					}
					return
				}
				if retryWithFallback {
					break
				}
				if outcome.Failure != nil {
					// This failure followed actual output. Finalize it through the stream-safe
					// adapter path rather than replaying request content to another provider.
					if outcome.OutputChanged {
						p.Exhausted(outcome.Failure, selected.Provider.Platform, true)
						return
					}
					switch state.HandleFailoverError(p.Context(), p, selected.Provider.ID, selected.Provider.Platform, selected.RetryLimit, outcome.Failure) {
					case failover.FailoverContinue:
						// Retire the preceding provider state before preparation selects another
						// target, so acquired attempt resources stay owned by their adapter.
						p.Switched()
						p.Abandon(selected.Provider.ID)
						continue
					case failover.FailoverExhausted:
						p.Exhausted(state.LastFailoverErr, selected.Provider.Platform, false)
						return
					case failover.FailoverCanceled:
						p.Canceled()
						return
					}
				}
				p.OtherFailure(outcome.Err)
				if options.CompletePartialFailure {
					p.Complete(attempt)
					served = outcome.HasResult
				}
				return
			}
			p.Success()
			p.Complete(attempt)
			served = true
			return
		}
		if !retryWithFallback {
			return
		}
	}
}
