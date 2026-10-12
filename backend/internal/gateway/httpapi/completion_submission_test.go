package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/gateway/completion"
	gatewaytelemetry "github.com/TokenFlux/TokenRouter/internal/gateway/telemetry"
	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry"
)

// Completion submission distinguishes an available pool, a stopped pool, and
// ordinary queue overflow. These cases observe task execution, inline deadlines,
// recovered panics, mandatory image submission, and correlation IDs delivered
// to the task after the HTTP source is captured.

// newUsageRecordTestPool starts a single-worker pool for asynchronous submission cases.
// Cleanup stops it after the test. A bounded queue with drop overflow and no
// autoscaling keeps task execution observable without creating extra workers.
func newUsageRecordTestPool(t *testing.T) *completion.UsageRecordWorkerPool {
	t.Helper()
	pool := completion.NewUsageRecordWorkerPoolWithOptions(completion.UsageRecordWorkerPoolOptions{
		Observe:               gatewaytelemetry.Completion,
		WorkerCount:           1,
		QueueSize:             8,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	pool.Start()
	t.Cleanup(pool.Stop)
	return pool
}

// newStoppedUsageRecordPoolForTest returns a pool whose normal lifecycle has ended.
// Starting and stopping before return makes later submissions report the stopped
// state, allowing the HTTP adapter's inline fallback to be inspected separately from queue overflow.
func newStoppedUsageRecordPoolForTest() *completion.UsageRecordWorkerPool {
	pool := completion.NewUsageRecordWorkerPoolWithOptions(completion.UsageRecordWorkerPoolOptions{
		Observe:        gatewaytelemetry.Completion,
		WorkerCount:    1,
		QueueSize:      1,
		TaskTimeout:    time.Second,
		OverflowPolicy: "sync",
	})
	pool.Start()
	pool.Stop()
	return pool
}

// TestGatewayHandlerSubmitUsageRecordTask_WithPool submits ordinary Messages
// completion to a running worker. Its completion channel must close within
// the observation deadline, proving the configured submission reaches task execution.
func TestGatewayHandlerSubmitUsageRecordTask_WithPool(t *testing.T) {
	pool := newUsageRecordTestPool(t)
	h := NewCompletionSubmission(pool, false)

	done := make(chan struct{})
	// Signal worker execution independently of Submit returning. The bounded
	// receive keeps a missing dispatch from leaving this test waiting forever.
	h.Submit(nil, func(ctx context.Context) {
		close(done)
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("task not executed")
	}
}

// TestGatewayHandlerSubmitUsageRecordTask_WithoutPoolSyncFallback omits the worker
// pool for Messages completion. The task must run before Submit returns
// and receive a deadline-bearing context on the inline path.
func TestGatewayHandlerSubmitUsageRecordTask_WithoutPoolSyncFallback(t *testing.T) {
	h := NewCompletionSubmission(nil, false)
	var called atomic.Bool

	h.Submit(nil, func(ctx context.Context) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("expected deadline in fallback context")
		}
		called.Store(true)
	})

	require.True(t, called.Load())
}

// TestGatewayHandlerSubmitUsageRecordTask_StoppedPoolFallsBackToSync uses an ended
// pool for an ordinary Messages task. Its execution flag must be set
// before return, protecting the constructor's configured stopped-pool fallback.
func TestGatewayHandlerSubmitUsageRecordTask_StoppedPoolFallsBackToSync(t *testing.T) {
	h := NewCompletionSubmission(newStoppedUsageRecordPoolForTest(), false)

	var executed atomic.Bool
	h.Submit(nil, func(ctx context.Context) {
		executed.Store(true)
	})
	require.True(t, executed.Load(), "池已停止时计费任务必须内联同步执行")
}

// TestGatewayHandlerSubmitUsageRecordTask_DropPolicyOverflowStillDrops occupies
// the worker and its queue before submitting ordinary completion. The task's
// flag must remain unset during the observation window, retaining configured
// queue overflow behavior even though stopped-pool fallback is enabled.
func TestGatewayHandlerSubmitUsageRecordTask_DropPolicyOverflowStillDrops(t *testing.T) {
	pool := completion.NewUsageRecordWorkerPoolWithOptions(completion.UsageRecordWorkerPoolOptions{
		Observe:        gatewaytelemetry.Completion,
		WorkerCount:    1,
		QueueSize:      1,
		TaskTimeout:    time.Minute,
		OverflowPolicy: "drop",
	})
	pool.Start()
	t.Cleanup(pool.Stop)
	h := NewCompletionSubmission(pool, false)

	started := make(chan struct{})
	block := make(chan struct{})
	// Release blocked tasks before pool cleanup waits for workers. Registering
	// this cleanup last makes its close run first under testing's cleanup order.
	t.Cleanup(func() { close(block) })
	require.Equal(t, completion.UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		close(started)
		<-block
	}))
	<-started
	require.Equal(t, completion.UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		<-block
	}))

	var executed atomic.Bool
	h.Submit(nil, func(ctx context.Context) {
		executed.Store(true)
	})
	// Observe whether the ordinary task executes during a short window while
	// the earlier worker and queue tasks remain blocked by their release channel.
	time.Sleep(50 * time.Millisecond)
	require.False(t, executed.Load(), "drop 溢出策略是运维显式配置，不应被同步兜底覆盖")
}

