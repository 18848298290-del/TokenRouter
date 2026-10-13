package ops

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// testErrorQueue is a shared serial-test fixture replaced only after the
// preceding queue's workers have completed shutdown.
var testErrorQueue = NewErrorLogQueue(ErrorLogQueueOptions{})

// TestOpsErrorLogQueueByteBudget reserves nearly the complete byte budget, then
// attempts to exceed it. It checks the rejected reservation leaves byte and job
// counters unchanged and restores those reservation counters afterward.
func TestOpsErrorLogQueueByteBudget(t *testing.T) {
	previousBytes := testErrorQueue.opsErrorLogQueueBytes.Load()
	previousLen := testErrorQueue.opsErrorLogQueueLen.Load()
	// Reset shared reservation counters for this serial test and restore their
	// previous values in cleanup so later fixture users do not inherit the
	// reservation.
	testErrorQueue.opsErrorLogQueueBytes.Store(0)
	testErrorQueue.opsErrorLogQueueLen.Store(0)
	t.Cleanup(func() {
		testErrorQueue.opsErrorLogQueueBytes.Store(previousBytes)
		testErrorQueue.opsErrorLogQueueLen.Store(previousLen)
	})

	if !testErrorQueue.reserveOpsErrorLogQueueBytes(opsErrorLogMaxQueueBytes - 1) {
		t.Fatal("first reservation within byte budget should succeed")
	}
	if testErrorQueue.reserveOpsErrorLogQueueBytes(2) {
		t.Fatal("reservation beyond byte budget should be rejected")
	}
	if got := testErrorQueue.OpsErrorLogQueueBytes(); got != opsErrorLogMaxQueueBytes-1 {
		t.Fatalf("queued bytes = %d, want %d", got, opsErrorLogMaxQueueBytes-1)
	}
	if got := testErrorQueue.OpsErrorLogQueueLength(); got != 1 {
		t.Fatalf("queue length = %d, want 1", got)
	}
}

// TestEstimateOpsErrorLogJobBytesIncludesVariablePayloads supplies body,
// message, UserAgent, and optional upstream strings. It checks estimated weight
// exceeds the base record plus body length, covering aggregate weight beyond
// the body alone.
func TestEstimateOpsErrorLogJobBytesIncludesVariablePayloads(t *testing.T) {
	base := estimateOpsErrorLogJobBytes(&OpsInsertErrorLogInput{})
	message := "upstream message"
	detail := "upstream detail"
	events := `[{"error":"x"}]`
	entry := &OpsInsertErrorLogInput{
		ErrorBody:            strings.Repeat("x", 1024),
		ErrorMessage:         "client error",
		UserAgent:            "test-agent",
		UpstreamErrorMessage: &message,
		UpstreamErrorDetail:  &detail,
		UpstreamErrorsJSON:   &events,
	}
	if got := estimateOpsErrorLogJobBytes(entry); got <= base+1024 {
		t.Fatalf("estimated bytes = %d, expected variable payloads above %d", got, base+1024)
	}
}

// TestEnqueueOpsErrorLog_QueueFullDrop keeps a one-slot fixture channel full
// with worker setup suppressed. It checks the second send is dropped while
// successful enqueue and waiting counters remain at one.
func TestEnqueueOpsErrorLog_QueueFullDrop(t *testing.T) {
	resetOpsErrorLoggerStateForTest(t)

	// Consume the start barrier with an empty callback so no worker can drain the
	// one-slot channel before the second enqueue.
	testErrorQueue.opsErrorLogOnce.Do(func() {})

	// Install the manual channel under its lifecycle mutex, matching publication
	// used by workers while keeping this fixture's startup suppressed.
	testErrorQueue.opsErrorLogMu.Lock()
	testErrorQueue.opsErrorLogQueue = make(chan opsErrorLogJob, 1)
	testErrorQueue.opsErrorLogMu.Unlock()

	ops := newLegacyShapeOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	entry := &OpsInsertErrorLogInput{ErrorPhase: "upstream", ErrorType: "upstream_error"}

	testErrorQueue.Enqueue(ops, entry)
	testErrorQueue.Enqueue(ops, entry)

	require.Equal(t, int64(1), testErrorQueue.OpsErrorLogEnqueuedTotal())
	require.Equal(t, int64(1), testErrorQueue.OpsErrorLogDroppedTotal())
	require.Equal(t, int64(1), testErrorQueue.OpsErrorLogQueueLength())
}

