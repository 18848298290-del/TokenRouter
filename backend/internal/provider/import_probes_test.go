package provider

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// importProbeLifecyclePort holds quota work across cancellation for scheduler
// shutdown tests.
type importProbeLifecyclePort struct {
	calls atomic.Int32 // atomic quota invocation count
	// started and cancelled close during the single QueryQuota invocation used by
	// each lifecycle test. release lets the deliberately delayed call finish after
	// the shutdown result is inspected.
	started, // quota execution start signal
	cancelled, // observed context cancellation signal
	release chan struct{} // delayed query release signal
	// ignore retains quota work after cancellation until release closes. False
	// returns the context error immediately after recording the cancellation
	// signal.
	ignore bool
}

// grokImportProbeStub records query deadlines, concurrency, and controlled
// completion for probes.
type grokImportProbeStub struct {
	// mu protects calls, failures, active, maxActive, and deadlineSeen during
	// concurrent probe execution. started and done report progress outside the
	// lock so blocked signal handling does not hold observation state.
	mu           sync.Mutex      // query observation state lock
	calls        map[int64]int   // per-provider quota query counts
	failures     map[int64]error // configured per-provider query errors
	active       int             // currently executing query count
	maxActive    int             // maximum observed query count
	deadlineSeen bool            // execution deadline observed flag
	// block delays QueryQuota after its start signal when configured. Tests set it
	// before scheduling and release it or allow cancellation.
	block   <-chan struct{}
	started chan int64 // buffered provider start signals
	done    chan int64 // buffered provider completion signals
}

// grokImportProbeLogBuffer synchronizes log writes and reads for asynchronous
// probe tests.
type grokImportProbeLogBuffer struct {
	// mu protects buffer while workers append logs and tests inspect them. Every
	// Write, contains, and String access holds this same mutex until its buffer
	// operation finishes.
	mu     sync.Mutex   // asynchronous log buffer lock
	buffer bytes.Buffer // retained probe log bytes
}

// grokImportProbeSchedulerTestSnapshot captures queue and worker counts under
// scheduler locking.
type grokImportProbeSchedulerTestSnapshot struct {
	queued     int // pending probe task count
	workers    int // current probe worker count
	maxWorkers int // maximum probe worker count
}

// grokImportProbeScheduler aliases the production scheduler for shared test
// helpers.
type grokImportProbeScheduler = GrokImportProbeScheduler

// QueryQuota signals execution, waits for context cancellation, and then
// signals that cancellation was observed. With ignore enabled it continues
// waiting for release, allowing shutdown to exhaust its budget while this
// operation remains active.
//
// The test schedules one invocation for the fixture, because started and
// cancelled are closed directly. Returning the context error keeps this
// observation a cancellation outcome after release rather than reporting a
// completed quota probe. Tests control release to clean up the deliberately
// noncooperative call.
func (p *importProbeLifecyclePort) QueryQuota(ctx context.Context, _ int64) (*GrokImportProbeResult, error) {
	p.calls.Add(1)
	// Signal the test before waiting for shutdown. Direct channel closure
	// relies on this lifecycle fixture receiving one call, as arranged by its
	// scheduling tests.
	close(p.started)
	<-ctx.Done()
	close(p.cancelled)
	// Keep the query alive after recording cancellation to reproduce an upstream
	// call that exceeds the shutdown budget until the test releases it.
	if p.ignore {
		<-p.release
	}
	return nil, ctx.Err()
}

