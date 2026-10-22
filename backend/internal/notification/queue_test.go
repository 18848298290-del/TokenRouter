package notification

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// blockingMailTask holds a credential send until its worker context is canceled.
type blockingMailTask struct {
	// started and once publish the first processing entry exactly once, allowing
	// the test to begin its drain deadline while a worker owns this held task.
	started chan struct{} // first processing signal
	once    sync.Once     // entry signal guard
}

// TestMailQueueBoundedDrain holds one credential task through a short shutdown deadline.
// It verifies deadline reporting, closed admission, the retained error on a
// later StopContext call, and terminal stopped state after another Start call.
func TestMailQueueBoundedDrain(t *testing.T) {
	p := &blockingMailTask{started: make(chan struct{})}
	q := NewEmailQueueService(p, 1)
	require.NoError(t, q.EnqueueVerifyCode("fixture@example.com", "fixture"))
	require.False(t, q.started)
	q.Start()
	q.Start()
	// Wait until the worker is inside the held processor so the deadline
	// exercises cancellation of in-progress credential work rather than only queued admission.
	<-p.started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := q.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Error(t, q.EnqueueVerifyCode("fixture@example.com", "fixture"))
	require.ErrorIs(t, q.StopContext(context.Background()), context.DeadlineExceeded)
	q.Start()
	require.True(t, q.stopped)
}

// TestMailQueueStopBeforeStartReportsPending admits one intent before any worker is registered.
// It verifies shutdown reports that queued task and that a later Start
// cannot register workers after the queue has reached terminal stopped state.
func TestMailQueueStopBeforeStartReportsPending(t *testing.T) {
	queue := NewEmailQueueService(nil, 1)
	require.NoError(t, queue.EnqueueVerifyCode("fixture@example.com", "fixture"))
	require.ErrorContains(t, queue.StopContext(context.Background()), "1 tasks")
	queue.Start()
	require.False(t, queue.started)
}

// TestMailQueueEnqueueBeforeStart checks both identity intents before workers exist.
// It verifies their payload fields and that only the first language hint is
// trimmed, including when that first value is empty and later values are present.
func TestMailQueueEnqueueBeforeStart(t *testing.T) {
	for _, taskType := range []string{TaskTypeVerifyCode, TaskTypePasswordReset} {
		t.Run(taskType, func(t *testing.T) {
			cases := []struct {
				name   string   // locale selection scenario
				values []string // supplied language hints
				want   string   // expected stored hint
			}{
				{name: "absent"},
				{name: "trimmed first", values: []string{" \tzh-CN\n", "en-US"}, want: "zh-CN"},
				{name: "empty first", values: []string{" \t", "en-US"}},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					queue := NewEmailQueueService(nil, 1)
					t.Cleanup(queue.cancel)
					require.NoError(t, enqueueMailQueueTask(queue, taskType, tc.values...))
					require.False(t, queue.started)
					expected := EmailTask{
						Email:    "fixture@example.com",
						SiteName: "fixture",
						TaskType: taskType,
						Locale:   tc.want,
					}
					if taskType == TaskTypePasswordReset {
						expected.ResetURL = "https://example.com/reset"
					}
					// A dormant queue has no consumer, so inspect the accepted intent
					// directly before shutdown to distinguish admission from credential processing.
					select {
					case task := <-queue.taskChan:
						require.Equal(t, expected, task)
					default:
						t.Fatal("accepted email task is missing")
					}
					require.NoError(t, queue.StopContext(context.Background()))
				})
			}
		})
	}
}

