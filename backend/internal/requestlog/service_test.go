package requestlog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

type testRepository struct {
	mu      sync.Mutex
	saved   []telemetry.RequestRecord
	save    func(context.Context, []telemetry.RequestRecord) error
	findErr error
}

func (r *testRepository) Save(ctx context.Context, records []telemetry.RequestRecord) error {
	if r.save != nil {
		if err := r.save(ctx, records); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saved = append(r.saved, records...)
	return nil
}

func (r *testRepository) Find(context.Context, string, int64, bool) ([]Detail, error) {
	return nil, r.findErr
}

func (r *testRepository) Cleanup(context.Context, time.Time) error { return nil }

func (r *testRepository) records() []telemetry.RequestRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]telemetry.RequestRecord(nil), r.saved...)
}

// TestFlushRetriesDatabaseFailure checks that failed snapshots remain buffered until the database recovers.
func TestFlushRetriesDatabaseFailure(t *testing.T) {
	unavailable := errors.New("database unavailable")
	repo := &testRepository{save: func(context.Context, []telemetry.RequestRecord) error { return unavailable }}
	service := NewService(repo, 30, nil)
	service.Observe(telemetry.RequestRecord{RequestID: "id", State: "failed"})
	require.ErrorIs(t, service.Flush(t.Context()), unavailable)
	health, err := service.Health()
	require.NoError(t, err)
	require.Equal(t, 1, health.Pending)
	repo.save = nil
	require.NoError(t, service.Flush(t.Context()))
	health, err = service.Health()
	require.NoError(t, err)
	require.Zero(t, health.Pending)
	require.Len(t, repo.records(), 1)
}

// TestFlushFallsBackPerRecord retains failures and removes successful per-record retries from the buffer.
func TestFlushFallsBackPerRecord(t *testing.T) {
	failure := errors.New("write failed")
	repo := &testRepository{save: func(_ context.Context, records []telemetry.RequestRecord) error {
		if len(records) > 1 || records[0].RequestID == "retry" {
			return failure
		}
		return nil
	}}
	service := NewService(repo, 30, nil)
	service.Observe(telemetry.RequestRecord{RequestID: "saved"})
	service.Observe(telemetry.RequestRecord{RequestID: "retry"})
	require.ErrorIs(t, service.Flush(t.Context()), failure)
	require.Equal(t, "saved", repo.records()[0].RequestID)
	require.Len(t, repo.records(), 1)
	health, err := service.Health()
	require.NoError(t, err)
	require.Equal(t, 1, health.Pending)
	require.Positive(t, health.Failures)
	repo.save = nil
	require.NoError(t, service.Flush(t.Context()))
	require.Len(t, repo.records(), 2)
}

// TestFlushDiscardsInvalidRecords checks permanent errors in single and mixed batches while valid snapshots proceed.
func TestFlushDiscardsInvalidRecords(t *testing.T) {
	for _, withValid := range []bool{false, true} {
		t.Run(fmt.Sprint(withValid), func(t *testing.T) {
			attempts := 0
			var messages []string
			repo := &testRepository{save: func(_ context.Context, records []telemetry.RequestRecord) error {
				attempts++
				for _, record := range records {
					if record.RequestID == "invalid" {
						return fmt.Errorf("malformed data: %w", ErrInvalidRecord)
					}
				}
				return nil
			}}
			service := NewService(repo, 30, func(format string, args ...any) {
				messages = append(messages, fmt.Sprintf(format, args...))
			})
			service.Observe(telemetry.RequestRecord{RequestID: "invalid"})
			if withValid {
				service.Observe(telemetry.RequestRecord{RequestID: "valid"})
			}
			require.NoError(t, service.Flush(t.Context()))
			health, err := service.Health()
			require.NoError(t, err)
			require.Zero(t, health.Pending)
			require.Positive(t, health.Failures)
			require.Contains(t, messages[len(messages)-1], "discard invalid request invalid")
			if withValid {
				require.Len(t, repo.records(), 1)
				require.Equal(t, "valid", repo.records()[0].RequestID)
			}
			before := attempts
			require.NoError(t, service.Flush(t.Context()))
			require.Equal(t, before, attempts)
			require.NoError(t, service.Stop(t.Context()))
		})
	}
}