// TestEnqueueOpsErrorLog_EarlyReturnBranches submits nil inputs and fixtures
// with shutdown, stopping, or an uninitialized channel. It checks none of those
// cases increases successful enqueue accounting.
func TestEnqueueOpsErrorLog_EarlyReturnBranches(t *testing.T) {
	resetOpsErrorLoggerStateForTest(t)

	ops := newLegacyShapeOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	entry := &OpsInsertErrorLogInput{ErrorPhase: "upstream", ErrorType: "upstream_error"}

	testErrorQueue.Enqueue(nil, entry)
	testErrorQueue.Enqueue(ops, nil)
	require.Equal(t, int64(0), testErrorQueue.OpsErrorLogEnqueuedTotal())

	// Close the early shutdown signal alone to reach its guard without starting
	// workers or changing the fixture's stopping state.
	testErrorQueue.opsErrorLogShutdownOnce.Do(func() { close(testErrorQueue.opsErrorLogShutdownCh) })
	testErrorQueue.Enqueue(ops, entry)
	require.Equal(t, int64(0), testErrorQueue.OpsErrorLogEnqueuedTotal())

	resetOpsErrorLoggerStateForTest(t)
	// Publish stopping under the lifecycle mutex so this fixture exercises the
	// same state check used by normal shutdown.
	testErrorQueue.opsErrorLogMu.Lock()
	testErrorQueue.opsErrorLogStopping = true
	testErrorQueue.opsErrorLogMu.Unlock()
	testErrorQueue.Enqueue(ops, entry)
	require.Equal(t, int64(0), testErrorQueue.OpsErrorLogEnqueuedTotal())

	// Use a fresh queue with its setup barrier consumed, leaving the channel nil
	// so the locked post-start guard omits this submission.
	resetOpsErrorLoggerStateForTest(t)
	testErrorQueue.opsErrorLogOnce.Do(func() {})
	testErrorQueue.opsErrorLogMu.Lock()
	testErrorQueue.opsErrorLogQueue = nil
	testErrorQueue.opsErrorLogMu.Unlock()
	testErrorQueue.Enqueue(ops, entry)
	require.Equal(t, int64(0), testErrorQueue.OpsErrorLogEnqueuedTotal())
}

// TestEnqueueOpsErrorLog_SanitizesAndBoundsBodyBeforeQueue submits a JSON body
// containing an oversized authorization value to a manually controlled queue.
// It checks the received job omits that secret and reports bounded, sanitized
// body accounting.
func TestEnqueueOpsErrorLog_SanitizesAndBoundsBodyBeforeQueue(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 1)
	ops := newLegacyShapeOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	secret := strings.Repeat("s", OpsErrorLogQueueBodyMaxBytes)
	entry := &OpsInsertErrorLogInput{
		ErrorPhase: "request",
		ErrorType:  "api_error",
		ErrorBody:  `{"authorization":"Bearer ` + secret + `","message":"failed"}`,
	}

	testErrorQueue.Enqueue(ops, entry)
	job := <-testErrorQueue.opsErrorLogQueue
	require.LessOrEqual(t, len(job.entry.ErrorBody), OpsErrorLogQueueBodyMaxBytes)
	require.NotContains(t, job.entry.ErrorBody, secret)
	require.Equal(t, int64(1), testErrorQueue.OpsErrorLogSanitizedTotal())
}

