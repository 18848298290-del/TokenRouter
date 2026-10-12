package notification

import (
	"context"
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
