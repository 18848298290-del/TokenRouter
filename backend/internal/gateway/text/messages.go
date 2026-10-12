package text

import (
	"context"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

const (
	FailureOther FailureKind = iota
	FailurePolicy
	FailurePromptTooLong
)

// AttemptFailure carries the cause and retry policy parsed by the platform adapter.
type AttemptFailure struct {
	Cause  error
	Policy *failover.FailureInfo
}

type FailureKind uint8

// Selection holds the provider, route plan, and retry limit for an attempt.
type Selection struct {
	Plan         routing.CandidatePlan
	PlanProvided bool
	Provider     provider.ProviderSnapshot
	RetryLimit   int
}
type Outcome struct {
	Attempt       upstream.AttemptResult
	Err           error
	Failure       *AttemptFailure
	Kind          FailureKind
	OutputChanged bool
	HasResult     bool
	Stop          bool
	Skip          bool
}

type AttemptState struct {
	SwitchCount       int
	ForceCacheBilling bool
}
type MessageOptions struct {
	MaxSwitches            int
	CompletePartialFailure bool
	StopOnCanceledContext  bool
	HasBoundSession        bool
	Observe                failover.Observe
}

// MessagePorts supplies individual operations; implementations must leave provider switching to RunMessages.
// Each adapter owns its request output and target, using dependencies bound at HTTP entry construction.
type MessagePorts interface {
	Context() context.Context
	Begin()
	Finish(bool)
	PrepareAttempt() bool
	Select(map[int64]struct{}) (Selection, error)
	FirstSelectionFailure(error, bool)
	SingleProviderRetry()
	Canceled()
	Exhausted(*AttemptFailure, string, bool)
	Intercept() bool
	Acquire() bool
	Forward(AttemptState) Outcome
	PolicyFailure(error)
	Fallback(error, bool) bool
	OtherFailure(error)
	Complete(AttemptState)
	Success()
	Switched()
	Abandon(int64)
	TempUnscheduleRetryableError(context.Context, int64, *AttemptFailure)
}

func (e *AttemptFailure) Error() string { return e.Cause.Error() }
func (e *AttemptFailure) Unwrap() error { return e.Cause }
func (e *AttemptFailure) RetryFailure() *failover.FailureInfo {
	if e == nil {
		return nil
	}
	return e.Policy
}

// RunMessages runs provider attempts and passes fallbackUsed to Fallback to enforce a single group fallback.
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
					// Restart after an accepted fallback; the HTTP adapter writes rejected fallback errors.
					if retryWithFallback {
						break
					}
					return
				}
				if retryWithFallback {
					break
				}
				if outcome.Failure != nil {
					if outcome.OutputChanged {
						p.Exhausted(outcome.Failure, selected.Provider.Platform, true)
						return
					}
					switch state.HandleFailoverError(p.Context(), p, selected.Provider.ID, selected.Provider.Platform, selected.RetryLimit, outcome.Failure) {
					case failover.FailoverContinue:
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