// TestNormalizeOpsPersistentUserAgentBoundsAndPreservesUTF8 places a multibyte
// character across the UserAgent byte limit. It checks the result remains valid
// UTF-8 and excludes the character's trailing payload after prefix truncation.
func TestNormalizeOpsPersistentUserAgentBoundsAndPreservesUTF8(t *testing.T) {
	value := strings.Repeat("a", opsErrorLogMaxUserAgentBytes-1) + "你" + strings.Repeat("b", 32)
	got := normalizeOpsPersistentUserAgent("  " + value + "  ")
	require.LessOrEqual(t, len(got), opsErrorLogMaxUserAgentBytes)
	require.True(t, utf8.ValidString(got))
	require.NotContains(t, got, "b")
}

// TestOpsErrorLogShutdownDrainsCapturedQueue starts workers and places empty
// jobs directly into their channel before shutdown. It checks repeated waits
// succeed, reserved length ends at zero, and the joined-worker flag is
// published.
func TestOpsErrorLogShutdownDrainsCapturedQueue(t *testing.T) {
	resetOpsErrorLoggerStateForTest(t)
	// Reset this shared fixture again after the assertions, joining any workers
	// even when a require assertion ends the test early.
	t.Cleanup(func() { resetOpsErrorLoggerStateForTest(t) })
	testErrorQueue.opsErrorLogOnce.Do(testErrorQueue.startOpsErrorLogWorkers)
	// Capture the channel under the read lock and fill it directly; empty jobs
	// exercise worker termination rather than successful database delivery.
	testErrorQueue.opsErrorLogMu.RLock()
	queue := testErrorQueue.opsErrorLogQueue
	// Use sixty-four empty jobs as the captured-channel drain fixture.
	for range 64 {
		queue <- opsErrorLogJob{}
	}
	testErrorQueue.opsErrorLogMu.RUnlock()
	// Give shutdown assertions a one-second wait context so a failure to join the
	// captured workers returns as a test failure.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, testErrorQueue.Shutdown(ctx))
	require.NoError(t, testErrorQueue.Shutdown(ctx))
	require.Zero(t, testErrorQueue.OpsErrorLogQueueLength())
	require.True(t, testErrorQueue.opsErrorLogDrained.Load())
}

// resetOpsErrorLoggerStateForTest waits for the shared fixture to shut down
// before replacing it with a new lazy queue. Tests use this serially because
// the package variable and callback state are shared.
func resetOpsErrorLoggerStateForTest(t *testing.T) {
	t.Helper()
	// Wait under a one-second assertion budget before replacing the package
	// fixture so workers cannot keep accessing the prior test's state.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, testErrorQueue.Shutdown(ctx))
	testErrorQueue = NewErrorLogQueue(ErrorLogQueueOptions{})
}

// setupOpsErrorLogTestQueue replaces the shared fixture and installs a manually
// consumed channel of the supplied size. It consumes the worker-start barrier
// first so Enqueue cannot replace that channel or launch workers.
func setupOpsErrorLogTestQueue(t *testing.T, size int) {
	resetOpsErrorLoggerStateForTest(t)
	// Consume lazy setup before publishing a manual channel, letting test code
	// receive jobs directly without a background worker competing.
	testErrorQueue.opsErrorLogOnce.Do(func() {})
	testErrorQueue.opsErrorLogQueue = make(chan opsErrorLogJob, size)
}

// TestErrorQueueConstructAndStopDoNotStartWorkers counts sizing callback calls
// around construction, shutdown, and a later submission. It checks queue
// capacity and enqueue count remain zero while lazy worker setup is never
// invoked.
func TestErrorQueueConstructAndStopDoNotStartWorkers(t *testing.T) {
	starts := 0
	q := NewErrorLogQueue(ErrorLogQueueOptions{Processors: func() int { starts++; return 2 }})
	require.Zero(t, starts)
	require.Zero(t, q.Health().Capacity)
	require.NoError(t, q.Shutdown(context.Background()))
	q.Enqueue(NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil), &OpsInsertErrorLogInput{ErrorMessage: "after stop"})
	require.Zero(t, starts)
	require.Zero(t, q.Health().Enqueued)
}
