package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logevent"
)

// stringerValue supplies a defined string implementing fmt.Stringer for
// conversion fixtures.
type stringerValue string

// opsSystemLogBackoffEvent allocates a warning event with empty attributes for
// failure-traffic fixtures.
func opsSystemLogBackoffEvent() *logevent.LogEvent {
	return &logevent.LogEvent{
		Time:      time.Now().UTC(),
		Level:     "warn",
		Component: "app",
		Message:   "boom",
		Fields:    map[string]any{},
	}
}

// pumpOpsSystemLogEvents repeatedly submits fresh warning events with
// two-millisecond waits until test cleanup signals it. Cleanup joins the
// producer after closing its stop channel; the sink's own deferred Stop has a
// separate lifecycle.
//
// The producer can overlap the sink's deferred stop, which seals admission
// first in these fixtures when the test function returns. Closing the producer
// signal then waits for done, keeping the loop from submitting more events
// after cleanup returns.
func pumpOpsSystemLogEvents(t *testing.T, sink *OpsSystemLogSink) {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	// Keep fixture event generation alive until cleanup signals stop, then publish
	// producer completion through done so no generator retains the fixture after
	// joining.
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			sink.WriteLogEvent(opsSystemLogBackoffEvent())
			// Pace fresh fixture traffic; cleanup is observed at the next loop select
			// instead of interrupting this short wait itself.
			time.Sleep(2 * time.Millisecond)
		}
	}()
	// Stop and join the independent event producer after fixture assertions. The
	// sink's deferred Stop seals admission even if the producer has not yet
	// received this signal.
	t.Cleanup(func() {
		close(stop)
		<-done
	})
}

// waitForOpsSystemLogCondition polls a supplied observation until its
// elapsed-time deadline with five-millisecond waits. It returns a final
// condition read after timeout, so caller assertions decide failure while
// worker cancellation remains separately owned.
func waitForOpsSystemLogCondition(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		// Yield between observation checks so the independent sink worker can advance
		// without requiring fixture state changes on this goroutine.
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// TestOpsSystemLogSinkFlushBackoffFor supplies one-second base and eight-second
// maximum suppression bounds. It protects doubling/capping for the listed
// streaks, including a large streak that stops arithmetic at these small
// bounds.
func TestOpsSystemLogSinkFlushBackoffFor(t *testing.T) {
	sink := &OpsSystemLogSink{flushBackoff: time.Second, flushBackoffMax: 8 * time.Second}

	cases := []struct {
		name     string        // selected streak fixture label
		failures int           // supplied attempted failure streak
		want     time.Duration // expected selected suppression duration
	}{
		{"first_failure", 1, time.Second},
		{"second_failure", 2, 2 * time.Second},
		{"third_failure", 3, 4 * time.Second},
		{"fourth_failure", 4, 8 * time.Second},
		{"capped", 9, 8 * time.Second},
		{"large_streak_does_not_overflow", 1000, 8 * time.Second},
		{"zero_streak_uses_base", 0, time.Second},
		{"negative_streak_uses_base", -1, time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sink.flushBackoffFor(tc.failures); got != tc.want {
				t.Fatalf("flushBackoffFor(%d) = %s, want %s", tc.failures, got, tc.want)
			}
		})
	}
}

// TestOpsSystemLogSinkFlushBackoffForFallbacks supplies zero settings and an
// inverted base/maximum pair. It protects default delay selection and lifting
// the maximum to the configured base for those cases.
func TestOpsSystemLogSinkFlushBackoffForFallbacks(t *testing.T) {
	zero := &OpsSystemLogSink{}
	if got := zero.flushBackoffFor(1); got != defaultOpsSystemLogFlushBackoff {
		t.Fatalf("zero-value base = %s, want %s", got, defaultOpsSystemLogFlushBackoff)
	}
	if got := zero.flushBackoffFor(100); got != defaultOpsSystemLogFlushBackoffMax {
		t.Fatalf("zero-value cap = %s, want %s", got, defaultOpsSystemLogFlushBackoffMax)
	}

	inverted := &OpsSystemLogSink{flushBackoff: 5 * time.Second, flushBackoffMax: time.Second}
	if got := inverted.flushBackoffFor(3); got != 5*time.Second {
		t.Fatalf("inverted bounds = %s, want 5s", got)
	}
}

