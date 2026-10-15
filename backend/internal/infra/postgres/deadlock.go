package postgres

import (
	"context"
	"math/rand/v2"
	"time"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

// deadlock.go retries operations that fail with PostgreSQL deadlock errors.
// A PostgreSQL deadlock aborts the attempted statement or transaction, so
// retrying requires a complete fresh operation. The helper permits two
// retries with bounded jitter, reports retry activity, and stops a failed
// deadlock attempt when its context is canceled.

// Deadlock retry limits bound complete operation attempts and add short random
// jitter to the two base delays so competing transactions can retry at different times.
const (
	postgresDeadlockMaxRetries  = 2
	postgresDeadlockMaxAttempts = postgresDeadlockMaxRetries + 1
	postgresDeadlockRetryJitter = 30 * time.Millisecond

	// DeadlockMaxAttempts includes the initial attempt and all permitted retries.
	DeadlockMaxAttempts = postgresDeadlockMaxAttempts
)

// postgresDeadlockRetryBaseDelays provides each permitted retry's base pause,
// before jitter is added and the caller's cancellation budget is applied.
var postgresDeadlockRetryBaseDelays = [...]time.Duration{
	20 * time.Millisecond,
	50 * time.Millisecond,
}

// RetryDeadlock calls fn immediately and retries PostgreSQL SQLSTATE 40P01 at
// most twice. Each call must start a fresh transaction or statement because
// the failed database operation has been aborted; operation and batchSize
// label retry logs. Cancellation is checked after a deadlock and during
// backoff, while fn owns cancellation during its own execution.
func RetryDeadlock[T any](ctx context.Context, operation string, batchSize int, fn func() (T, error)) (T, error) {
	var zero T
	startedAt := time.Now()

	for attempt := 1; ; attempt++ {
		result, err := fn()
		if err == nil {
			if attempt > 1 {
				logger.LegacyPrintf(
					"repository.postgres_retry",
					"operation=%s sqlstate=40P01 attempt=%d batch_size=%d retry_succeeded=true fallback_failed=false elapsed_ms=%d",
					operation,
					attempt,
					batchSize,
					time.Since(startedAt).Milliseconds(),
				)
			}
			return result, nil
		}

		if !IsDeadlock(err) {
			return zero, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, ctxErr
		}

		retryAvailable := attempt <= postgresDeadlockMaxRetries
		logger.LegacyPrintf(
			"repository.postgres_retry",
			"operation=%s sqlstate=40P01 attempt=%d batch_size=%d retry_succeeded=false fallback_failed=false elapsed_ms=%d retry_scheduled=%t",
			operation,
			attempt,
			batchSize,
			time.Since(startedAt).Milliseconds(),
			retryAvailable,
		)
		if !retryAvailable {
			return zero, err
		}
		// The failed transaction is aborted. Pause before fn creates its next
		// complete operation, allowing conflicting work to advance and cancellation to stop retrying.
		if waitErr := WaitDeadlockRetry(ctx, attempt-1); waitErr != nil {
			return zero, waitErr
		}
	}
}

// WaitDeadlockRetry waits for the indexed retry delay plus bounded random
// jitter, returning the context error when canceled. An out-of-range index
// returns immediately; callers supply a non-nil context for an applicable
// delay.
func WaitDeadlockRetry(ctx context.Context, retryIndex int) error {
	if retryIndex < 0 || retryIndex >= len(postgresDeadlockRetryBaseDelays) {
		return nil
	}
	delay := postgresDeadlockRetryBaseDelays[retryIndex]
	// Add up to thirty milliseconds so callers contending on the same rows
	// need not resume all permitted retries at identical base-delay intervals.
	if postgresDeadlockRetryJitter > 0 {
		delay += time.Duration(rand.Int64N(int64(postgresDeadlockRetryJitter) + 1))
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