// TestImportProbesStopCancelsPendingAndWaits queues a second provider behind an
// active probe before shutdown. Stop cancels active work, discards queued IDs,
// empties worker state, and rejects scheduling after the first successful stop.
func TestImportProbesStopCancelsPendingAndWaits(t *testing.T) {
	p := &importProbeLifecyclePort{started: make(chan struct{}), cancelled: make(chan struct{})}
	queue := NewGrokImportProbeScheduler(GrokImportProbeOptions{Concurrency: 1})
	require.Zero(t, queue.workers)
	require.Zero(t, p.calls.Load())
	value := ProviderSnapshot{ID: 1, Platform: PlatformGrok, Type: ProviderTypeOAuth}
	queue.Schedule(p, &value)
	waitUsageSignal(t, p.started)
	value.ID = 2
	queue.Schedule(p, &value)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, queue.StopContext(ctx))
	waitUsageSignal(t, p.cancelled)
	require.NoError(t, queue.StopContext(ctx))
	queue.Schedule(p, &value)
	require.Equal(t, int32(1), p.calls.Load())
	queue.mu.Lock()
	defer queue.mu.Unlock()
	require.Zero(t, queue.workers)
	require.Empty(t, queue.queue)
	require.Empty(t, queue.pending)
	require.Empty(t, queue.inFlight)
}

// TestImportProbesStopReportsNonCooperativeExecution holds a cancelled quota
// query beyond a short shutdown budget. Stop reports deadline exhaustion with
// its service label and retains that same first result after the query is
// released.
func TestImportProbesStopReportsNonCooperativeExecution(t *testing.T) {
	p := &importProbeLifecyclePort{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), ignore: true}
	queue := NewGrokImportProbeScheduler(GrokImportProbeOptions{Concurrency: 1})
	queue.Schedule(p, &ProviderSnapshot{ID: 1, Platform: PlatformGrok, Type: ProviderTypeOAuth})
	waitUsageSignal(t, p.started)
	// Expire shutdown before the test releases the deliberately noncooperative query.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := queue.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, err.Error(), "provider import probes")
	// The first shutdown result has been inspected. Release the delayed query
	// so the fixture can finish while the following Stop checks retained error
	// identity.
	close(p.release)
	require.Same(t, err, queue.StopContext(context.Background()))
}

// Write appends log bytes while holding the buffer mutex for asynchronous probe
// tests.
func (b *grokImportProbeLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

// contains checks recorded log bytes under the same mutex used by concurrent
// writers.
func (b *grokImportProbeLogBuffer) contains(data []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.buffer.Bytes(), data)
}

// String copies recorded log text while holding the buffer's asynchronous write
// mutex.
func (b *grokImportProbeLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// newGrokImportProbeStub allocates query counters and buffered start/done
// channels. Callers provide enough buffering or receivers for every signal so
// fixture reporting can complete without holding worker slots indefinitely.
func newGrokImportProbeStub(buffer int) *grokImportProbeStub {
	return &grokImportProbeStub{
		calls:    make(map[int64]int),
		failures: make(map[int64]error),
		started:  make(chan int64, buffer),
		done:     make(chan int64, buffer),
	}
}

// QueryQuota records one active query and its deadline, then emits start and
// completion signals. A configured blocking channel delays the query until
// release or context cancellation. Cancellation errors take precedence over the
// per-ID injected failure.
//
// Active counts and failure lookup are read under the fixture mutex, while
// channel waits occur outside it so other workers can progress. The caller
// configures controls before scheduling and provides buffering or receivers
// for every start and completion signal.
// Successful calls return a fixed minimal observation for scheduler log
// assertions.
func (s *grokImportProbeStub) QueryQuota(ctx context.Context, providerID int64) (*GrokImportProbeResult, error) {
	_, deadlineSeen := ctx.Deadline()
	s.mu.Lock()
	s.calls[providerID]++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.deadlineSeen = s.deadlineSeen || deadlineSeen
	s.mu.Unlock()

	// Publish query start after releasing the observation lock so other workers
	// can record their own progress while this call waits for the
	// test-controlled gate.
	s.started <- providerID
	var ctxErr error
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			ctxErr = ctx.Err()
		}
	}

	// Complete active-count bookkeeping before emitting done so the receiving
	// test can inspect finished counts without racing an unfinished fixture
	// state mutation.
	s.mu.Lock()
	s.active--
	failure := s.failures[providerID]
	s.mu.Unlock()
	s.done <- providerID
	if ctxErr != nil {
		return nil, ctxErr
	}
	if failure != nil {
		return nil, failure
	}
	return &GrokImportProbeResult{
		Model:      "grok-4.5",
		StatusCode: 200,
	}, nil
}