// TestOpsSystemLogSinkSuppressesRetriesDuringBackoff submits sustained warning
// traffic to a failing mock writer (#5265). It protects one attempted write
// during the observed suppression interval plus dropped and failed-entry
// health, using no real database pool.
func TestOpsSystemLogSinkSuppressesRetriesDuringBackoff(t *testing.T) {
	var calls int64
	repo := &opsRepoMock{
		BatchInsertSystemLogsFn: func(_ context.Context, _ []*OpsInsertSystemLogInput) (int64, error) {
			atomic.AddInt64(&calls, 1)
			return 0, errors.New("db unavailable")
		},
	}

	sink := NewOpsSystemLogSink(repo)
	sink.batchSize = 1
	sink.flushInterval = 5 * time.Millisecond
	sink.flushBackoff = 800 * time.Millisecond
	sink.flushBackoffMax = 800 * time.Millisecond
	sink.Start()
	// Join the started sink when the fixture returns or fatals. Its writer and
	// local suppression state finish before callback-owned test data goes out of
	// scope.
	defer sink.Stop()
	pumpOpsSystemLogEvents(t, sink)

	if !waitForOpsSystemLogCondition(t, 2*time.Second, func() bool { return atomic.LoadInt64(&calls) >= 1 }) {
		t.Fatalf("first flush never happened")
	}

	// The backoff window must suppress writes despite the 5 ms flush interval.
	time.Sleep(300 * time.Millisecond)
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("upstream calls during backoff = %d, want 1", got)
	}

	// Suppressed entries count as dropped; write_failed counts entries in attempted failed batches.
	health := sink.Health()
	if health.DroppedCount == 0 {
		t.Fatalf("dropped_count should grow while flushing is suppressed")
	}
	if health.WriteFailed == 0 || health.LastError == "" {
		t.Fatalf("failed flush should still surface in health: %+v", health)
	}
}

// TestOpsSystemLogSinkResumesAfterBackoffWindow uses repeated mock failures
// with a short fixed delay. It protects additional attempted writes after
// suppression expires while the warning producer continues submitting new
// events.
func TestOpsSystemLogSinkResumesAfterBackoffWindow(t *testing.T) {
	var calls int64
	repo := &opsRepoMock{
		BatchInsertSystemLogsFn: func(_ context.Context, _ []*OpsInsertSystemLogInput) (int64, error) {
			atomic.AddInt64(&calls, 1)
			return 0, errors.New("db unavailable")
		},
	}

	sink := NewOpsSystemLogSink(repo)
	sink.batchSize = 1
	sink.flushInterval = 5 * time.Millisecond
	sink.flushBackoff = 150 * time.Millisecond
	sink.flushBackoffMax = 150 * time.Millisecond
	sink.Start()
	// Join the started sink when the fixture returns or fatals. Its writer and
	// local suppression state finish before callback-owned test data goes out of
	// scope.
	defer sink.Stop()
	pumpOpsSystemLogEvents(t, sink)

	if !waitForOpsSystemLogCondition(t, 3*time.Second, func() bool { return atomic.LoadInt64(&calls) >= 3 }) {
		t.Fatalf("sink did not resume flushing after backoff, calls=%d", atomic.LoadInt64(&calls))
	}
}

// TestOpsSystemLogSinkSuccessClearsSuppression fails the first attempted write
// and succeeds afterward. It protects renewed frequent writes and growing
// written-row health after recovery rather than retaining the prior delay.
func TestOpsSystemLogSinkSuccessClearsSuppression(t *testing.T) {
	var calls int64
	repo := &opsRepoMock{
		BatchInsertSystemLogsFn: func(_ context.Context, inputs []*OpsInsertSystemLogInput) (int64, error) {
			if atomic.AddInt64(&calls, 1) == 1 {
				return 0, errors.New("db unavailable")
			}
			return int64(len(inputs)), nil
		},
	}

	sink := NewOpsSystemLogSink(repo)
	sink.batchSize = 1
	sink.flushInterval = 5 * time.Millisecond
	sink.flushBackoff = 150 * time.Millisecond
	sink.flushBackoffMax = 150 * time.Millisecond
	sink.Start()
	// Join the started sink when the fixture returns or fatals. Its writer and
	// local suppression state finish before callback-owned test data goes out of
	// scope.
	defer sink.Stop()
	pumpOpsSystemLogEvents(t, sink)

	// After the second call succeeds, subsequent writes should proceed without backoff.
	if !waitForOpsSystemLogCondition(t, 3*time.Second, func() bool { return atomic.LoadInt64(&calls) >= 2 }) {
		t.Fatalf("sink never retried after the first failure")
	}
	recovered := atomic.LoadInt64(&calls)
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt64(&calls) - recovered; got < 3 {
		t.Fatalf("calls after recovery = %d in 200ms, want >=3 (suppression not cleared)", got)
	}
	if sink.Health().WrittenCount == 0 {
		t.Fatalf("written_count should grow after recovery")
	}
}

