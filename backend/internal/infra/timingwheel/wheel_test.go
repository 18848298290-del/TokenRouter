package timingwheel

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zeromicro/go-zero/core/collection"
)

// Wheel fixtures replace the package constructor serially to use short ticks
// or force startup failure. Buffered notifications check one-shot and
// recurring execution; gated callbacks separately establish cancellation
// waiting, prevention of rearming, and shutdown timeout followed by a later
// successful wait.

// TestNewTimingWheelService_InitFail_NoPanicAndReturnError replaces
// construction with a failure before Start. It requires an error and a normal
// Stop afterward, protecting cleanup when no underlying timer wheel was
// created.
func TestNewTimingWheelService_InitFail_NoPanicAndReturnError(t *testing.T) {
	// This serial fixture replaces the package constructor for its own failure
	// or timing setup. Cleanup restores the dependency before another test can
	// start a wheel.
	original := newTimingWheel
	t.Cleanup(func() { newTimingWheel = original })

	newTimingWheel = func(_ time.Duration, _ int, _ collection.Execute) (*collection.TimingWheel, error) {
		return nil, errors.New("boom")
	}

	svc := New()
	err := svc.Start()
	if err == nil {
		t.Fatalf("期望返回 error，但得到 nil")
	}
	svc.Stop() // Cleanup must be safe after initialization fails.
}

// TestNewTimingWheelService_Success starts a newly constructed wheel and then
// stops it. It requires a non-nil service and successful Start, protecting
// ordinary initialization and completion of its cleanup path.
func TestNewTimingWheelService_Success(t *testing.T) {
	svc := New()
	err := svc.Start()
	if err != nil {
		t.Fatalf("期望 err 为 nil，但得到: %v", err)
	}
	if svc == nil {
		t.Fatalf("期望 svc 非空，但得到 nil")
	}
	svc.Stop()
}

// TestNewTimingWheelService_ExecuteCallbackRunsFunc captures the callback
// supplied to the underlying constructor. It requires a callable dispatcher
// and observes it invoking a supplied function before stopping the wheel.
func TestNewTimingWheelService_ExecuteCallbackRunsFunc(t *testing.T) {
	// This serial fixture replaces the package constructor for its own failure
	// or timing setup. Cleanup restores the dependency before another test can
	// start a wheel.
	original := newTimingWheel
	t.Cleanup(func() { newTimingWheel = original })

	var captured collection.Execute
	newTimingWheel = func(interval time.Duration, numSlots int, execute collection.Execute) (*collection.TimingWheel, error) {
		captured = execute
		return original(interval, numSlots, execute)
	}

	svc := New()
	err := svc.Start()
	if err != nil {
		t.Fatalf("期望 err 为 nil，但得到: %v", err)
	}
	if captured == nil {
		t.Fatalf("期望 captured 非空，但得到 nil")
	}

	called := false
	captured("k", func() { called = true })
	if !called {
		t.Fatalf("期望 execute 回调触发传入函数执行")
	}

	svc.Stop()
}

// TestTimingWheelService_Schedule_ExecutesOnce uses short wheel ticks and a
// buffered callback notification. It requires a first execution within the
// observation budget and no second notification during a later waiting
// interval.
func TestTimingWheelService_Schedule_ExecutesOnce(t *testing.T) {
	// This serial fixture replaces the package constructor for its own failure
	// or timing setup. Cleanup restores the dependency before another test can
	// start a wheel.
	original := newTimingWheel
	t.Cleanup(func() { newTimingWheel = original })

	newTimingWheel = func(_ time.Duration, _ int, execute collection.Execute) (*collection.TimingWheel, error) {
		return original(10*time.Millisecond, 128, execute)
	}

	svc := New()
	err := svc.Start()
	if err != nil {
		t.Fatalf("期望 err 为 nil，但得到: %v", err)
	}
	defer svc.Stop()

	ch := make(chan struct{}, 1)
	svc.Schedule("once", 30*time.Millisecond, func() { ch <- struct{}{} })

	select {
	case <-ch:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("等待任务执行超时")
	}

	select {
	case <-ch:
		t.Fatalf("任务不应重复执行")
	case <-time.After(80 * time.Millisecond):
	}
}