// TestGatewayHandlerSubmitUsageRecordTask_NilTask supplies no completion callback
// on the inline Messages path. Submission must tolerate that absent work
// without panicking or requiring a pool for a task that cannot execute.
func TestGatewayHandlerSubmitUsageRecordTask_NilTask(t *testing.T) {
	h := NewCompletionSubmission(nil, false)
	require.NotPanics(t, func() {
		h.Submit(nil, nil)
	})
}

// TestGatewayHandlerSubmitUsageRecordTask_WithoutPool_TaskPanicRecovered panics in
// inline Messages completion and then submits another task. The first call
// must not propagate the panic, and the subsequent task must still execute.
func TestGatewayHandlerSubmitUsageRecordTask_WithoutPool_TaskPanicRecovered(t *testing.T) {
	h := NewCompletionSubmission(nil, false)
	var called atomic.Bool

	require.NotPanics(t, func() {
		h.Submit(nil, func(ctx context.Context) {
			panic("usage task panic")
		})
	})

	h.Submit(nil, func(ctx context.Context) {
		called.Store(true)
	})
	require.True(t, called.Load(), "panic 后后续任务应仍可执行")
}

// TestOpenAIGatewayHandlerSubmitUsageRecordTask_WithPool uses the OpenAI submission
// options with a running worker. Its completion channel must close before
// the observation deadline, showing that ordinary work reaches the pool task.
func TestOpenAIGatewayHandlerSubmitUsageRecordTask_WithPool(t *testing.T) {
	pool := newUsageRecordTestPool(t)
	h := NewCompletionSubmission(pool, true)

	done := make(chan struct{})
	// Signal worker execution independently of Submit returning. The bounded
	// receive keeps a missing dispatch from leaving this test waiting forever.
	h.Submit(nil, func(ctx context.Context) {
		close(done)
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("task not executed")
	}
}

// TestOpenAIGatewayHandlerSubmitUsageRecordTask_WithoutPoolSyncFallback submits an
// OpenAI task with no pool. The execution flag must be set before return,
// and the task must observe a deadline on its inline context.
func TestOpenAIGatewayHandlerSubmitUsageRecordTask_WithoutPoolSyncFallback(t *testing.T) {
	h := NewCompletionSubmission(nil, true)
	var called atomic.Bool

	h.Submit(nil, func(ctx context.Context) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("expected deadline in fallback context")
		}
		called.Store(true)
	})

	require.True(t, called.Load())
}

// TestOpenAIGatewayHandlerSubmitUsageRecordTask_StoppedPoolFallsBackToSync supplies
// a stopped pool to the OpenAI constructor. Ordinary completion must execute
// inline before Submit returns, as observed through the task's atomic flag.
func TestOpenAIGatewayHandlerSubmitUsageRecordTask_StoppedPoolFallsBackToSync(t *testing.T) {
	h := NewCompletionSubmission(newStoppedUsageRecordPoolForTest(), true)

	var executed atomic.Bool
	h.Submit(nil, func(ctx context.Context) {
		executed.Store(true)
	})
	require.True(t, executed.Load(), "池已停止时 OpenAI 计费任务必须内联同步执行")
}

// TestOpenAIGatewayHandlerSubmitUsageRecordTask_NilTask passes a nil callback
// through OpenAI submission with no pool. The call must remain usable
// without panicking when there is no completion work to wrap or dispatch.
func TestOpenAIGatewayHandlerSubmitUsageRecordTask_NilTask(t *testing.T) {
	h := NewCompletionSubmission(nil, true)
	require.NotPanics(t, func() {
		h.Submit(nil, nil)
	})
}

// TestOpenAIGatewayHandlerSubmitUsageRecordTask_WithoutPool_TaskPanicRecovered raises
// an inline OpenAI task panic. Submission must contain that panic and
// continue accepting a later task whose execution flag is asserted afterward.
func TestOpenAIGatewayHandlerSubmitUsageRecordTask_WithoutPool_TaskPanicRecovered(t *testing.T) {
	h := NewCompletionSubmission(nil, true)
	var called atomic.Bool

	require.NotPanics(t, func() {
		h.Submit(nil, func(ctx context.Context) {
			panic("usage task panic")
		})
	})

	h.Submit(nil, func(ctx context.Context) {
		called.Store(true)
	})
	require.True(t, called.Load(), "panic 后后续任务应仍可执行")
}

