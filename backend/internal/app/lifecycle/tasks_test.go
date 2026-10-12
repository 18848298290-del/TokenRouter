package lifecycle

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Task shutdown permits a running parent to register child work before final
// sealing. The fixtures contrast that final drain with an ordinary reusable
// barrier and exercise timeout diagnostics followed by a later successful wait
// after callback completion.

// TestTasksStopDrainsChildrenAndRejectsLaterWork launches Stop around a parent
// that registers blocked child work. Child registration must succeed, Stop
// must stay pending before child release, and later work must be rejected after completion.
func TestTasksStopDrainsChildrenAndRejectsLaterWork(t *testing.T) {
	tasks := NewTasks()
	parent := make(chan struct{})
	child := make(chan struct{})
	spawned := make(chan struct{})
	var completed atomic.Int32
	// Hold the parent until the shutdown goroutine is launched, then register
	// the child before the parent returns. spawned announces that registration; child keeps the
	// tracked group active until the test permits completion and reads its count.
	require.True(t, tasks.Go("parent", func() {
		<-parent
		if !tasks.Go("child", func() { <-child; completed.Add(1) }) {
			t.Error("子任务在 drain 期间被拒绝")
		}
		close(spawned)
	}))
	result := make(chan error, 1)
	// Launch Stop before releasing the parent to exercise registration around
	// shutdown. The spawned signal establishes that blocked child work exists
	// when the test checks that Stop has not returned.
	go func() { result <- tasks.Stop(context.Background()) }()
	close(parent)
	<-spawned
	select {
	case <-result:
		t.Fatal("子任务尚未完成却已返回")
	default:
	}
	close(child)
	require.NoError(t, <-result)
	require.EqualValues(t, 1, completed.Load())
	require.False(t, tasks.Go("after-stop", func() { t.Error("关闭后启动任务") }))
}

// TestTasksBarrierKeepsLaterConsumerSideEffects first waits on an empty
// tracker, then submits later work. Ordinary Wait must leave admission usable,
// and the later callback must complete before final Stop succeeds.
func TestTasksBarrierKeepsLaterConsumerSideEffects(t *testing.T) {
	tasks := NewTasks()
	require.NoError(t, tasks.Wait(context.Background()))
	done := make(chan struct{})
	// Announce completion inside the callback before its tracked count is released.
	// This distinguishes its execution from the preceding wait on an empty group.
	require.True(t, tasks.Go("later-consumer", func() { close(done) }))
	require.NoError(t, tasks.Wait(context.Background()))
	<-done
	require.NoError(t, tasks.Stop(context.Background()))
}

// TestTasksTimeoutNamesUnfinishedWork holds a named callback past the stop
// deadline. The error must include its active count, and a later stop must
// succeed after that callback is released.
func TestTasksTimeoutNamesUnfinishedWork(t *testing.T) {
	tasks := NewTasks()
	finish := make(chan struct{})
	// Keep the callback active until timeout diagnostics are inspected. Its
	// release signal then permits the final idle transition that seals admission.
	require.True(t, tasks.Go("quota-persistence", func() { <-finish }))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := tasks.Stop(ctx)
	require.True(t, errors.Is(err, context.DeadlineExceeded))
	require.Contains(t, err.Error(), "quota-persistence=1")
	// A timed-out wait leaves callback work admitted. Release it before retrying
	// Stop so the final completion can seal admission and close the idle signal.
	close(finish)
	require.NoError(t, tasks.Stop(context.Background()))
}
