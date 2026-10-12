package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Deadlock fixtures distinguish typed SQLSTATE detection from ordinary
// diagnostic text and exercise complete operation retries. Successful and
// exhausted retries assert attempt counts; cancellation after a deadlock and
// cancellation during waiting protect separate stopping paths for storage
// callers.

// TestIsPostgresDeadlock compares direct and wrapped deadlock errors with
// other SQLSTATEs, ordinary text, and nil. It requires classification to
// follow PostgreSQL's typed 40P01 code across wrapping.
func TestIsPostgresDeadlock(t *testing.T) {
	deadlockErr := &pq.Error{Code: "40P01"}
	tests := []struct {
		name string // case display name
		err  error  // supplied classification error
		want bool   // expected deadlock decision
	}{
		{name: "直接死锁错误", err: deadlockErr, want: true},
		{name: "包装后的死锁错误", err: fmt.Errorf("wrapped: %w", deadlockErr), want: true},
		{name: "其他 PostgreSQL 错误", err: &pq.Error{Code: "23505"}, want: false},
		{name: "普通错误", err: errors.New("deadlock detected"), want: false},
		{name: "空错误", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, IsDeadlock(tt.err))
		})
	}
}

// TestRetryPostgresDeadlock_RetriesTwiceThenSucceeds fails the first two
// operation attempts with deadlocks. It requires the third value to succeed
// and the total call count to match the permitted attempt budget.
func TestRetryPostgresDeadlock_RetriesTwiceThenSucceeds(t *testing.T) {
	attempts := 0
	result, err := RetryDeadlock(context.Background(), "test_operation", 0, func() (int, error) {
		attempts++
		if attempts < postgresDeadlockMaxAttempts {
			return 0, &pq.Error{Code: "40P01"}
		}
		return 42, nil
	})

	require.NoError(t, err)
	require.Equal(t, 42, result)
	require.Equal(t, postgresDeadlockMaxAttempts, attempts)
}

// TestRetryPostgresDeadlock_StopsAfterMaximumAttempts makes every operation
// attempt deadlock. It requires the final error to retain deadlock
// classification and fn to stop after the full configured attempt budget.
func TestRetryPostgresDeadlock_StopsAfterMaximumAttempts(t *testing.T) {
	attempts := 0
	_, err := RetryDeadlock(context.Background(), "test_operation", 0, func() (int, error) {
		attempts++
		return 0, &pq.Error{Code: "40P01"}
	})

	require.Error(t, err)
	require.True(t, IsDeadlock(err))
	require.Equal(t, postgresDeadlockMaxAttempts, attempts)
}

// TestRetryPostgresDeadlock_DoesNotRetryOtherErrors returns an ordinary write
// failure on the first attempt. It requires that same error and exactly one
// fn call, protecting the typed retry filter.
func TestRetryPostgresDeadlock_DoesNotRetryOtherErrors(t *testing.T) {
	attempts := 0
	wantErr := errors.New("write failed")
	_, err := RetryDeadlock(context.Background(), "test_operation", 0, func() (int, error) {
		attempts++
		return 0, wantErr
	})

	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 1, attempts)
}

// TestRetryPostgresDeadlock_StopsWhenContextCanceled cancels ctx inside the
// first deadlocking operation. It requires context.Canceled and one attempt,
// protecting cancellation before another retry can begin.
func TestRetryPostgresDeadlock_StopsWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	_, err := RetryDeadlock(ctx, "test_operation", 0, func() (int, error) {
		attempts++
		cancel()
		return 0, &pq.Error{Code: "40P01"}
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, attempts)
}

// TestWaitDeadlockRetryCanceled supplies a canceled context for the first
// backoff slot. It requires context.Canceled rather than waiting for the
// applicable retry delay to complete.
func TestWaitDeadlockRetryCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, WaitDeadlockRetry(ctx, 0), context.Canceled)
}
