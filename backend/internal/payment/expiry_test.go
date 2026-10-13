package payment

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// expiryProbe records phase attempts and gates first-phase completion for
// shutdown tests.
type expiryProbe struct {
	// entered and release coordinate the active first phase with the test.
	// Closing entered permits StopContext after entry; ignoreCancel switches
	// completion from context cancellation to the release signal.
	entered chan struct{} // first phase entry signal
	release chan struct{} // uncooperative phase release signal
	// ignoreCancel makes the first phase wait for release after entry. The
	// timeout test closes release after its first stop attempt so a later join
	// can finish.
	ignoreCancel bool
	calls        atomic.Int32 // total attempted recovery phases
}

// ReconcilePendingPaymentOrders records first-phase entry and blocks until its
// selected completion signal arrives. Invoke it once for a probe, because it
// closes entered to notify the test.
//
// The count is published before that signal so the test can request shutdown
// with an admitted phase. ignoreCancel waits for release and succeeds; the
// ordinary path waits on the supplied context and returns its cancellation
// error. This separation lets shutdown tests distinguish a cancellation
// request from the worker's actual completion.
func (p *expiryProbe) ReconcilePendingPaymentOrders(ctx context.Context) (int, error) {
	// Record entry before releasing the test gate. The test can then issue
	// StopContext while this phase remains blocked on its completion signal.
	p.calls.Add(1)
	close(p.entered)
	// The uncooperative mode deliberately waits for the test release after
	// cancellation. This keeps worker completion pending while StopContext's own
	// deadline is exercised.
	if p.ignoreCancel {
		<-p.release
		return 0, nil
	}
	<-ctx.Done()
	return 0, ctx.Err()
}

// ReconcileProcessingOrders records an attempted processing phase and reports
// zero recovered orders. The test expects the total phase count to stay one
// after shutdown, making any later phase entry visible.
func (p *expiryProbe) ReconcileProcessingOrders(context.Context) (int, error) {
	p.calls.Add(1)
	return 0, nil
}

// ReconcilePaidFulfillmentOrders records an attempted fulfillment phase and
// returns zero completed orders. It performs no grant work, allowing shutdown
// assertions to detect a later invocation through the shared counter.
func (p *expiryProbe) ReconcilePaidFulfillmentOrders(context.Context) (int, error) {
	p.calls.Add(1)
	return 0, nil
}

// ExpireTimedOutOrders records an attempted expiry phase and returns zero
// closed orders. Cancellation tests use the accumulated attempts to detect
// whether the worker reached this final phase after stopping.
func (p *expiryProbe) ExpireTimedOutOrders(context.Context) (int, error) {
	p.calls.Add(1)
	return 0, nil
}

// TestOrderExpiryConstructionAndCancellation starts a probe blocked on its
// running context after checking constructor inactivity. Repeated start/stop
// and a post-stop start leave exactly the first phase entered and shutdown
// successfully joined.
func TestOrderExpiryConstructionAndCancellation(t *testing.T) {
	p := &expiryProbe{entered: make(chan struct{})}
	runner := NewOrderExpiry(p, time.Hour, ExpiryRuntime{})
	if p.calls.Load() != 0 {
		t.Fatal("构造不得启动")
	}
	runner.Start(context.Background())
	<-p.entered
	runner.Start(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.StopContext(ctx); err != nil {
		t.Fatal(err)
	}
	runner.Start(context.Background())
	if err := runner.StopContext(ctx); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("停止后不得进入后续阶段，calls=%d", p.calls.Load())
	}
}

// TestOrderExpiryStopBudgetReportsUnfinished holds a phase that ignores
// cancellation until the test releases it. The first wait reports deadline
// exceeded, and a later join succeeds with no additional phase invocation.
func TestOrderExpiryStopBudgetReportsUnfinished(t *testing.T) {
	p := &expiryProbe{entered: make(chan struct{}), release: make(chan struct{}), ignoreCancel: true}
	runner := NewOrderExpiry(p, time.Hour, ExpiryRuntime{})
	runner.Start(context.Background())
	<-p.entered
	// Keep the first phase blocked beyond this short waiting budget. StopContext
	// must report unfinished work before the test closes release and joins again.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := runner.StopContext(ctx)
	// The first stop attempt has already returned its error. Release the
	// uncooperative phase so the subsequent stop call can observe actual worker
	// termination.
	close(p.release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时必须报告未完成：%v", err)
	}
	if err := runner.StopContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("停止后不进入下一阶段")
	}
}