// TestMailQueueFullRejectsBothTaskKinds fills a dormant queue through each public entry.
// It verifies immediate capacity rejection, retained accepted tasks, and the stopped
// error taking precedence over a still-full buffer after shutdown closes admission.
func TestMailQueueFullRejectsBothTaskKinds(t *testing.T) {
	for _, taskType := range []string{TaskTypeVerifyCode, TaskTypePasswordReset} {
		t.Run(taskType, func(t *testing.T) {
			queue := NewEmailQueueService(nil, 1)
			t.Cleanup(queue.cancel)
			capacity := cap(queue.taskChan)
			for range capacity {
				require.NoError(t, enqueueMailQueueTask(queue, taskType))
			}
			require.EqualError(t, enqueueMailQueueTask(queue, taskType), "email queue is full")
			require.Len(t, queue.taskChan, capacity)
			require.EqualError(t, queue.StopContext(context.Background()), fmt.Sprintf("email queue drain incomplete: %d tasks", capacity))
			require.EqualError(t, enqueueMailQueueTask(queue, taskType), "email queue is stopped")
			require.Len(t, queue.taskChan, capacity)
		})
	}
}

// TestMailQueueEnqueueConcurrentStop races each identity entry with terminal shutdown.
// Accepted intents remain counted in the dormant buffer, and every rejected call
// reports the stopped error while channel closure remains safe under the race detector.
func TestMailQueueEnqueueConcurrentStop(t *testing.T) {
	for _, taskType := range []string{TaskTypeVerifyCode, TaskTypePasswordReset} {
		t.Run(taskType, func(t *testing.T) {
			queue := NewEmailQueueService(nil, 1)
			t.Cleanup(queue.cancel)
			require.NoError(t, enqueueMailQueueTask(queue, taskType))
			// Leave one unused slot even if every racing call wins admission.
			// This isolates stopped rejections from capacity errors while shutdown closes the same channel.
			attempts := cap(queue.taskChan) - 2
			start := make(chan struct{})
			results := make(chan error, attempts)
			stopped := make(chan error, 1)
			var callers sync.WaitGroup
			callers.Add(attempts + 1)
			// Release admission and shutdown together, then wait for every caller
			// before reading retained tasks so either lock winner has a complete observable result.
			for range attempts {
				go func() {
					defer callers.Done()
					<-start
					results <- enqueueMailQueueTask(queue, taskType)
				}()
			}
			go func() {
				defer callers.Done()
				<-start
				stopped <- queue.StopContext(context.Background())
			}()
			close(start)
			callers.Wait()
			close(results)
			accepted := 1
			for err := range results {
				if err == nil {
					accepted++
					continue
				}
				require.EqualError(t, err, "email queue is stopped")
			}
			require.Len(t, queue.taskChan, accepted)
			require.EqualError(t, <-stopped, fmt.Sprintf("email queue drain incomplete: %d tasks", accepted))
			require.EqualError(t, enqueueMailQueueTask(queue, taskType), "email queue is stopped")
		})
	}
}

// enqueueMailQueueTask submits a fixed identity fixture through the selected public entry.
// Callers select one of the two supported task kinds and supply language hints;
// password-reset fixtures include the same reset URL used by payload assertions.
func enqueueMailQueueTask(queue *EmailQueueService, taskType string, locale ...string) error {
	switch taskType {
	case TaskTypeVerifyCode:
		return queue.EnqueueVerifyCode("fixture@example.com", "fixture", locale...)
	case TaskTypePasswordReset:
		return queue.EnqueuePasswordReset("fixture@example.com", "fixture", "https://example.com/reset", locale...)
	default:
		panic("unsupported email task fixture")
	}
}

// SendVerifyCode reports its first entry and blocks until its context is canceled.
// It returns that cancellation error to the worker, giving drain tests a
// cooperative processor whose lifetime can be ended by StopContext's shared cancellation.
func (p *blockingMailTask) SendVerifyCode(ctx context.Context, _, _ string, _ ...string) error {
	// Entry publication lets the test start its deadline after processing begins.
	// The context wait keeps this call active until queue shutdown cancels the shared run context.
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	return ctx.Err()
}

// SendPasswordResetEmailWithCooldown uses the same cancellation-held behavior as the verification fixture.
// Reset URL input is ignored because these tests observe worker lifetime;
// the language values pass through to the shared held processor method.
func (p *blockingMailTask) SendPasswordResetEmailWithCooldown(ctx context.Context, a, b, c string, d ...string) error {
	return p.SendVerifyCode(ctx, a, b, d...)
}