// snapshot copies call counts and reads maximum concurrency plus deadline
// observation under the fixture mutex. The returned count map can be changed
// independently of subsequent recorded probe calls.
func (s *grokImportProbeStub) snapshot() (map[int64]int, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := make(map[int64]int, len(s.calls))
	maps.Copy(calls, s.calls)
	return calls, s.maxActive, s.deadlineSeen
}

// snapshotGrokImportProbeScheduler reads pending queue and worker counts under
// the scheduler mutex. A nil scheduler returns zero counts, allowing
// eventual-completion assertions to inspect a consistent snapshot.
func snapshotGrokImportProbeScheduler(s *grokImportProbeScheduler) grokImportProbeSchedulerTestSnapshot {
	if s == nil {
		return grokImportProbeSchedulerTestSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return grokImportProbeSchedulerTestSnapshot{
		queued:     len(s.queue),
		workers:    s.workers,
		maxWorkers: s.maxWorkers,
	}
}

// newGrokOAuthImportProvider creates a Grok OAuth routing snapshot with the
// supplied fixture ID.
func newGrokOAuthImportProvider(id int64) *ProviderSnapshot {
	return &ProviderSnapshot{
		ID:       id,
		Platform: PlatformGrok,
		Type:     ProviderTypeOAuth,
	}
}

// awaitGrokProbeSignal waits up to one second for a provider ID from the signal
// channel. Missing progress fails the caller's test rather than leaving an
// asynchronous probe assertion blocked.
func awaitGrokProbeSignal(t *testing.T, signals <-chan int64) int64 {
	t.Helper()
	// Bound progress waits so stalled worker signals produce an actionable test
	// failure.
	select {
	case id := <-signals:
		return id
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Grok import probe")
		return 0
	}
}

// TestGrokImportProbeSchedulerProbesSingleProviderOnce schedules one imported
// OAuth provider. The fixture sees one quota call with a deadline, and eventual
// assertions wait until the queue and lazy worker count are empty.
func TestGrokImportProbeSchedulerProbesSingleProviderOnce(t *testing.T) {
	scheduler := newGrokImportProbeScheduler(1, time.Second)
	prober := newGrokImportProbeStub(1)

	scheduler.Schedule(prober, newGrokOAuthImportProvider(101))
	require.Equal(t, int64(101), awaitGrokProbeSignal(t, prober.done))

	calls, maxActive, deadlineSeen := prober.snapshot()
	require.Equal(t, map[int64]int{101: 1}, calls)
	require.Equal(t, 1, maxActive)
	require.True(t, deadlineSeen)
	require.Eventually(t, func() bool {
		snapshot := snapshotGrokImportProbeScheduler(scheduler)
		return snapshot.queued == 0 && snapshot.workers == 0
	}, time.Second, 10*time.Millisecond)
}

// TestGrokImportProbeSchedulerQueuesBatchWithoutPerTaskGoroutines queues fifty
// probes while three queries are blocked. Only three workers start, queued
// tasks remain bounded by those slots, and every admitted provider is
// eventually queried once.
func TestGrokImportProbeSchedulerQueuesBatchWithoutPerTaskGoroutines(t *testing.T) {
	// Queue enough tasks to exercise reuse of the three worker slots.
	const taskCount = 50
	release := make(chan struct{})
	scheduler := newGrokImportProbeScheduler(3, time.Second)
	prober := newGrokImportProbeStub(taskCount)
	prober.block = release
	prober.failures[150] = apperror.New(502, "GROK_TEST_PROBE_FAILED", "sensitive-upstream-body")

	for id := int64(101); id < 101+taskCount; id++ {
		scheduler.Schedule(prober, newGrokOAuthImportProvider(id))
	}
	for range 3 {
		awaitGrokProbeSignal(t, prober.started)
	}
	snapshot := snapshotGrokImportProbeScheduler(scheduler)
	require.Equal(t, taskCount-3, snapshot.queued)
	require.Equal(t, 3, snapshot.workers)
	require.Equal(t, 3, snapshot.maxWorkers)
	// Keep the initial slots occupied through this observation interval so
	// a fourth start would demonstrate admission beyond the configured worker ceiling.
	select {
	case id := <-prober.started:
		t.Fatalf("probe %d started before a concurrency slot was released", id)
	case <-time.After(75 * time.Millisecond):
	}
	// Release blocked queries after admission assertions so admitted work can
	// resume and the test can drain each query's completion signal.
	close(release)
	for range taskCount {
		awaitGrokProbeSignal(t, prober.done)
	}

	calls, maxActive, _ := prober.snapshot()
	require.Len(t, calls, taskCount)
	for id := int64(101); id < 101+taskCount; id++ {
		require.Equal(t, 1, calls[id])
	}
	require.Equal(t, 3, maxActive)
	require.Eventually(t, func() bool {
		snapshot = snapshotGrokImportProbeScheduler(scheduler)
		return snapshot.queued == 0 && snapshot.workers == 0
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, 3, snapshot.maxWorkers)
}

// TestGrokImportProbeSchedulerDeduplicatesPendingAndInFlightProviders resubmits
// both an executing and a queued provider. The queue retains one pending entry
// per ID, and release produces one quota call for each admitted provider.
func TestGrokImportProbeSchedulerDeduplicatesPendingAndInFlightProviders(t *testing.T) {
	scheduler := newGrokImportProbeScheduler(1, time.Second)
	prober := newGrokImportProbeStub(2)
	release := make(chan struct{})
	prober.block = release
	provider := newGrokOAuthImportProvider(501)
	queued := newGrokOAuthImportProvider(502)

	scheduler.Schedule(prober, provider)
	require.Equal(t, int64(501), awaitGrokProbeSignal(t, prober.started))
	scheduler.Schedule(prober, provider)
	scheduler.Schedule(prober, queued)
	scheduler.Schedule(prober, queued)

	scheduler.mu.Lock()
	require.Len(t, scheduler.queue, 1)
	require.Contains(t, scheduler.inFlight, int64(501))
	require.Contains(t, scheduler.pending, int64(502))
	scheduler.mu.Unlock()

	// Release blocked queries after admission assertions so admitted work can
	// resume and the test can drain each query's completion signal.
	close(release)
	require.Equal(t, int64(501), awaitGrokProbeSignal(t, prober.done))
	require.Equal(t, int64(502), awaitGrokProbeSignal(t, prober.done))
	calls, _, _ := prober.snapshot()
	require.Equal(t, 1, calls[501])
	require.Equal(t, 1, calls[502])
}

// TestGrokImportProbeSchedulerBoundsPendingQueue fills pending capacity behind
// one blocked query. The queued count stays at the configured limit, and
// admitted tasks finish after the active query is released.
func TestGrokImportProbeSchedulerBoundsPendingQueue(t *testing.T) {
	scheduler := newGrokImportProbeScheduler(1, time.Second)
	prober := newGrokImportProbeStub(grokImportProbeQueueLimit + 1)
	release := make(chan struct{})
	prober.block = release
	scheduler.Schedule(prober, newGrokOAuthImportProvider(600))
	require.Equal(t, int64(600), awaitGrokProbeSignal(t, prober.started))
	for id := int64(601); id < 601+grokImportProbeQueueLimit+10; id++ {
		scheduler.Schedule(prober, newGrokOAuthImportProvider(id))
	}

	scheduler.mu.Lock()
	require.Len(t, scheduler.queue, grokImportProbeQueueLimit)
	scheduler.mu.Unlock()

	// Release blocked queries after admission assertions so admitted work can
	// resume and the test can drain each query's completion signal.
	close(release)
	for range grokImportProbeQueueLimit + 1 {
		awaitGrokProbeSignal(t, prober.done)
	}
}

// TestGrokImportProbeSchedulerTimeoutCancelsProbe runs a query behind a gate
// that never opens. Its execution deadline releases the fixture through
// cancellation, and the provider receives one recorded quota call.
func TestGrokImportProbeSchedulerTimeoutCancelsProbe(t *testing.T) {
	neverRelease := make(chan struct{})
	// Give the never-opened gate a finite probe budget to observe cancellation.
	scheduler := newGrokImportProbeScheduler(1, 20*time.Millisecond)
	prober := newGrokImportProbeStub(1)
	prober.block = neverRelease

	scheduler.Schedule(prober, newGrokOAuthImportProvider(201))
	require.Equal(t, int64(201), awaitGrokProbeSignal(t, prober.done))

	calls, _, _ := prober.snapshot()
	require.Equal(t, 1, calls[201])
}

// TestGrokImportProbeSchedulerSkipsMissingServiceAndNonGrokProviders submits a
// nil service and unsupported platform/type snapshots. No start signal or
// recorded query appears during the bounded observation window for these
// ineligible inputs.
func TestGrokImportProbeSchedulerSkipsMissingServiceAndNonGrokProviders(t *testing.T) {
	scheduler := newGrokImportProbeScheduler(1, time.Second)
	prober := newGrokImportProbeStub(1)

	scheduler.Schedule(nil, newGrokOAuthImportProvider(301))
	scheduler.Schedule(prober, &ProviderSnapshot{ID: 302, Platform: PlatformOpenAI, Type: ProviderTypeOAuth})
	scheduler.Schedule(prober, &ProviderSnapshot{ID: 303, Platform: PlatformGrok, Type: ProviderTypeAPIKey})

	// Observe rejected inputs during a bounded interval while the recorded call
	// map separately confirms that no eligible query was admitted by this scenario.
	select {
	case id := <-prober.started:
		t.Fatalf("unexpected probe for provider %d", id)
	case <-time.After(50 * time.Millisecond):
	}
	calls, _, _ := prober.snapshot()
	require.Empty(t, calls)
}

// TestGrokImportProbeFailureLogDoesNotIncludeErrorMessage injects a failure
// containing a credential-like secret. The eventual failure log includes its
// structured reason while excluding the error message that carried the secret.
func TestGrokImportProbeFailureLogDoesNotIncludeErrorMessage(t *testing.T) {
	var logs grokImportProbeLogBuffer
	// The scheduler captures slog callbacks that consult the global logger.
	// Restore that logger after this scenario so later tests keep their
	// configured logging.
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	scheduler := newGrokImportProbeScheduler(1, time.Second)
	prober := newGrokImportProbeStub(1)
	prober.failures[401] = apperror.New(502, "GROK_TEST_PROBE_FAILED", "refresh-token-secret")
	scheduler.Schedule(prober, newGrokOAuthImportProvider(401))
	awaitGrokProbeSignal(t, prober.done)

	require.Eventually(t, func() bool {
		return logs.contains([]byte("grok_import_active_probe_failed"))
	}, time.Second, 10*time.Millisecond)
	require.Contains(t, logs.String(), "GROK_TEST_PROBE_FAILED")
	require.NotContains(t, logs.String(), "refresh-token-secret")
}

// newGrokImportProbeScheduler binds the selected worker limit and timeout to
// slog callbacks. It delegates default handling and lazy worker startup to the
// production scheduler used by these tests.
func newGrokImportProbeScheduler(concurrency int, timeout time.Duration) *GrokImportProbeScheduler {
	return NewGrokImportProbeScheduler(GrokImportProbeOptions{Concurrency: concurrency, Timeout: timeout, Debug: slog.Debug, Info: slog.Info, Warn: slog.Warn, Error: slog.Error})
}
