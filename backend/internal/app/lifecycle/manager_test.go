package lifecycle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Lifecycle ordering separates producer stops, consumer drains, and storage
// cleanup. The fixtures record start and stop callbacks, then block startup,
// stop, or reporting independently to check which hooks remain eligible and
// which later dependencies stay uncalled at a deadline.

// TestManagerOrdersStartAndDrain registers storage, producer, and consumer
// hooks with different start and stop levels. Repeated Start and Stop must
// produce one ordered event sequence ending with database cleanup.
func TestManagerOrdersStartAndDrain(t *testing.T) {
	m := New()
	// Keep stop levels distinct for callbacks writing this event slice. Each
	// level waits for its single callback before a later level appends another event.
	var events []string
	add := func(name string) { events = append(events, name) }
	m.Register(Hook{Name: "db", StopOrder: 100, Stop: func(context.Context) error { add("db-close"); return nil }})
	m.Register(Hook{
		Name: "producer", StartOrder: 20, StopOrder: 10,
		Start: func(context.Context) error { add("producer-start"); return nil },
		Stop:  func(context.Context) error { add("producer-stop"); return nil },
	})
	m.Register(Hook{
		Name: "consumer", StartOrder: 10, StopOrder: 20,
		Start: func(context.Context) error { add("consumer-start"); return nil },
		Stop:  func(context.Context) error { add("consumer-drain"); return nil },
	})
	require.Empty(t, events)
	require.NoError(t, m.Start(context.Background()))
	require.NoError(t, m.Start(context.Background()))
	require.NoError(t, m.Stop(context.Background()))
	require.NoError(t, m.Stop(context.Background()))
	require.Equal(t, []string{"consumer-start", "producer-start", "producer-stop", "consumer-drain", "db-close"}, events)
}

// TestManagerRollsBackPartialStartAndConstruction contrasts construction-only
// cleanup with a failing startup callback. Start must retain the failure cause;
// rollback must reverse acquired resources, include the attempted failing hook,
// and exclude work whose start was never attempted.
func TestManagerRollsBackPartialStartAndConstruction(t *testing.T) {
	for _, start := range []bool{false, true} {
		t.Run(map[bool]string{false: "construction", true: "startup"}[start], func(t *testing.T) {
			m := New()
			var stopped []string
			for i, name := range []string{"db", "redis"} {
				m.Register(Hook{Name: name, StartOrder: i, Stop: func(context.Context) error { stopped = append(stopped, name); return nil }})
			}
			failure := errors.New("partial start")
			m.Register(Hook{Name: "partial", StartOrder: 10, Start: func(context.Context) error { return failure }, Stop: func(context.Context) error { stopped = append(stopped, "partial"); return nil }})
			m.Register(Hook{Name: "never", StartOrder: 20, Start: func(context.Context) error { t.Error("unexpected start"); return nil }, Stop: func(context.Context) error { t.Error("unexpected stop"); return nil }})
			if start {
				require.ErrorIs(t, m.Start(context.Background()), failure)
			}
			require.NoError(t, m.Rollback(context.Background()))
			if start {
				require.Equal(t, []string{"partial", "redis", "db"}, stopped)
			} else {
				require.Equal(t, []string{"redis", "db"}, stopped)
			}
		})
	}
}

// TestManagerTimeoutKeepsDependenciesAndSharesResult blocks the first cleanup
// level beyond its deadline. The error must name both pending levels, later
// storage must stay uncalled, and concurrent repeat stops must share one
// result and one callback invocation.
func TestManagerTimeoutKeepsDependenciesAndSharesResult(t *testing.T) {
	m := New()
	blocked := make(chan struct{})
	// A timed-out stop callback can still be waiting in its goroutine. Release
	// that fixture at test cleanup so its buffered result send can finish.
	defer close(blocked)
	var calls atomic.Int32
	m.Register(Hook{Name: "blocked-drain", StopOrder: 10, Stop: func(context.Context) error { calls.Add(1); <-blocked; return nil }})
	m.Register(Hook{Name: "database", StopOrder: 20, Stop: func(context.Context) error { t.Error("database closed before drain"); return nil }})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := m.Stop(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, err.Error(), "blocked-drain")
	require.Contains(t, err.Error(), "database")
	var wg sync.WaitGroup
	// Concurrent retries must observe the already published stop error and
	// must not schedule another callback while the first blocked one remains active.
	for range 8 {
		wg.Go(func() { require.Equal(t, err, m.Stop(context.Background())) })
	}
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
}

// TestManagerStopWaitsForPartialStartup blocks a start callback while Stop
// reaches its deadline. Cleanup must return that timeout before the
// still-starting hook is stopped, and releasing startup must let its original
// Start return successfully.
func TestManagerStopWaitsForPartialStartup(t *testing.T) {
	m := New()
	entered := make(chan struct{})
	finish := make(chan struct{})
	startResult := make(chan error, 1)
	// Announce start entry and hold its return behind finish, keeping startDone
	// open while the cleanup caller exhausts its independent startup-wait budget.
	m.Register(Hook{Name: "starting", Start: func(context.Context) error { close(entered); <-finish; return nil }, Stop: func(context.Context) error { t.Error("still starting"); return nil }})
	// Capture startup entry before attempting Stop, making the startup-wait
	// deadline observable rather than allowing a stop to win admission first.
	go func() { startResult <- m.Start(context.Background()) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, m.Stop(ctx), context.DeadlineExceeded)
	close(finish)
	require.NoError(t, <-startResult)
}

// TestManagerReportingIsBounded blocks the cleanup reporter after a worker
// stop returns. The same stop deadline must cover that reporting wait and keep
// the later database hook uncalled, with both names retained in the error.
func TestManagerReportingIsBounded(t *testing.T) {
	release := make(chan struct{})
	// Reporter work may outlive the bounded manager wait. Release its fixture
	// during cleanup so the reporting goroutine can return after the timeout assertion.
	defer close(release)
	// Hold reporting after callback completion so the manager must spend its
	// remaining budget here before it can admit the later dependency level.
	m := New(func(string, string, error) { <-release })
	m.Register(Hook{Name: "worker", StopOrder: 1, Stop: func(context.Context) error { return nil }})
	m.Register(Hook{Name: "database", StopOrder: 2, Stop: func(context.Context) error { t.Error("报告阻塞后仍关闭依赖"); return nil }})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := m.Stop(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, err.Error(), "report worker")
	require.Contains(t, err.Error(), "database")
}