// TestDiscardInvalidPreservesNewVersion checks that discarding an invalid version retains a concurrently corrected snapshot.
func TestDiscardInvalidPreservesNewVersion(t *testing.T) {
	repo := &testRepository{}
	service := NewService(repo, 30, nil)
	now := time.Now()
	service.Observe(telemetry.RequestRecord{RequestID: "id", UpdatedAt: now})
	repo.save = func(context.Context, []telemetry.RequestRecord) error {
		service.Observe(telemetry.RequestRecord{RequestID: "id", UpdatedAt: now.Add(time.Second), State: "completed"})
		return ErrInvalidRecord
	}
	require.NoError(t, service.Flush(t.Context()))
	health, err := service.Health()
	require.NoError(t, err)
	require.Equal(t, 1, health.Pending)
	repo.save = nil
	require.NoError(t, service.Flush(t.Context()))
	require.Equal(t, "completed", repo.records()[0].State)
}

// TestObserveCoalescesSnapshots checks version, terminal-state, and alias merging plus independence from caller mutations.
func TestObserveCoalescesSnapshots(t *testing.T) {
	repo := &testRepository{}
	service := NewService(repo, 30, nil)
	now := time.Now()
	first := telemetry.RequestRecord{RequestID: "id", StartedAt: now, UpdatedAt: now, State: "failed", Aliases: []telemetry.RequestAlias{{Kind: "caller", Value: "client"}}}
	service.Observe(first)
	first.Aliases[0].Value = "changed"
	service.Observe(telemetry.RequestRecord{RequestID: "id", UpdatedAt: now.Add(time.Second), State: "completed", Aliases: []telemetry.RequestAlias{{Kind: "upstream", Value: "supplier"}}})
	service.Observe(telemetry.RequestRecord{RequestID: "id", UpdatedAt: now, State: "running"})
	require.NoError(t, service.Flush(t.Context()))
	records := repo.records()
	require.Len(t, records, 1)
	require.Equal(t, "failed", records[0].State)
	require.Equal(t, now, records[0].StartedAt)
	require.ElementsMatch(t, []telemetry.RequestAlias{{Kind: "caller", Value: "client"}, {Kind: "upstream", Value: "supplier"}}, records[0].Aliases)
}

// TestFlushPreservesConcurrentUpdate checks that an update received during a write remains pending for the next batch.
func TestFlushPreservesConcurrentUpdate(t *testing.T) {
	repo := &testRepository{}
	service := NewService(repo, 30, nil)
	now := time.Now()
	service.Observe(telemetry.RequestRecord{RequestID: "id", UpdatedAt: now, State: "running"})
	repo.save = func(context.Context, []telemetry.RequestRecord) error {
		service.Observe(telemetry.RequestRecord{RequestID: "id", UpdatedAt: now.Add(time.Second), State: "completed"})
		return nil
	}
	require.NoError(t, service.Flush(t.Context()))
	health, err := service.Health()
	require.NoError(t, err)
	require.Equal(t, 1, health.Pending)
	repo.save = nil
	require.NoError(t, service.Flush(t.Context()))
	require.Equal(t, "completed", repo.records()[1].State)
}

// TestObserveOverflowWritesSynchronously checks the buffer limit and visible failure counts for overflow writes.
func TestObserveOverflowWritesSynchronously(t *testing.T) {
	repo := &testRepository{}
	service := NewService(repo, 30, nil)
	for i := range requestBufferCapacity {
		service.Observe(telemetry.RequestRecord{RequestID: fmt.Sprint(i)})
	}
	service.Observe(telemetry.RequestRecord{RequestID: "overflow"})
	require.Len(t, repo.records(), 1)
	require.Equal(t, "overflow", repo.records()[0].RequestID)
	repo.save = func(context.Context, []telemetry.RequestRecord) error { return errors.New("database unavailable") }
	service.Observe(telemetry.RequestRecord{RequestID: "failed-overflow"})
	health, err := service.Health()
	require.NoError(t, err)
	require.Equal(t, requestBufferCapacity, health.Pending)
	require.Equal(t, uint64(1), health.Failures)
}

// TestStartFlushesInBackground uses a virtual clock to check batching and repeated lifecycle calls.
func TestStartFlushesInBackground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &testRepository{}
		service := NewService(repo, 30, nil)
		require.NoError(t, service.Start(t.Context()))
		require.NoError(t, service.Start(t.Context()))
		service.Observe(telemetry.RequestRecord{RequestID: "id"})
		synctest.Wait()
		time.Sleep(requestFlushInterval)
		synctest.Wait()
		require.Len(t, repo.records(), 1)
		require.NoError(t, service.Stop(t.Context()))
		require.NoError(t, service.Stop(t.Context()))
		require.NoError(t, service.Start(t.Context()))
	})
}