// TestTimingWheelService_Cancel_PreventsExecution registers a delayed
// one-shot and cancels it immediately. It requires no callback notification
// during a bounded observation window, protecting cancellation before
// admission.
func TestTimingWheelService_Cancel_PreventsExecution(t *testing.T) {
	// This serial fixture replaces the package constructor for its own failure
	// or timing setup. Cleanup restores the dependency before another test can
	// start a wheel.
	original := newTimingWheel
	t.Cleanup(func() { newTimingWheel = original })

	newTimingWheel = func(_ time.Duration, _ int, execute collection.Execute) (*collection.TimingWheel, error) {
		return original(10*time.Millisecond, 128, execute)
	}

	svc := New()
	err := svc.Start()
	if err != nil {
		t.Fatalf("期望 err 为 nil，但得到: %v", err)
	}
	defer svc.Stop()

	ch := make(chan struct{}, 1)
	svc.Schedule("cancel", 80*time.Millisecond, func() { ch <- struct{}{} })
	svc.Cancel("cancel")

	select {
	case <-ch:
		t.Fatalf("任务已取消，不应执行")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestTimingWheelService_Schedule_AfterStop_LogsError stops the wheel before
// registering a one-shot callback that would fail the test. It requires the
// scheduling call to return normally and leave that callback unexecuted.
func TestTimingWheelService_Schedule_AfterStop_LogsError(t *testing.T) {
	svc := New()
	err := svc.Start()
	if err != nil {
		t.Fatalf("期望 err 为 nil，但得到: %v", err)
	}
	svc.Stop()

	svc.Schedule("after-stop", 100*time.Millisecond, func() {
		t.Fatal("不应被执行")
	})
}

// TestTimingWheelService_ScheduleRecurring_AfterStop_LogsError stops the
// wheel before registering recurring work that would fail the test. It
// requires registration to return normally and leave the rejected callback
// unexecuted.
func TestTimingWheelService_ScheduleRecurring_AfterStop_LogsError(t *testing.T) {
	svc := New()
	err := svc.Start()
	if err != nil {
		t.Fatalf("期望 err 为 nil，但得到: %v", err)
	}
	svc.Stop()

	svc.ScheduleRecurring("after-stop-rec", 100*time.Millisecond, func() {
		t.Fatal("不应被执行")
	})
}

// TestTimingWheelService_Stop_Idempotent starts a wheel and calls Stop twice
// in succession. It requires both calls to complete normally, protecting
// reuse of terminal shutdown state and its shared completion signal.
func TestTimingWheelService_Stop_Idempotent(t *testing.T) {
	svc := New()
	err := svc.Start()
	if err != nil {
		t.Fatalf("期望 err 为 nil，但得到: %v", err)
	}
	svc.Stop()
	svc.Stop() // Repeated Stop calls are safe.
}

// TestTimingWheelService_ScheduleRecurring_ExecutesMultipleTimes observes a
// short-delay recurring callback with an atomic counter. It requires at least
// two completed observations before the deadline, protecting normal rearming
// after callback completion.
func TestTimingWheelService_ScheduleRecurring_ExecutesMultipleTimes(t *testing.T) {
	// This serial fixture replaces the package constructor for its own failure
	// or timing setup. Cleanup restores the dependency before another test can
	// start a wheel.
	original := newTimingWheel
	t.Cleanup(func() { newTimingWheel = original })

	newTimingWheel = func(_ time.Duration, _ int, execute collection.Execute) (*collection.TimingWheel, error) {
		return original(10*time.Millisecond, 128, execute)
	}

	svc := New()
	err := svc.Start()
	if err != nil {
		t.Fatalf("期望 err 为 nil，但得到: %v", err)
	}
	defer svc.Stop()

	var count int32
	svc.ScheduleRecurring("rec", 30*time.Millisecond, func() { atomic.AddInt32(&count, 1) })

	deadline := time.Now().Add(500 * time.Millisecond)
	for atomic.LoadInt32(&count) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&count) < 2 {
		t.Fatalf("期望周期任务至少执行 2 次，但只执行了 %d 次", atomic.LoadInt32(&count))
	}
}

// TestWheelCancelWaitsAndPreventsRearm holds a recurring callback behind a
// gate while launching cancellation. It requires no cancellation completion
// during the observation window, completion after gate release, and one
// callback after a further interval for detecting rearm.
func TestWheelCancelWaitsAndPreventsRearm(t *testing.T) {
	// This serial fixture replaces the package constructor for its own failure
	// or timing setup. Cleanup restores the dependency before another test can
	// start a wheel.
	original := newTimingWheel
	newTimingWheel = func(_ time.Duration, _ int, execute collection.Execute) (*collection.TimingWheel, error) {
		return original(time.Millisecond, 64, execute)
	}
	t.Cleanup(func() { newTimingWheel = original })
	wheel := New()
	require.NoError(t, wheel.Start())
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	wheel.ScheduleRecurring("flush", time.Millisecond, func() {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
	})
	<-entered
	canceled := make(chan struct{})
	// Run cancellation independently while the admitted callback is gated, so
	// its completion signal can establish that waiting lasts until callback release.
	go func() { wheel.CancelAndWait("flush"); close(canceled) }()
	select {
	case <-canceled:
		t.Fatal("回调仍在执行")
	case <-time.After(10 * time.Millisecond):
	}
	// Release the admitted callback before joining cancellation, then allow a
	// short further interval to detect any recurring timer that escaped removal.
	close(release)
	<-canceled
	time.Sleep(10 * time.Millisecond)
	require.EqualValues(t, 1, calls.Load())
	wheel.Stop()
}

// TestWheelShutdownTimeoutReportsIncompleteCallback holds an admitted
// callback beyond the shutdown budget. It requires a deadline error,
// rejection of later scheduling, and successful repeated shutdown after the
// callback gate is released.
func TestWheelShutdownTimeoutReportsIncompleteCallback(t *testing.T) {
	// This serial fixture replaces the package constructor for its own failure
	// or timing setup. Cleanup restores the dependency before another test can
	// start a wheel.
	original := newTimingWheel
	newTimingWheel = func(_ time.Duration, _ int, execute collection.Execute) (*collection.TimingWheel, error) {
		return original(time.Millisecond, 64, execute)
	}
	t.Cleanup(func() { newTimingWheel = original })
	wheel := New()
	require.NoError(t, wheel.Start())
	entered := make(chan struct{})
	release := make(chan struct{})
	wheel.Schedule("busy", time.Millisecond, func() { close(entered); <-release })
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, wheel.Shutdown(ctx), context.DeadlineExceeded)
	wheel.Schedule("late", time.Millisecond, func() { t.Error("停止后执行了任务") })
	close(release)
	require.NoError(t, wheel.Shutdown(context.Background()))
}
