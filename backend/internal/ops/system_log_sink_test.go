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

type stringerValue string

func opsSystemLogBackoffEvent() *logevent.LogEvent {
	return &logevent.LogEvent{
		Time:      time.Now().UTC(),
		Level:     "warn",
		Component: "app",
		Message:   "boom",
		Fields:    map[string]any{},
	}
}

// pumpOpsSystemLogEvents keeps sending events to simulate WARN/ERROR traffic during a storage failure.
func pumpOpsSystemLogEvents(t *testing.T, sink *OpsSystemLogSink) {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			sink.WriteLogEvent(opsSystemLogBackoffEvent())
			time.Sleep(2 * time.Millisecond)
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-done
	})
}

func waitForOpsSystemLogCondition(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestOpsSystemLogSinkFlushBackoffFor(t *testing.T) {
	sink := &OpsSystemLogSink{flushBackoff: time.Second, flushBackoffMax: 8 * time.Second}

	cases := []struct {
		name     string
		failures int
		want     time.Duration
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

// TestOpsSystemLogSinkFlushBackoffForFallbacks checks default delays and a maximum below the base delay.
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

// TestOpsSystemLogSinkSuppressesRetriesDuringBackoff covers issue #5265: frequent retries after failed writes
// can exhaust a small connection pool when canceled PostgreSQL COPY operations discard connections.
// Backoff gives business queries a chance to acquire connections.
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

// TestOpsSystemLogSinkResumesAfterBackoffWindow checks that writes resume after the delay expires.
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
	defer sink.Stop()
	pumpOpsSystemLogEvents(t, sink)

	if !waitForOpsSystemLogCondition(t, 3*time.Second, func() bool { return atomic.LoadInt64(&calls) >= 3 }) {
		t.Fatalf("sink did not resume flushing after backoff, calls=%d", atomic.LoadInt64(&calls))
	}
}

// TestOpsSystemLogSinkSuccessClearsSuppression checks that success resets failures and the backoff window.
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

// TestOpsSystemLogSinkHealthyPathNeverSuppressed checks that successful writes never enter backoff.
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
	defer sink.Stop()
	pumpOpsSystemLogEvents(t, sink)

	if !waitForOpsSystemLogCondition(t, 3*time.Second, func() bool { return atomic.LoadInt64(&calls) >= 10 }) {
		t.Fatalf("healthy sink should keep flushing, calls=%d", atomic.LoadInt64(&calls))
	}
	if got := sink.Health().DroppedCount; got != 0 {
		t.Fatalf("healthy sink dropped_count = %d, want 0", got)
	}
}

func TestOpsSystemLogSink_ShouldIndex(t *testing.T) {
	sink := &OpsSystemLogSink{}

	cases := []struct {
		name  string
		event *logevent.LogEvent
		want  bool
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

func TestOpsSystemLogSink_Health(t *testing.T) {
	sink := &OpsSystemLogSink{
		queue: make(chan *logevent.LogEvent, 10),
	}
	sink.lastError.Store("db timeout")
	atomic.StoreUint64(&sink.droppedCount, 3)
	atomic.StoreUint64(&sink.writeFailed, 2)
	atomic.StoreUint64(&sink.writtenCount, 5)
	atomic.StoreUint64(&sink.totalDelayNs, uint64(5000000)) // 5ms total -> avg 1ms
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
	defer sink.Stop()

	sink.WriteLogEvent(&logevent.LogEvent{
		Time:      time.Now().UTC(),
		Level:     "warn",
		Component: "app",
		Message:   "boom",
		Fields:    map[string]any{},
	})

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

func (s stringerValue) String() string { return string(s) }

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
		in   any
		want int64
		ok   bool
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

func newTestSystemLogSink(repo SystemLogWriter) *OpsSystemLogSink {
	return NewOpsSystemLogSink(repo, SystemLogSinkOptions{})
}

func TestRegressionSystemLogDuplicateStart(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
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
	<-entered
	select {
	case <-entered:
		t.Error("duplicate Start created two concurrent system-log writers with separate backoff state")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	s.Stop()
}