// TestStartSlowsFailedWrites checks the one-second retry delay before saving a buffered snapshot after recovery.
func TestStartSlowsFailedWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts atomic.Int64
		repo := &testRepository{save: func(context.Context, []telemetry.RequestRecord) error {
			if attempts.Add(1) == 1 {
				return errors.New("database unavailable")
			}
			return nil
		}}
		service := NewService(repo, 30, nil)
		service.Observe(telemetry.RequestRecord{RequestID: "id"})
		require.NoError(t, service.Start(t.Context()))
		synctest.Wait()
		time.Sleep(requestFlushInterval)
		synctest.Wait()
		require.Equal(t, int64(1), attempts.Load())
		time.Sleep(requestRetryInterval / 2)
		synctest.Wait()
		require.Equal(t, int64(1), attempts.Load())
		time.Sleep(requestRetryInterval / 2)
		synctest.Wait()
		require.Equal(t, int64(2), attempts.Load())
		require.Len(t, repo.records(), 1)
		require.NoError(t, service.Stop(t.Context()))
	})
}

// TestStopDrainsAllBatches checks that shutdown saves all pending snapshots even before Start.
func TestStopDrainsAllBatches(t *testing.T) {
	repo := &testRepository{}
	service := NewService(repo, 30, nil)
	for i := range requestBatchSize + 1 {
		service.Observe(telemetry.RequestRecord{RequestID: fmt.Sprint(i)})
	}
	require.NoError(t, service.Stop(t.Context()))
	require.Len(t, repo.records(), requestBatchSize+1)
	service.Observe(telemetry.RequestRecord{RequestID: "late"})
	require.Equal(t, "late", repo.records()[requestBatchSize+1].RequestID)
	health, err := service.Health()
	require.NoError(t, err)
	require.Zero(t, health.Pending)
}

// TestStopWaitsForSynchronousWrite checks that shutdown waits for overflow writes started before admission closes.
func TestStopWaitsForSynchronousWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		repo := &testRepository{save: func(_ context.Context, records []telemetry.RequestRecord) error {
			if len(records) == 1 && records[0].RequestID == "overflow" {
				close(entered)
				<-release
			}
			return nil
		}}
		service := NewService(repo, 30, nil)
		for i := range requestBufferCapacity {
			service.Observe(telemetry.RequestRecord{RequestID: fmt.Sprint(i)})
		}
		go service.Observe(telemetry.RequestRecord{RequestID: "overflow"})
		<-entered
		stopped := make(chan error, 1)
		go func() { stopped <- service.Stop(t.Context()) }()
		synctest.Wait()
		select {
		case <-stopped:
			t.Fatal("stop returned before the synchronous write completed")
		default:
		}
		close(release)
		require.NoError(t, <-stopped)
		require.Len(t, repo.records(), requestBufferCapacity+1)
	})
}

// TestStopTimeoutReportsPending checks that write timeout retains pending counts and makes shutdown fail.
func TestStopTimeoutReportsPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &testRepository{save: func(ctx context.Context, _ []telemetry.RequestRecord) error {
			<-ctx.Done()
			return ctx.Err()
		}}
		service := NewService(repo, 30, nil)
		service.Observe(telemetry.RequestRecord{RequestID: "id"})
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		require.ErrorIs(t, service.Stop(ctx), context.DeadlineExceeded)
		synctest.Wait()
		require.ErrorIs(t, service.Stop(t.Context()), context.DeadlineExceeded)
		health, err := service.Health()
		require.NoError(t, err)
		require.Equal(t, 1, health.Pending)
		require.Positive(t, health.Failures)
	})
}

// TestFindPendingChecksOwnership checks ownership and response isolation for in-memory snapshots.
func TestFindPendingChecksOwnership(t *testing.T) {
	repo := &testRepository{findErr: errors.New("database unavailable")}
	service := NewService(repo, 30, nil)
	service.Observe(telemetry.RequestRecord{RequestID: "id", UserID: 1, Attempts: []telemetry.RequestAttempt{{RequestID: "supplier"}}})
	items, err := service.Find(t.Context(), "id", 2, false)
	require.Error(t, err)
	require.Empty(t, items)
	items, err = service.Find(t.Context(), "id", 1, false)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.True(t, items[0].Pending)
	items[0].Attempts[0].RequestID = ""
	items, err = service.Find(t.Context(), "id", 0, true)
	require.NoError(t, err)
	require.Equal(t, "supplier", items[0].Attempts[0].RequestID)
}
