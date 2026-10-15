package text

import (
	"context"
	"errors"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/gateway/admission"
	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// responses.go runs Responses attempts with separate first-output recovery,
// same-provider retry, and switching budgets. Partial native output and
// observed partial images can still reach completion, while pricing rejection
// and closed replay windows end execution.
//
// Unsupported HTTP continuation candidates are excluded without consuming the
// ordinary switch counter. Group fallback resets excluded IDs and same-provider
// counts while retaining global switch, first-output and OAuth limiting state.
// Eligible first-output failures consume their separate allowance before a
// same-provider retry decision, and the adapter decides whether actual output
// still permits replay. Completion, provider resources and HTTP responses
// remain adapter operations; optional lifecycle hooks receive the observed
// served decision after the chosen terminal path.

// ResponseSelection carries a candidate and availability,
// unsupported-continuation and OAuth-limiting evidence.
type ResponseSelection struct {
	// Available enables forwarding, while Skip excludes an otherwise captured
	// candidate before acquisition. That skip does not consume MaxSwitches; OAuth
	// describes limiting evidence used only after an ordinary failed switch.
	Selection                           // captured provider and candidate route
	Available bool                      // candidate selection is usable
	Skip      *AttemptFailure           // unsupported continuation exclusion evidence
	OAuth     failover.OAuth429Provider // candidate OAuth limiting evidence
}

// ResponseOutcome extends observed attempt evidence with partial-output and
// first-output recovery decisions.
type ResponseOutcome struct {
	// NativePartial takes the direct completion/error path; Images takes partial
	// image handling before completion. FirstOutputRecovery applies only to a
	// classified replayable failure when the additional budget is enabled.
	Outcome                  // captured forwarding result and error
	Images              bool // partial-image completion path selected
	NativePartial       bool // native partial-result completion selected
	FirstOutputRecovery bool // failure eligible for first-output allowance
}

// ResponseOptions controls ordinary switching and the additional allowance for
// first-output recovery.
type ResponseOptions struct {
	MaxSwitches       int  // ordinary provider-switch budget
	FirstOutputBudget bool // enable separate first-output failure allowance
}

// ResponsePorts supplies serial attempt, output and completion operations for
// RunResponses. Adapters own selected resources and retry-window validation;
// the loop maintains its separate counters and exclusions.
type ResponsePorts interface {
	// Context returns the current nonnil request context used for retry waiting.
	// The adapter owns cancellation and group-derived request state, and the loop
	// rereads the context for each selected delay.
	Context() context.Context
	// CanAttempt reports whether request execution can prepare another attempt.
	// False stops before selection; implementations enforce cancellation or
	// rejection conditions and own any output already produced by those
	// conditions.
	CanAttempt() bool
	// Select captures a candidate using the supplied excluded-provider set and
	// returns availability evidence. A Skip value identifies an unsupported HTTP
	// continuation candidate that the loop excludes before acquisition or ordinary
	// switch accounting.
	Select(map[int64]struct{}) (ResponseSelection, error)
	// SelectionFailure handles an error after selection using exclusion count and
	// the most recent classified failure. The adapter owns final response
	// formatting, and the loop returns after this operation.
	SelectionFailure(error, int, *AttemptFailure)
	// Acquire obtains resources for the captured candidate and reports whether
	// forwarding may proceed. False stops execution, while the adapter owns
	// release for acquired resources on its forwarding or request-exit paths.
	Acquire() bool
	// Forward executes the current captured target and returns observed result,
	// partial-output and recovery evidence. The adapter owns upstream and provider
	// resource cleanup, while the loop chooses subsequent retry or completion
	// handling.
	Forward() ResponseOutcome
	// PartialImages handles the forwarding error accompanying retained image
	// output before completion. The loop proceeds to Success and Complete
	// afterward, so the adapter keeps the partial image observations available to
	// those operations.
	PartialImages(error)
	// RetryReady reports whether a classified failure remains safe to replay for
	// this request. Implementations reject closed output windows and own final
	// failure handling on false, after which the loop returns.
	RetryReady(*AttemptFailure) bool
	// RetryWait observes a same-provider retry with its effective limit, new retry
	// count and selected delay. The loop performs the context-aware wait
	// afterward, and the adapter leaves current attempt cleanup complete before it
	// returns.
	RetryWait(*AttemptFailure, int, int, time.Duration)
	// Exhausted handles the final classified failure after retry or switch
	// allowances end. The adapter writes output appropriate to the current request
	// state, and the loop performs no completion after this path.
	Exhausted(*AttemptFailure)
	// Switched records the selected ordinary switch before its budget is checked.
	// It can therefore run for the final exhausted failure, and funds completion
	// remains separate from this provider feedback.
	Switched()
	// Switching observes an allowed next-provider transition with current and
	// maximum switch counts. It runs after budget and OAuth limiting checks,
	// before the loop selects its next candidate.
	Switching(*AttemptFailure, int, int)
	// OtherFailure handles an ordinary or native-partial forwarding error before
	// completion handling. It does not determine funds persistence; the loop can
	// call Complete and Failed after this observation.
	OtherFailure(error)
	// Failed finalizes adapter failure handling after completion capture on an
	// ordinary/native-partial error. That captured result may contain measurable
	// usage, and funds success is determined by the recorder separately.
	Failed()
	// Complete captures/submits the current observed result for completion
	// handling. It runs on success, retained partial images, and
	// ordinary/native-partial errors, with eligibility and snapshot ownership
	// defined by the adapter.
	Complete()
	// Success applies successful/retained-image provider feedback before
	// completion capture. It is separate from persistent usage and funds work, so
	// the callback itself does not prove a completed settlement.
	Success()
	// Completed finalizes successful or retained-image request handling with the
	// ordinary switch count. It runs after Complete and does not wait for
	// asynchronous funds or usage persistence to succeed.
	Completed(int)
}

// RunResponses coordinates serial attempts with same-provider, ordinary-switch
// and optional first-output recovery allowances. Adapters own request resources
// and output safety, and RetryReady ends replay when the current output window
// is closed.
//
// Unsupported continuation skips exclude candidates before ordinary accounting.
// Accepted group fallback clears local exclusions and same-provider counts
// while retaining global counters and OAuth limiting state. Native partial
// results complete through the failure path, and partial image results reach
// retained-output completion. Ordinary classified failures retry or switch only
// within their applicable allowances; success and retained images finish with
// completion and their ordinary switch count.
func RunResponses(options ResponseOptions, p ResponsePorts) {
	served := false
	if lifecycle, ok := p.(interface {
		// Begin starts optional lifecycle observation before the first attempt.
		// RunResponses calls it before CanAttempt and Select when both lifecycle
		// methods are provided, then registers Finish after Begin returns.
		Begin()
		// Finish ends optional lifecycle observation with the loop's served decision.
		// It runs on return after Begin, with native partial output or observed
		// attempt service determining that value.
		Finish(bool)
	}); ok {
		lifecycle.Begin()
		defer func() { lifecycle.Finish(served) }()
	}

	excluded := make(map[int64]struct{})
	sameProvider := make(map[int64]int)
	switches, firstOutputSwitches := 0, 0
	var last *AttemptFailure
	var oauth failover.OAuth429State
	for {
		if !p.CanAttempt() {
			return
		}
		selected, err := p.Select(excluded)
		if err != nil {
			p.SelectionFailure(err, len(excluded), last)
			return
		}
		if !selected.Available {
			return
		}
		if selected.Skip != nil {
			excluded[selected.Provider.ID] = struct{}{}
			last = selected.Skip
			continue
		}
		if !p.Acquire() {
			return
		}
		outcome := p.Forward()
		// Preparation can return a pricing rejection after writing its local client
		// error. Stop here before retry, provider-success feedback or completion capture.
		if outcome.Stop || errors.Is(outcome.Err, admission.ErrModelPricingRejected) {
			return
		}
		if outcome.Err != nil {
			if fallback, ok := p.(interface {
				// TryGroupFallback handles group recovery and returns handled/retry
				// decisions. Handled false leaves ordinary failure processing available;
				// handled true either restarts local candidate state or ends the request.
				TryGroupFallback(error) (bool, bool)
			}); ok && !outcome.NativePartial {
				handled, retry := fallback.TryGroupFallback(outcome.Err)
				if handled {
					if !retry {
						return
					}
					// The new group needs fresh candidate and same-provider state. Retain
					// global recovery counters so repeated group changes cannot renew those allowances.
					excluded = make(map[int64]struct{})
					sameProvider = make(map[int64]int)
					last = nil
					continue
				}
			}
			if outcome.NativePartial {
				served = true
				p.OtherFailure(outcome.Err)
				p.Complete()
				p.Failed()
				return
			}
			if outcome.Images {
				p.PartialImages(outcome.Err)
			} else {
				if outcome.Failure != nil {
					failure := outcome.Failure
					if !p.RetryReady(failure) {
						return
					}
					if failure.Policy == nil || !failure.Policy.RetryNext {
						p.Exhausted(failure)
						return
					}
					// Eligible first-output failures spend their separate allowance before
					// same-provider recovery, so repeated retries cannot renew that failure opportunity.
					if options.FirstOutputBudget && failover.FirstOutputExhausted(outcome.FirstOutputRecovery, &firstOutputSwitches) {
						p.Exhausted(failure)
						return
					}
					retryLimit := failover.EffectiveSameProviderRetryLimit(failure.Policy, selected.RetryLimit)
					if failure.Policy.RetryableOnSameProvider && failover.SameProviderRetryAllowed(failure.Policy, sameProvider[selected.Provider.ID], retryLimit) {
						if observer, ok := p.(interface {
							// PrepareRetry observes classified recovery before waiting or switching,
							// with true selecting same-provider recovery. OpenAI adapters use this
							// opportunity to carry forced-cache billing and derived request context
							// into their next attempt.
							PrepareRetry(*AttemptFailure, bool)
						}); ok {
							observer.PrepareRetry(failure, true)
						}
						sameProvider[selected.Provider.ID]++
						delay := failover.SameProviderRetryDelayFor(failure.Policy, sameProvider[selected.Provider.ID])
						p.RetryWait(failure, retryLimit, sameProvider[selected.Provider.ID], delay)
						// Attempt cleanup belongs to the adapter before this delay. If the wait
						// observes cancellation, return before another selection can start this recovery.
						if !failover.SleepWithContext(p.Context(), delay) {
							return
						}
						continue
					}
					if observer, ok := p.(interface {
						// PrepareRetry observes classified recovery before waiting or switching,
						// with true selecting same-provider recovery. OpenAI adapters use this
						// opportunity to carry forced-cache billing and derived request context
						// into their next attempt.
						PrepareRetry(*AttemptFailure, bool)
					}); ok {
						observer.PrepareRetry(failure, false)
					}
					p.Switched()
					excluded[selected.Provider.ID] = struct{}{}
					last = failure
					if switches >= options.MaxSwitches {
						p.Exhausted(failure)
						return
					}
					switches++
					if failover.StopOAuth429(selected.OAuth, failure.Policy.StatusCode, switches, &oauth) {
						p.Exhausted(failure)
						return
					}
					p.Switching(failure, switches, options.MaxSwitches)
					continue
				}
				p.OtherFailure(outcome.Err)
				p.Complete()
				p.Failed()
				return
			}
		}
		served = outcome.Attempt.Served
		p.Success()
		p.Complete()
		p.Completed(switches)
		return
	}
}

// ResponsesCapability requires the Responses endpoint for image intent on the
// OpenAI platform. Other combinations select text-generation capability,
// leaving platform/provider eligibility checks with the selection adapter.
func ResponsesCapability(imageIntent bool, platform string) providercore.OpenAIEndpointCapability {
	if imageIntent && platform == capability.PlatformOpenAI {
		return providercore.OpenAIEndpointCapabilityResponses
	}
	return providercore.OpenAIEndpointCapabilityTextGeneration
}

// RequiredResponsesCapability prioritizes OpenAI native compaction, then legacy
// Compact, then ordinary image/text requirements. Other platforms use the
// ordinary capability decision; these endpoint requirements guide selection
// rather than establish execution support by themselves.
func RequiredResponsesCapability(imageIntent bool, nativeCompactionV2 bool, legacyCompact bool, platform string) providercore.OpenAIEndpointCapability {
	if nativeCompactionV2 && platform == capability.PlatformOpenAI {
		return providercore.OpenAIEndpointCapabilityRemoteCompactionV2
	}
	if legacyCompact && platform == capability.PlatformOpenAI {
		return providercore.OpenAIEndpointCapabilityResponses
	}
	return ResponsesCapability(imageIntent, platform)
}
