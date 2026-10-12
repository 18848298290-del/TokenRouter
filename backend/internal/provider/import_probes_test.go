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

type importProbeLifecyclePort struct {
	calls                       atomic.Int32
	started, cancelled, release chan struct{}
	ignore                      bool
}

type grokImportProbeStub struct {
	mu           sync.Mutex
	calls        map[int64]int
	failures     map[int64]error
	active       int
	maxActive    int
	deadlineSeen bool
	block        <-chan struct{}
	started      chan int64
	done         chan int64
}

type grokImportProbeLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

type grokImportProbeSchedulerTestSnapshot struct {
	queued     int
	workers    int
	maxWorkers int
}

type grokImportProbeScheduler = GrokImportProbeScheduler

func (p *importProbeLifecyclePort) QueryQuota(ctx context.Context, _ int64) (*GrokImportProbeResult, error) {
	p.calls.Add(1)
	close(p.started)
	<-ctx.Done()
	close(p.cancelled)
	if p.ignore {
		<-p.release
	}
	return nil, ctx.Err()
}

// TestImportProbesStopCancelsPendingAndWaits checks that shutdown discards queued probes, waits for active
// work, and rejects later scheduling.
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

func TestImportProbesStopReportsNonCooperativeExecution(t *testing.T) {
	p := &importProbeLifecyclePort{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), ignore: true}
	queue := NewGrokImportProbeScheduler(GrokImportProbeOptions{Concurrency: 1})
	queue.Schedule(p, &ProviderSnapshot{ID: 1, Platform: PlatformGrok, Type: ProviderTypeOAuth})
	waitUsageSignal(t, p.started)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := queue.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, err.Error(), "provider import probes")
	close(p.release)
	require.Same(t, err, queue.StopContext(context.Background()))
}

func (b *grokImportProbeLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *grokImportProbeLogBuffer) contains(data []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.buffer.Bytes(), data)
}

func (b *grokImportProbeLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func newGrokImportProbeStub(buffer int) *grokImportProbeStub {
	return &grokImportProbeStub{
		calls:    make(map[int64]int),
		failures: make(map[int64]error),
		started:  make(chan int64, buffer),
		done:     make(chan int64, buffer),
	}
}

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

	s.started <- providerID
	var ctxErr error
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			ctxErr = ctx.Err()
		}
	}

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

func (s *grokImportProbeStub) snapshot() (map[int64]int, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := make(map[int64]int, len(s.calls))
	maps.Copy(calls, s.calls)
	return calls, s.maxActive, s.deadlineSeen
}

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

func newGrokOAuthImportProvider(id int64) *ProviderSnapshot {
	return &ProviderSnapshot{
		ID:       id,
		Platform: PlatformGrok,
		Type:     ProviderTypeOAuth,
	}
}

func awaitGrokProbeSignal(t *testing.T, signals <-chan int64) int64 {
	t.Helper()
	select {
	case id := <-signals:
		return id
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Grok import probe")
		return 0
	}
}

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

func TestGrokImportProbeSchedulerQueuesBatchWithoutPerTaskGoroutines(t *testing.T) {
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
	select {
	case id := <-prober.started:
		t.Fatalf("probe %d started before a concurrency slot was released", id)
	case <-time.After(75 * time.Millisecond):
	}
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

	close(release)
	require.Equal(t, int64(501), awaitGrokProbeSignal(t, prober.done))
	require.Equal(t, int64(502), awaitGrokProbeSignal(t, prober.done))
	calls, _, _ := prober.snapshot()
	require.Equal(t, 1, calls[501])
	require.Equal(t, 1, calls[502])
}

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

	close(release)
	for range grokImportProbeQueueLimit + 1 {
		awaitGrokProbeSignal(t, prober.done)
	}
}

func TestGrokImportProbeSchedulerTimeoutCancelsProbe(t *testing.T) {
	neverRelease := make(chan struct{})
	scheduler := newGrokImportProbeScheduler(1, 20*time.Millisecond)
	prober := newGrokImportProbeStub(1)
	prober.block = neverRelease

	scheduler.Schedule(prober, newGrokOAuthImportProvider(201))
	require.Equal(t, int64(201), awaitGrokProbeSignal(t, prober.done))

	calls, _, _ := prober.snapshot()
	require.Equal(t, 1, calls[201])
}

func TestGrokImportProbeSchedulerSkipsMissingServiceAndNonGrokProviders(t *testing.T) {
	scheduler := newGrokImportProbeScheduler(1, time.Second)
	prober := newGrokImportProbeStub(1)

	scheduler.Schedule(nil, newGrokOAuthImportProvider(301))
	scheduler.Schedule(prober, &ProviderSnapshot{ID: 302, Platform: PlatformOpenAI, Type: ProviderTypeOAuth})
	scheduler.Schedule(prober, &ProviderSnapshot{ID: 303, Platform: PlatformGrok, Type: ProviderTypeAPIKey})

	select {
	case id := <-prober.started:
		t.Fatalf("unexpected probe for provider %d", id)
	case <-time.After(50 * time.Millisecond):
	}
	calls, _, _ := prober.snapshot()
	require.Empty(t, calls)
}

func TestGrokImportProbeFailureLogDoesNotIncludeErrorMessage(t *testing.T) {
	var logs grokImportProbeLogBuffer
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

func newGrokImportProbeScheduler(concurrency int, timeout time.Duration) *GrokImportProbeScheduler {
	return NewGrokImportProbeScheduler(GrokImportProbeOptions{Concurrency: concurrency, Timeout: timeout, Debug: slog.Debug, Info: slog.Info, Warn: slog.Warn, Error: slog.Error})
}
