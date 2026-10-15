package postgres

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/lib/pq"
)

// initialize_retry.go retries database initialization when PostgreSQL reports
// temporary connection failures.
// A PostgreSQL instance can still be starting when application initialization
// begins. Initialization retries connection-class SQLSTATEs and
// cannot_connect_now with capped exponential delays; the supplied initializer
// tolerates completed earlier effects, and permanent configuration or
// migration failures stop immediately.

// Initialization retries allow PostgreSQL startup failures to recover within
// the caller's budget, with eight retry pauses capped at thirty seconds.
const (
	maxDatabaseInitializationRetries = 8
	databaseInitializationRetryBase  = time.Second
	databaseInitializationRetryMax   = 30 * time.Second
)

// InitializeWithRetry invokes initialize immediately and permits eight
// retries for wrapped PostgreSQL connection-class errors or
// cannot_connect_now. Other errors return immediately; initialize owns its
// side effects and must tolerate retries after partial initialization.
// Waiting honors ctx cancellation.
func InitializeWithRetry(ctx context.Context, initialize func(context.Context) error) error {
	return initializeDatabaseWithRetryWithWait(ctx, initialize, waitForDatabaseInitializationRetry)
}

// initializeDatabaseWithRetryWithWait invokes initialization and passes each
// applicable backoff to wait. The injected wait owns delay cancellation; a
// failed wait ends retrying, and the ninth failed initialization returns its
// last error.
func initializeDatabaseWithRetryWithWait(
	ctx context.Context,
	initialize func(context.Context) error,
	wait func(context.Context, time.Duration) error,
) error {
	for attempt := 1; ; attempt++ {
		err := initialize(ctx)
		if err == nil {
			return nil
		}
		if !isTransientDatabaseInitializationError(err) || attempt > maxDatabaseInitializationRetries {
			return err
		}

		// Reinvoke the initializer only for temporary PostgreSQL startup failures.
		// Cap later pauses so the caller can retry while its overall budget permits.
		delay := min(databaseInitializationRetryBase*time.Duration(1<<(attempt-1)), databaseInitializationRetryMax)
		slog.Warn("database initialization temporarily unavailable; retrying",
			"retry", attempt,
			"max_retries", maxDatabaseInitializationRetries,
			"retry_in", delay,
			"error", err,
		)
		if err := wait(ctx, delay); err != nil {
			return err
		}
	}
}

// waitForDatabaseInitializationRetry waits for delay or returns ctx's
// cancellation error. The timer is stopped on return; simultaneous timer
// readiness and cancellation follow the selected ready case.
func waitForDatabaseInitializationRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isTransientDatabaseInitializationError recognizes wrapped pq
// connection-class codes and cannot_connect_now errors.
func isTransientDatabaseInitializationError(err error) bool {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		return false
	}
	code := string(pqErr.Code)
	return code == "57P03" || strings.HasPrefix(code, "08")
}
