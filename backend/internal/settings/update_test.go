package settings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// TestUpdatesAtomicCommitAndApplyFailure contrasts a failed write with a completed
// write followed by an application error. Failed persistence applies nothing;
// completed persistence retains both values, attempts later callbacks, and reports
// failed-module metadata while rejecting another commit.
func TestUpdatesAtomicCommitAndApplyFailure(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply_failure", true: "write_failure"}[failWrite], func(t *testing.T) {
			repo := &writeStoreStub{}
			if failWrite {
				repo.err = errors.New("write failed")
			}
			update, err := New(repo).Updates().Begin(context.Background())
			require.NoError(t, err)
			defer update.Close()
			var applied []string
			err = update.Commit(PreparedChange{Module: "identity", Values: map[string]string{"a": "1"}, Apply: func(context.Context) error { applied = append(applied, "identity"); return errors.New("apply failed") }}, PreparedChange{Module: "payment", Values: map[string]string{"b": "2"}, Apply: func(context.Context) error { applied = append(applied, "payment"); return nil }})
			if failWrite {
				require.ErrorIs(t, err, repo.err)
				require.Empty(t, applied)
				require.Empty(t, repo.values)
				return
			}
			require.Equal(t, "SETTINGS_APPLY_FAILED", apperror.Reason(err))
			require.Equal(t, map[string]string{"persisted": "true", "modules": "identity"}, apperror.FromError(err).Metadata)
			require.Equal(t, []string{"identity", "payment"}, applied)
			require.Equal(t, map[string]string{"a": "1", "b": "2"}, repo.values)
			require.Error(t, update.Commit())
		})
	}
}

// TestUpdatesRejectDuplicateOwnershipBeforeWriting submits the same storage key
// from two prepared modules. Commit returns an error before the fixture retains
// any values, protecting batch ownership admission ahead of persistence.
func TestUpdatesRejectDuplicateOwnershipBeforeWriting(t *testing.T) {
	repo := &writeStoreStub{}
	update, err := New(repo).Updates().Begin(context.Background())
	require.NoError(t, err)
	defer update.Close()
	err = update.Commit(PreparedChange{Module: "a", Values: map[string]string{"k": "1"}}, PreparedChange{Module: "b", Values: map[string]string{"k": "2"}})
	require.Error(t, err)
	require.Empty(t, repo.values)
}

// TestUpdatesStopCancelsOwnersAndWaiters holds one session while requesting another
// and stopping with a short budget. The owner context is cancelled, the other
// request errors, and closing the owner permits a later Stop to succeed.
func TestUpdatesStopCancelsOwnersAndWaiters(t *testing.T) {
	updates := New(&writeStoreStub{}).Updates()
	first, err := updates.Begin(context.Background())
	require.NoError(t, err)
	waiter := make(chan error, 1)
	// Request another session while the first retains admission. Whether it
	// registers before sealing or arrives afterward, it must return an error during shutdown.
	go func() {
		s, e := updates.Begin(context.Background())
		if s != nil {
			s.Close()
		}
		waiter <- e
	}()
	// Bound Stop while deliberately keeping the owner open so its deadline
	// reports unfinished cleanup before the test releases that session through Close.
	budget, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, updates.Stop(budget), context.DeadlineExceeded)
	require.ErrorIs(t, first.Context().Err(), context.Canceled)
	require.Error(t, <-waiter)
	first.Close()
	first.Close()
	require.NoError(t, updates.Stop(context.Background()))
	_, err = updates.Begin(context.Background())
	require.Equal(t, "SETTINGS_STOPPED", apperror.Reason(err))
}

// TestUpdatesWaitCancellationDoesNotReleaseOwner attempts admission with an already
// cancelled context while another session owns the slot. The failed admission
// returns cancellation, and the owner can still commit its prepared setting.
func TestUpdatesWaitCancellationDoesNotReleaseOwner(t *testing.T) {
	updates := New(&writeStoreStub{}).Updates()
	first, err := updates.Begin(context.Background())
	require.NoError(t, err)
	defer first.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = updates.Begin(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, first.Commit(PreparedChange{Module: "site", Values: map[string]string{"name": "next"}}))
}
