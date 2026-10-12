package systemone

import (
	"context"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/gateway/failover"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// ErrNoProvider indicates that protocol and model filtering found no available provider.
var ErrNoProvider = errors.New("no available SystemOne provider")

// Outcome holds an exchange result and the retry state allowed by the gateway.
type Outcome struct {
	Result     upstream.AttemptResult
	Err        error
	Failure    *failover.FailureInfo
	RetryLimit int
}

// Ports supplies provider selection, slot acquisition, exchange, completion, and health feedback for a request.
type Ports interface {
	Select(context.Context, map[int64]struct{}) (provider.ProviderSnapshot, error)
	Acquire(context.Context) (func(), bool)
	Forward(context.Context) Outcome
	Report(context.Context, Outcome)
	Complete(context.Context, upstream.AttemptResult)
	MissingUsage(context.Context, upstream.AttemptResult)
	Switch()
}

// Run switches providers within the retry budget before output is committed.
// It captures completion for served answers before releasing the provider slot.
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
		excluded[selected.ID] = struct{}{}
		ports.Switch()
	}
	return last
}

// runSelected retries within one provider lease, with cancellable retry waits.
func runSelected(ctx context.Context, ports Ports) Outcome {
	for retries := 0; ; retries++ {
		outcome := ports.Forward(ctx)
		ports.Report(ctx, outcome)
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
		delay := failover.SameProviderRetryDelayFor(outcome.Failure, retries+1)
		if !failover.SleepWithContext(ctx, delay) {
			return Outcome{Err: ctx.Err()}
		}
	}
}

// canRetry requires a retryable failure before serving or committing the response.
func (o Outcome) canRetry() bool {
	return o.Err != nil && o.Failure != nil && o.Failure.RetryNext && !o.Result.Served && !o.Result.HTTPCommitted && !o.Result.RetryCommitted
}
