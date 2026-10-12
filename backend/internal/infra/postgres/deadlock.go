package postgres

import (
	"context"
	"math/rand/v2"
	"time"

	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
)

const (
	postgresDeadlockMaxRetries  = 2
	postgresDeadlockMaxAttempts = postgresDeadlockMaxRetries + 1
	postgresDeadlockRetryJitter = 30 * time.Millisecond

	// DeadlockMaxAttempts includes the initial attempt and all permitted retries.
	DeadlockMaxAttempts = postgresDeadlockMaxAttempts
)

var postgresDeadlockRetryBaseDelays = [...]time.Duration{
	20 * time.Millisecond,
	50 * time.Millisecond,
}

// RetryDeadlock retries a complete operation after PostgreSQL deadlocks.
// Each fn call must use a fresh transaction or statement.
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
		if waitErr := WaitDeadlockRetry(ctx, attempt-1); waitErr != nil {
			return zero, waitErr
		}
	}
}

// WaitDeadlockRetry waits for the indexed backoff with jitter and honors cancellation.
// An out-of-range retryIndex returns immediately.
func WaitDeadlockRetry(ctx context.Context, retryIndex int) error {
	if retryIndex < 0 || retryIndex >= len(postgresDeadlockRetryBaseDelays) {
		return nil
	}
	delay := postgresDeadlockRetryBaseDelays[retryIndex]
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