// TestOpsSystemLogSinkHealthyPathNeverSuppressed returns success for sustained
// warning batches with an intentionally long configured backoff. It protects
// ongoing write attempts and zero dropped count under this healthy fixture.
func TestOpsSystemLogSinkHealthyPathNeverSuppressed(t *testing.T) {
	var calls int64
	repo := &opsRepoMock{
		BatchInsertSystemLogsFn: func(_ context.Context, inputs []*OpsInsertSystemLogInput) (int64, error) {
			atomic.AddInt64(&calls, 1)
			return int64(len(inputs)), nil
		},
	}

	sink := NewOpsSystemLogSink(repo)
	sink.batchSize = 1
	sink.flushInterval = 5 * time.Millisecond
	sink.flushBackoff = time.Hour
	sink.flushBackoffMax = time.Hour
	sink.Start()
	// Join the started sink when the fixture returns or fatals. Its writer and
	// local suppression state finish before callback-owned test data goes out of
	// scope.
	defer sink.Stop()
	pumpOpsSystemLogEvents(t, sink)

	if !waitForOpsSystemLogCondition(t, 3*time.Second, func() bool { return atomic.LoadInt64(&calls) >= 10 }) {
		t.Fatalf("healthy sink should keep flushing, calls=%d", atomic.LoadInt64(&calls))
	}
	if got := sink.Health().DroppedCount; got != 0 {
		t.Fatalf("healthy sink dropped_count = %d, want 0", got)
	}
}

