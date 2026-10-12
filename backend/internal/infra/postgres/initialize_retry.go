package postgres

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/lib/pq"
)

const (
	maxDatabaseInitializationRetries = 8
	databaseInitializationRetryBase  = time.Second
	databaseInitializationRetryMax   = 30 * time.Second
)

// InitializeWithRetry retries PostgreSQL connection-class errors and cannot_connect_now
// with capped backoff. Other errors return immediately; initialize must tolerate retries.
func InitializeWithRetry(ctx context.Context, initialize func(context.Context) error) error {
	return initializeDatabaseWithRetryWithWait(ctx, initialize, waitForDatabaseInitializationRetry)
}

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

func isTransientDatabaseInitializationError(err error) bool {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		return false
	}
	code := string(pqErr.Code)
	return code == "57P03" || strings.HasPrefix(code, "08")
}
