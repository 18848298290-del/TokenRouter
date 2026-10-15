package systemone

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// execute.go runs SystemOne provider attempts and routes served results to
// completion or missing-usage observation. Served results finish the attempt:
// valid usage reaches completion, while missing usage reaches its observation
// callback, and neither result starts another provider inference.
//
// Unserved failures can retry on the held provider according to failover
// policy, then switch within an independent outer budget if response commitment
// still permits it. Reporting and completion run before the acquired provider
// release callback. Same-provider backoff is cancellable, and the outer loop
// checks cancellation before selection and switching. Acquisition refusal
// returns the current context error; the HTTP adapter can already have
// presented a local slot error while that context remains live.

// ErrNoProvider reports that selection returned no usable SystemOne provider
// for the request.
var ErrNoProvider = errors.New("no available SystemOne provider")

// Outcome holds an exchange result and the retry state allowed by the gateway.
type Outcome struct {
	Result     upstream.AttemptResult // observed selected attempt result
	Err        error                  // exchange or output failure
	Failure    *failover.FailureInfo  // optional classified recovery decision
	RetryLimit int                    // ordinary same-provider retry allowance
}

// Ports supplies provider selection, slot acquisition, exchange, completion,
// and health feedback for a request.
type Ports interface {
	// Select returns an eligible provider outside excluded and prepares
	// request-local exchange state. A zero provider identifier means unavailable;
	// selection errors return directly unless a prior failure supplies the final
	// cause.
	Select(context.Context, map[int64]struct{}) (provider.ProviderSnapshot, error)
	// Acquire obtains the selected provider slot and its release callback. A
	// false result ends Run with ctx.Err, which can be nil when an adapter has
	// already handled the local refusal response.
	Acquire(context.Context) (func(), bool)
	// Forward exchanges once on the currently acquired provider and returns
	// observed output, usage, and failure decisions. The loop can call it
	// repeatedly within that lease when same-provider retries remain eligible.
	Forward(context.Context) Outcome
	// Report observes every exchange outcome before usage completion or retry
	// handling. It runs synchronously while the provider slot remains held,
	// allowing health feedback to use the current attempt state.
	Report(context.Context, Outcome)
	// Complete captures served results with observed usage before the provider
	// slot is released. The loop calls it even when output reports an error,
	// retaining usage that already represents served work.
	Complete(context.Context, upstream.AttemptResult)
	// MissingUsage observes a served result whose usage is unavailable before
	// releasing the provider slot. Such an answer ends recovery, and the callback
	// records the missing-usage outcome separately from billing completion.
	MissingUsage(context.Context, upstream.AttemptResult)
	// Switch synchronously records an outer provider change after exclusion is
	// added.
	Switch()
}

// Run executes SystemOne with same-provider retries inside a separate
// provider-switch budget. Negative maxSwitches permits the initial provider
// alone, and a nil release callback is accepted for a successful acquisition.
//
// Each held slot remains active through reporting and served-result completion
// before release. A later selection failure returns the prior eligible
// upstream error when one exists. Output commitment or served work ends replay
// even when the error is otherwise retryable. Acquire refusal returns ctx.Err
// directly, including nil when that context remains live. The HTTP slot
// adapter can already have written a local error, so nil alone does not prove
// that an upstream exchange occurred.
// @project-doc docs/interfaces/jev_upstream.md#systemone_usage
func Run(ctx context.Context, maxSwitches int, ports Ports) error {
	excluded := make(map[int64]struct{})
	var last error
	for attempt := 0; attempt <= max(0, maxSwitches); attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		selected, err := ports.Select(ctx, excluded)
		if err != nil {
			if last != nil {
				return last
			}
			return err
		}
		if selected.ID == 0 {
			if last != nil {
				return last
			}
			return ErrNoProvider
		}
		release, ok := ports.Acquire(ctx)
		if !ok {
			return ctx.Err()
		}
		// Keep one acquired slot through every same-provider retry and completion
		// callback, then release before the outer loop can select another provider
		// for recovery.
		outcome := func() Outcome {
			if release != nil {
				defer release()
			}
			return runSelected(ctx, ports)
		}()
		if !outcome.canRetry() {
			return outcome.Err
		}
		last = outcome.Err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == max(0, maxSwitches) {
			break
		}
		// Exclude the failed provider only after its inner retry opportunity
		// finishes; the separate outer switch counter bounds how often this
		// selection transition can occur.
		excluded[selected.ID] = struct{}{}
		ports.Switch()
	}
	return last
}

// runSelected reports each exchange and retries eligible unserved work within
// one provider lease. The caller owns that lease until this function returns;
// served usage reaches Complete, while served missing usage reaches
// MissingUsage.
//
// An error can replay only before served work and both commitment flags.
// SameProviderRetryAllowed then combines this outcome's count or deadline
// policy with its provider limit. Waiting uses the failure's selected delay
// and context cancellation; interruption returns the context error alone and
// prevents another Forward call on that lease.
func runSelected(ctx context.Context, ports Ports) Outcome {
	for retries := 0; ; retries++ {
		outcome := ports.Forward(ctx)
		ports.Report(ctx, outcome)
		// Retain completion for served work even when its exchange reports an output
		// error; missing usage is observed separately and cannot authorize a normal
		// settlement.
		if outcome.Result.Served {
			if outcome.Result.HasUsage {
				ports.Complete(ctx, outcome.Result)
			} else {
				ports.MissingUsage(ctx, outcome.Result)
			}
		}
		if !outcome.canRetry() || !failover.SameProviderRetryAllowed(outcome.Failure, retries, outcome.RetryLimit) {
			return outcome
		}
		// Wait under the same provider slot after retry admission, with cancellation
		// preventing another inference call even when the ordinary retry count would
		// still permit it.
		delay := failover.SameProviderRetryDelayFor(outcome.Failure, retries+1)
		if !failover.SleepWithContext(ctx, delay) {
			return Outcome{Err: ctx.Err()}
		}
	}
}

// canRetry reports whether this failed outcome permits replay before service
// or output commitment. It requires RetryNext and a nonnil classified failure;
// served, HTTP-committed, and retry-committed outcomes refuse recovery.
func (o Outcome) canRetry() bool {
	return o.Err != nil && o.Failure != nil && o.Failure.RetryNext && !o.Result.Served && !o.Result.HTTPCommitted && !o.Result.RetryCommitted
}