// TestOpsSystemLogSink_ShouldIndex supplies selected levels, access/audit
// component forms, and a true skip marker. It protects the listed eligibility
// outcomes, including skip precedence for the provided access event.
func TestOpsSystemLogSink_ShouldIndex(t *testing.T) {
	sink := &OpsSystemLogSink{}

	cases := []struct {
		name  string             // selected eligibility fixture label
		event *logevent.LogEvent // supplied stable event observation
		want  bool               // expected event indexing decision
	}{
		{
			name:  "warn level",
			event: &logevent.LogEvent{Level: "warn", Component: "app"},
			want:  true,
		},
		{
			name:  "error level",
			event: &logevent.LogEvent{Level: "error", Component: "app"},
			want:  true,
		},
		{
			name:  "access component",
			event: &logevent.LogEvent{Level: "info", Component: "http.access"},
			want:  true,
		},
		{
			name: "rejected access excluded from database sink",
			event: &logevent.LogEvent{
				Level:     "info",
				Component: "http.access",
				Fields:    map[string]any{logevent.OpsSystemLogSkipField: true},
			},
			want: false,
		},
		{
			name: "access component from fields (real zap path)",
			event: &logevent.LogEvent{
				Level:     "info",
				Component: "",
				Fields:    map[string]any{"component": "http.access"},
			},
			want: true,
		},
		{
			name:  "audit component",
			event: &logevent.LogEvent{Level: "info", Component: "audit.log_config_change"},
			want:  true,
		},
		{
			name: "audit component from fields (real zap path)",
			event: &logevent.LogEvent{
				Level:     "info",
				Component: "",
				Fields:    map[string]any{"component": "audit.log_config_change"},
			},
			want: true,
		},
		{
			name:  "plain info",
			event: &logevent.LogEvent{Level: "info", Component: "app"},
			want:  false,
		},
	}

	for _, tc := range cases {
		if got := sink.shouldIndex(tc.event); got != tc.want {
			t.Fatalf("%s: shouldIndex()=%v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestOpsSystemLogSink_WriteLogEvent_ShouldDropWhenQueueFull submits two
// warning pointers to an unstarted one-slot queue. It protects one buffered
// event and one counted capacity rejection for direct admission.
func TestOpsSystemLogSink_WriteLogEvent_ShouldDropWhenQueueFull(t *testing.T) {
	sink := &OpsSystemLogSink{
		queue: make(chan *logevent.LogEvent, 1),
	}

	sink.WriteLogEvent(&logevent.LogEvent{Level: "warn", Component: "app"})
	sink.WriteLogEvent(&logevent.LogEvent{Level: "warn", Component: "app"})

	if got := len(sink.queue); got != 1 {
		t.Fatalf("queue len = %d, want 1", got)
	}
	if dropped := atomic.LoadUint64(&sink.droppedCount); dropped != 1 {
		t.Fatalf("droppedCount = %d, want 1", dropped)
	}
}

// TestOpsSystemLogSink_Health assigns independent fixture counters and two
// queued events without starting work. It protects sampled counts, capacity,
// error text, and successful-duration division yielding one millisecond per
// reported row.
func TestOpsSystemLogSink_Health(t *testing.T) {
	sink := &OpsSystemLogSink{
		queue: make(chan *logevent.LogEvent, 10),
	}
	sink.lastError.Store("db timeout")
	atomic.StoreUint64(&sink.droppedCount, 3)
	atomic.StoreUint64(&sink.writeFailed, 2)
	atomic.StoreUint64(&sink.writtenCount, 5)
	// Five million successful-flush nanoseconds across five reported rows yields
	// one integer millisecond per row in the sampled health calculation.
	atomic.StoreUint64(&sink.totalDelayNs, uint64(5000000))
	sink.queue <- &logevent.LogEvent{Level: "warn", Component: "app"}
	sink.queue <- &logevent.LogEvent{Level: "warn", Component: "app"}

	health := sink.Health()
	if health.QueueDepth != 2 {
		t.Fatalf("queue depth = %d, want 2", health.QueueDepth)
	}
	if health.QueueCapacity != 10 {
		t.Fatalf("queue capacity = %d, want 10", health.QueueCapacity)
	}
	if health.DroppedCount != 3 {
		t.Fatalf("dropped = %d, want 3", health.DroppedCount)
	}
	if health.WriteFailed != 2 {
		t.Fatalf("write failed = %d, want 2", health.WriteFailed)
	}
	if health.WrittenCount != 5 {
		t.Fatalf("written = %d, want 5", health.WrittenCount)
	}
	if health.AvgWriteDelayMs != 1 {
		t.Fatalf("avg delay ms = %d, want 1", health.AvgWriteDelayMs)
	}
	if health.LastError != "db timeout" {
		t.Fatalf("last error = %q, want db timeout", health.LastError)
	}
}

// TestOpsSystemLogSink_StartStopAndFlushSuccess submits one attributed warning
// and captures prepared rows through a mock writer. It protects
// host/correlation/scalar IDs, a nonempty message, and eventual written count;
// no credential-removal assertion is made.
func TestOpsSystemLogSink_StartStopAndFlushSuccess(t *testing.T) {
	done := make(chan struct{}, 1)
	var captured []*OpsInsertSystemLogInput
	repo := &opsRepoMock{
		BatchInsertSystemLogsFn: func(_ context.Context, inputs []*OpsInsertSystemLogInput) (int64, error) {
			captured = append(captured, inputs...)
			select {
			case done <- struct{}{}:
			default:
			}
			return int64(len(inputs)), nil
		},
	}

	sink := NewOpsSystemLogSink(repo)
	sink.host = "api-node-1"
	sink.batchSize = 1
	sink.flushInterval = 10 * time.Millisecond
	sink.Start()
	// Join the started sink when the fixture returns or fatals. Its writer and
	// local suppression state finish before callback-owned test data goes out of
	// scope.
	defer sink.Stop()

	sink.WriteLogEvent(&logevent.LogEvent{
		Time:      time.Now().UTC(),
		Level:     "warn",
		Component: "http.access",
		Message:   `authorization="Bearer sk-test-123"`,
		Fields: map[string]any{
			"component":         "http.access",
			"request_id":        "req-1",
			"client_request_id": "creq-1",
			"user_id":           "12",
			"api_key_id":        json.Number("56"),
			"provider_id":       json.Number("34"),
			"platform":          "openai",
			"model":             "gpt-5",
		},
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for sink flush")
	}

	if len(captured) != 1 {
		t.Fatalf("captured len = %d, want 1", len(captured))
	}
	item := captured[0]
	if item.Host != "api-node-1" {
		t.Fatalf("host = %q, want api-node-1", item.Host)
	}
	if item.RequestID != "req-1" || item.ClientRequestID != "creq-1" {
		t.Fatalf("unexpected request ids: %+v", item)
	}
	if item.UserID == nil || *item.UserID != 12 {
		t.Fatalf("unexpected user_id: %+v", item.UserID)
	}
	if item.APIKeyID == nil || *item.APIKeyID != 56 {
		t.Fatalf("unexpected api_key_id: %+v", item.APIKeyID)
	}
	if item.ProviderID == nil || *item.ProviderID != 34 {
		t.Fatalf("unexpected provider_id: %+v", item.ProviderID)
	}
	if strings.TrimSpace(item.Message) == "" {
		t.Fatalf("message should not be empty")
	}
	// writtenCount is incremented after BatchInsertSystemLogsFn returns,
	// so poll briefly to avoid a race between the done signal and the atomic add.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if sink.Health().WrittenCount > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	health := sink.Health()
	if health.WrittenCount == 0 {
		t.Fatalf("written_count should be >0")
	}
}

// TestOpsSystemLogSink_FlushFailureUpdatesHealth injects one failed warning
// write and observes health. It protects a positive attempted-failure count and
// retained database-error diagnostic within the fixture wait.
func TestOpsSystemLogSink_FlushFailureUpdatesHealth(t *testing.T) {
	repo := &opsRepoMock{
		BatchInsertSystemLogsFn: func(_ context.Context, inputs []*OpsInsertSystemLogInput) (int64, error) {
			return 0, errors.New("db unavailable")
		},
	}
	sink := NewOpsSystemLogSink(repo)
	sink.batchSize = 1
	sink.flushInterval = 10 * time.Millisecond
	sink.Start()
	// Join the started sink when the fixture returns or fatals. Its writer and
	// local suppression state finish before callback-owned test data goes out of
	// scope.
	defer sink.Stop()

	sink.WriteLogEvent(&logevent.LogEvent{
		Time:      time.Now().UTC(),
		Level:     "warn",
		Component: "app",
		Message:   "boom",
		Fields:    map[string]any{},
	})

	// Poll separate health loads until a failed-attempt count is visible; the
	// following diagnostic assertion uses that sampled response.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		health := sink.Health()
		if health.WriteFailed > 0 {
			if !strings.Contains(health.LastError, "db unavailable") {
				t.Fatalf("unexpected last error: %s", health.LastError)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("write_failed_count not updated")
}

// TestOpsSystemLogSink_StopFlushUsesActiveContextAndDrainsQueue leaves one
// warning below a large batch size and stops before the periodic tick. It
// protects one reported write using a live context and no canceled-context
// callback calls.
func TestOpsSystemLogSink_StopFlushUsesActiveContextAndDrainsQueue(t *testing.T) {
	var inserted int64
	var canceledCtxCalls int64
	repo := &opsRepoMock{
		BatchInsertSystemLogsFn: func(ctx context.Context, inputs []*OpsInsertSystemLogInput) (int64, error) {
			if err := ctx.Err(); err != nil {
				atomic.AddInt64(&canceledCtxCalls, 1)
				return 0, err
			}
			atomic.AddInt64(&inserted, int64(len(inputs)))
			return int64(len(inputs)), nil
		},
	}

	sink := NewOpsSystemLogSink(repo)
	sink.batchSize = 200
	sink.flushInterval = time.Hour
	sink.Start()

	sink.WriteLogEvent(&logevent.LogEvent{
		Time:      time.Now().UTC(),
		Level:     "warn",
		Component: "app",
		Message:   "pending-on-shutdown",
		Fields:    map[string]any{"component": "http.access"},
	})

	sink.Stop()

	if got := atomic.LoadInt64(&inserted); got != 1 {
		t.Fatalf("inserted = %d, want 1", got)
	}
	if got := atomic.LoadInt64(&canceledCtxCalls); got != 0 {
		t.Fatalf("canceled ctx calls = %d, want 0", got)
	}
	health := sink.Health()
	if health.WrittenCount != 1 {
		t.Fatalf("written_count = %d, want 1", health.WrittenCount)
	}
}

// String returns the defined stringer's underlying text for conversion testing.
func (s stringerValue) String() string { return string(s) }

// TestOpsSystemLogSink_HelperFunctions supplies scalar map copies, Stringer
// values, and several identity representations. It protects top-level copy
// independence, selected string conversion, and the listed
// positive-versus-invalid numeric cases.
func TestOpsSystemLogSink_HelperFunctions(t *testing.T) {
	src := map[string]any{"a": 1}
	cloned := CopyMap(src)
	src["a"] = 2
	v, ok := cloned["a"].(int)
	if !ok || v != 1 {
		t.Fatalf("copyMap should create copy")
	}
	if got := AsString(stringerValue(" hello ")); got != "hello" {
		t.Fatalf("asString stringer = %q", got)
	}
	if got := AsString(fmt.Errorf("x")); got != "" {
		t.Fatalf("asString error should be empty, got %q", got)
	}
	if got := AsString(123); got != "" {
		t.Fatalf("asString non-string should be empty, got %q", got)
	}

	cases := []struct {
		in   any   // supplied scalar identity representation
		want int64 // expected positive identity value
		ok   bool  // expected identity pointer presence
	}{
		{in: 5, want: 5, ok: true},
		{in: int64(6), want: 6, ok: true},
		{in: float64(7), want: 7, ok: true},
		{in: json.Number("8"), want: 8, ok: true},
		{in: "9", want: 9, ok: true},
		{in: "0", ok: false},
		{in: -1, ok: false},
		{in: "abc", ok: false},
	}
	for _, tc := range cases {
		got := AsInt64Ptr(tc.in)
		if tc.ok {
			if got == nil || *got != tc.want {
				t.Fatalf("AsInt64Ptr(%v) = %+v, want %d", tc.in, got, tc.want)
			}
		} else if got != nil {
			t.Fatalf("AsInt64Ptr(%v) should be nil, got %d", tc.in, *got)
		}
	}
}

// TestNormalizeSystemLogHost supplies trimmed, empty, errored, and oversized
// Unicode hostnames. It protects unknown selection and the maximum rune count
// on the provided truncation path.
func TestNormalizeSystemLogHost(t *testing.T) {
	if got := normalizeSystemLogHost(" api-node-1 ", nil); got != "api-node-1" {
		t.Fatalf("trimmed host = %q, want api-node-1", got)
	}
	if got := normalizeSystemLogHost("", nil); got != "unknown" {
		t.Fatalf("empty host = %q, want unknown", got)
	}
	if got := normalizeSystemLogHost("api-node-1", errors.New("hostname unavailable")); got != "unknown" {
		t.Fatalf("errored host = %q, want unknown", got)
	}
	longHost := strings.Repeat("节", maxSystemLogHostLength+1)
	got := normalizeSystemLogHost(longHost, nil)
	if runeCount := len([]rune(got)); runeCount != maxSystemLogHostLength {
		t.Fatalf("truncated host rune count = %d, want %d", runeCount, maxSystemLogHostLength)
	}
}

// newTestSystemLogSink delegates construction with an empty options value,
// preserving standard sink defaults for fixtures.
func newTestSystemLogSink(repo SystemLogWriter) *OpsSystemLogSink {
	return NewOpsSystemLogSink(repo, SystemLogSinkOptions{})
}

// TestRegressionSystemLogDuplicateStart calls Start twice and blocks the first
// repository batch while more events are buffered. It protects no second
// concurrent writer entry during the observation interval and joins after
// release.
func TestRegressionSystemLogDuplicateStart(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	// Signal repository entry, then hold that writer until release. A second entry
	// while it remains blocked exposes a separately running duplicate writer.
	r := &opsRepoMock{BatchInsertSystemLogsFn: func(_ context.Context, in []*OpsInsertSystemLogInput) (int64, error) {
		entered <- struct{}{}
		<-release
		return int64(len(in)), nil
	}}
	s := NewOpsSystemLogSink(r)
	s.Start()
	s.Start()
	for range 400 {
		s.WriteLogEvent(&logevent.LogEvent{Level: "error", Message: "lifecycle-test"})
	}
	// Wait for the first writer before observing duplicate entry; this timeout
	// starts after initial admission and does not bound that first wait.
	<-entered
	select {
	case <-entered:
		t.Error("duplicate Start created two concurrent system-log writers with separate backoff state")
	case <-time.After(100 * time.Millisecond):
	}
	// Release the blocked write after checking duplicate startup, then join the
	// worker so queued batches can finish before the fixture returns.
	close(release)
	s.Stop()
}