// TestOpenAIGatewayHandlerSubmitMandatoryUsageRecordTask_DroppedTaskSyncFallback fills
// a drop-policy pool before mandatory submission. Its task execution flag
// must be set despite occupied capacity, and the blocked worker is released
// after submission so pool cleanup can finish.
func TestOpenAIGatewayHandlerSubmitMandatoryUsageRecordTask_DroppedTaskSyncFallback(t *testing.T) {
	pool := completion.NewUsageRecordWorkerPoolWithOptions(completion.UsageRecordWorkerPoolOptions{
		Observe:               gatewaytelemetry.Completion,
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	pool.Start()
	t.Cleanup(pool.Stop)
	h := NewCompletionSubmission(pool, true)

	block := make(chan struct{})
	release := make(chan struct{})
	// Wait until the first task occupies the worker before filling its queue.
	// Keep it blocked through submission, then release it before pool cleanup joins workers.
	pool.Submit(func(ctx context.Context) {
		close(block)
		<-release
	})
	<-block
	pool.Submit(func(ctx context.Context) {})

	var called atomic.Bool
	h.SubmitMandatory(nil, func(ctx context.Context) {
		called.Store(true)
	})
	close(release)

	require.True(t, called.Load(), "mandatory usage task must run synchronously when async submit is dropped")
}

// TestOpenAIGatewayHandlerSubmitOpenAIUsageRecordTask_ImageResultUsesMandatoryFallback
// supplies a positive image count to a full drop-policy pool. The task
// execution flag must be set after submission despite occupied capacity,
// observing the mandatory submission choice for completed image output.
func TestOpenAIGatewayHandlerSubmitOpenAIUsageRecordTask_ImageResultUsesMandatoryFallback(t *testing.T) {
	pool := completion.NewUsageRecordWorkerPoolWithOptions(completion.UsageRecordWorkerPoolOptions{
		Observe:               gatewaytelemetry.Completion,
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	pool.Start()
	t.Cleanup(pool.Stop)
	h := NewCompletionSubmission(pool, true)

	block := make(chan struct{})
	release := make(chan struct{})
	// Wait until the first task occupies the worker before filling its queue.
	// Keep it blocked through submission, then release it before pool cleanup joins workers.
	pool.Submit(func(ctx context.Context) {
		close(block)
		<-release
	})
	<-block
	pool.Submit(func(ctx context.Context) {})

	var called atomic.Bool
	h.SubmitImages(nil, 1, func(ctx context.Context) {
		called.Store(true)
	})
	close(release)

	require.True(t, called.Load(), "image usage task must be mandatory when async submit is dropped")
}

// TestOpenAIGatewayHandlerSubmitUsageRecordTask_PreservesRequestIDs installs the
// shared request-ID key through both compatibility names on an OpenAI request.
// The pool task must report the source value through both aliased lookups.
func TestOpenAIGatewayHandlerSubmitUsageRecordTask_PreservesRequestIDs(t *testing.T) {
	pool := newUsageRecordTestPool(t)
	h := NewCompletionSubmission(pool, true)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), telemetry.RequestID, "req-123")
	ctx = context.WithValue(ctx, telemetry.ClientRequestID, "req-123")
	c.Request = req.WithContext(ctx)

	got := make(chan [2]string, 1)
	// Buffer the observed lookups so the worker can finish before this test
	// starts receiving, while the receive deadline bounds the wait for missing execution.
	h.Submit(c, func(ctx context.Context) {
		requestID, _ := ctx.Value(telemetry.RequestID).(string)
		clientRequestID, _ := ctx.Value(telemetry.ClientRequestID).(string)
		got <- [2]string{requestID, clientRequestID}
	})

	select {
	case ids := <-got:
		require.Equal(t, [2]string{"req-123", "req-123"}, ids)
	case <-time.After(time.Second):
		t.Fatal("task not executed")
	}
}

// TestGatewayHandlerSubmitUsageRecordTask_PreservesRequestIDs submits Messages
// completion with RequestID and its compatibility alias naming the same key.
// Both task lookups must return the source value in the observed pair.
func TestGatewayHandlerSubmitUsageRecordTask_PreservesRequestIDs(t *testing.T) {
	pool := newUsageRecordTestPool(t)
	h := NewCompletionSubmission(pool, false)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ctx := context.WithValue(req.Context(), telemetry.RequestID, "req-gateway")
	ctx = context.WithValue(ctx, telemetry.ClientRequestID, "req-gateway")
	c.Request = req.WithContext(ctx)

	got := make(chan [2]string, 1)
	// Buffer the observed lookups so the worker can finish before this test
	// starts receiving, while the receive deadline bounds the wait for missing execution.
	h.Submit(c, func(ctx context.Context) {
		requestID, _ := ctx.Value(telemetry.RequestID).(string)
		clientRequestID, _ := ctx.Value(telemetry.ClientRequestID).(string)
		got <- [2]string{requestID, clientRequestID}
	})

	select {
	case ids := <-got:
		require.Equal(t, [2]string{"req-gateway", "req-gateway"}, ids)
	case <-time.After(time.Second):
		t.Fatal("task not executed")
	}
}
