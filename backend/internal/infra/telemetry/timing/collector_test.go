package timing

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// Synthetic intervals establish exact timing totals for overlapping
// dependencies and database or Redis work. Other cases exercise cache-status
// precedence, sanitized labels, bounded detail emission, and concurrent
// counts beyond stored-span capacity, separating additive operation counts
// from elapsed coverage.

// TestCollectorHeaderValueAggregatesIntervals supplies overlapping database,
// Redis, and dependency intervals within a fixed request window. It requires
// exact union durations, additive counts, application time, and sorted
// dependency details in the complete header.
func TestCollectorHeaderValueAggregatesIntervals(t *testing.T) {
	startedAt := time.Unix(100, 0)
	collector := New(startedAt)
	collector.Record(MetricDatabase, startedAt.Add(10*time.Millisecond), startedAt.Add(40*time.Millisecond), 2)
	collector.Record(MetricRedis, startedAt.Add(30*time.Millisecond), startedAt.Add(50*time.Millisecond), 3)
	collector.Record(dependencyMetricName("openai"), startedAt.Add(70*time.Millisecond), startedAt.Add(100*time.Millisecond), 1)
	collector.Record(dependencyMetricName("github"), startedAt.Add(60*time.Millisecond), startedAt.Add(90*time.Millisecond), 1)

	got := collector.HeaderValue(startedAt.Add(120*time.Millisecond), "miss")
	want := `total;dur=120.0, app;dur=40.0, db;dur=30.0;desc="queries=2", redis;dur=20.0;desc="commands=3", cache;desc="miss", deps;dur=40.0;desc="calls=2", dep_github;dur=30.0;desc="calls=1", dep_openai;dur=30.0;desc="calls=1"`
	if got != want {
		t.Fatalf("HeaderValue() = %q, want %q", got, want)
	}
}

// TestRecordIntervalDoesNotIncrementCount adds one counted database call and
// a separate blocking interval. It requires their combined duration with one
// query and subtraction of both spans from application elapsed time.
func TestRecordIntervalDoesNotIncrementCount(t *testing.T) {
	startedAt := time.Unix(200, 0)
	collector := New(startedAt)
	ctx := WithCollector(context.Background(), collector)

	Record(ctx, MetricDatabase, startedAt.Add(10*time.Millisecond), startedAt.Add(20*time.Millisecond), 1)
	RecordInterval(ctx, MetricDatabase, startedAt.Add(30*time.Millisecond), startedAt.Add(40*time.Millisecond))

	header := HeaderValue(ctx, startedAt.Add(100*time.Millisecond), "hit")
	if !strings.Contains(header, `db;dur=20.0;desc="queries=1"`) {
		t.Fatalf("header %q does not contain one query with both blocking intervals", header)
	}
	if !strings.Contains(header, "app;dur=80.0") {
		t.Fatalf("header %q does not subtract the interval union from app time", header)
	}
}

// TestCollectorCacheStatusFallback stores a normalized hit status and
// supplies an invalid header argument. It requires the stored hit to win,
// while another collector with no recognized status selects bypass.
func TestCollectorCacheStatusFallback(t *testing.T) {
	startedAt := time.Unix(300, 0)
	collector := New(startedAt)
	ctx := WithCollector(context.Background(), collector)

	SetCacheStatus(ctx, " HIT ")
	if got := HeaderValue(ctx, startedAt.Add(time.Millisecond), "invalid"); !strings.Contains(got, `cache;desc="hit"`) {
		t.Fatalf("HeaderValue() = %q, want stored cache hit", got)
	}

	other := New(startedAt)
	if got := other.HeaderValue(startedAt.Add(time.Millisecond), "invalid"); !strings.Contains(got, `cache;desc="bypass"`) {
		t.Fatalf("HeaderValue() = %q, want cache bypass", got)
	}
}

// TestCollectorSanitizesDependencyMetric records a module containing newlines
// and header-like punctuation. It requires sanitized dependency text and no
// injected newline or duration fragment in the emitted header.
func TestCollectorSanitizesDependencyMetric(t *testing.T) {
	startedAt := time.Unix(400, 0)
	collector := New(startedAt)
	ctx := WithCollector(context.Background(), collector)
	RecordDependency(ctx, "GitHub API\r\nInjected;dur=999", startedAt, startedAt.Add(time.Millisecond))

	header := HeaderValue(ctx, startedAt.Add(2*time.Millisecond), "bypass")
	if strings.ContainsAny(header, "\r\n") || strings.Contains(header, ";dur=999") {
		t.Fatalf("unsafe metric content reached header: %q", header)
	}
	if !strings.Contains(header, "dep_githubapiinjecteddur999;dur=1.0") {
		t.Fatalf("sanitized dependency metric missing from header: %q", header)
	}
}

// TestCollectorBoundsHeaderLength records three hundred distinct long
// dependency names. It requires a header within the detail budget while total
// timing and aggregate dependency duration remain present after details are
// omitted.
func TestCollectorBoundsHeaderLength(t *testing.T) {
	startedAt := time.Unix(500, 0)
	collector := New(startedAt)
	for i := range 300 {
		collector.Record(
			dependencyMetricName(fmt.Sprintf("module_%03d_with_a_deliberately_long_name", i)),
			startedAt,
			startedAt.Add(time.Millisecond),
			1,
		)
	}

	header := collector.HeaderValue(startedAt.Add(2*time.Millisecond), "bypass")
	if len(header) > maxHeaderLength {
		t.Fatalf("header length = %d, want <= %d", len(header), maxHeaderLength)
	}
	if !strings.Contains(header, "total;dur=2.0") || !strings.Contains(header, "deps;dur=1.0") {
		t.Fatalf("bounded header lost fixed metrics: %q", header)
	}
}

// TestCollectorConcurrentRecording records twenty-five hundred database
// operations across twenty-five workers, exceeding retained-span capacity. It
// requires the complete additive query count after all workers finish,
// protecting counts independently of stored duration evidence.
func TestCollectorConcurrentRecording(t *testing.T) {
	startedAt := time.Now()
	collector := New(startedAt)
	ctx := WithCollector(context.Background(), collector)

	// workers and recordsPerWorker create more observations than the stored-span
	// cap so the final assertion protects continued operation counting after truncation.
	const workers = 25
	const recordsPerWorker = 100
	// Wait for every writer before reading the final count, separating lost
	// concurrent updates from an incomplete snapshot taken while workers still run.
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for range recordsPerWorker {
				Record(ctx, MetricDatabase, startedAt, startedAt.Add(time.Microsecond), 1)
			}
		}()
	}
	wg.Wait()

	header := HeaderValue(ctx, startedAt.Add(time.Millisecond), "bypass")
	want := fmt.Sprintf(`queries=%d`, workers*recordsPerWorker)
	if !strings.Contains(header, want) {
		t.Fatalf("header %q does not contain %q", header, want)
	}
}

// TestContextHelpersHandleMissingCollector supplies a context with no timing
// collector to the helpers. It requires inactive detection and empty header
// text, protecting optional timing behavior for ordinary requests.
func TestContextHelpersHandleMissingCollector(t *testing.T) {
	if Active(context.Background()) {
		t.Fatal("context without collector reported active")
	}
	if got := HeaderValue(context.Background(), time.Now(), "hit"); got != "" {
		t.Fatalf("HeaderValue() = %q without collector, want empty", got)
	}
}
