//go:build integration

package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/search"
	"github.com/TokenFlux/TokenRouter/internal/search/rediscache"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// A real Redis counter can expose reservation before the supplier HTTP response
// finishes. These integration cases connect Manager selection with local Brave
// exchanges, retaining successful increments and restoring failed or canceled
// attempts before a later shared-work stop completes.
//
// The canceled server signals entry and waits for a test release, allowing
// usage to be observed before request cancellation. The test joins the search
// result before releasing the server handler and checking that no proxy
// classification or second supplier attempt occurred. A separate counter fixture checks
// missing-TTL repair and administrator reset. Redis commands use the
// constructed client's timeout policy; these cases do not stall socket I/O past
// the cleanup context deadline.

// countingExecutor records real HTTP attempts and connectivity classification
// calls for cancellation assertions.
type countingExecutor struct {
	// Executor handles exchanges while calls and classifications count wrapper
	// invocations atomically. Tests sample both counters after joining the Manager
	// result to assess cancellation ordering.
	*Executor                           // real supplier HTTP executor
	calls, classifications atomic.Int64 // calls: supplier attempt count; classifications: proxy heuristic invocation count
}

// Search atomically counts an attempted supplier exchange before delegating to
// the embedded executor. The wrapper uses the same context, settings, and
// request, returning its HTTP outcome unchanged.
func (e *countingExecutor) Search(ctx context.Context, cfg ProviderConfig, req SearchRequest) (*SearchResponse, error) {
	e.calls.Add(1)
	return e.Executor.Search(ctx, cfg, req)
}

// IsProxyError counts heuristic invocations before delegating the error
// classification to Executor. Atomic counters permit observation after
// concurrent search work joins, including the cancellation branch that bypasses
// this method.
func (e *countingExecutor) IsProxyError(err error) bool {
	e.classifications.Add(1)
	return e.Executor.IsProxyError(err)
}

// TestSearchRedisReservations checks successful, failed, and canceled local
// HTTP attempts against live Redis counters. It asserts retained or released
// usage, canceled-attempt ordering, missing-TTL repair, and zero usage after
// reset.
func TestSearchRedisReservations(t *testing.T) {
	setup, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	container, err := rediscontainer.Run(setup, "redis:8.4-alpine")
	// Run may return a container on startup failure. Register removal before the
	// error assertion; the client defer below runs before test cleanup.
	if container != nil {
		t.Cleanup(func() {
			// Setup or test cancellation must not cancel Docker removal. Start the
			// bounded cleanup context when this callback runs.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := container.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate Redis container: %v", err)
			}
		})
	}
	require.NoError(t, err)
	address, err := container.ConnectionString(setup)
	require.NoError(t, err)
	opts, err := redis.ParseURL(address)
	require.NoError(t, err)
	rdb := redis.NewClient(opts)
	// Close the Redis client after the scenarios finish, suppressing that deferred
	// close error while asserted command and cleanup outcomes remain the test
	// result.
	defer func() { _ = rdb.Close() }()
	for _, kind := range []string{"success", "failure", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			// Clear counters and markers between cases so each outcome starts with no
			// earlier reservation or failure marker influencing supplier selection.
			require.NoError(t, rdb.FlushDB(context.Background()).Err())
			entered := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Hold the canceled scenario's server response after signaling entry so the
				// test can observe reserved usage before canceling its HTTP request.
				if kind == "cancel" {
					close(entered)
					<-release
				}
				if kind == "failure" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				// Suppress fixture response-write errors, including the disconnected
				// canceled client; the scenario asserts Manager outcome and Redis counters
				// after the attempt.
				_, _ = w.Write([]byte(`{"web":{"results":[{"url":"https://fixture.invalid","title":"fixture","description":"fixture"}]}}`))
			}))
			defer server.Close()
			// Replace and restore the shared endpoint serially so local fixture
			// exchanges do not alter later Brave searches using the production address.
			old := *braveSearchURL
			request, err := http.NewRequest(http.MethodGet, server.URL, nil)
			require.NoError(t, err)
			*braveSearchURL = *request.URL
			defer func() { *braveSearchURL = old }()
			executor := &countingExecutor{Executor: NewExecutor()}
			// Inject the fixture client before the search goroutine starts, bypassing
			// live supplier transport while retaining executor request construction and
			// response handling.
			executor.clientCache[""] = server.Client()
			configs := []ProviderConfig{{Type: "brave", APIKey: "fixture", QuotaLimit: 10}}
			if kind == "cancel" {
				configs = append(configs, ProviderConfig{Type: "tavily", APIKey: "fixture"})
			}
			work := search.NewWorkGroup()
			manager := search.NewManager(configs, rediscache.New(rdb), executor, work)
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			done := make(chan error, 1)
			// Execute asynchronously so the canceled case can observe its active
			// reservation and cancel before the server response gate opens.
			go func() { _, _, err := manager.SearchWithBestProvider(ctx, SearchRequest{Query: "fixture"}); done <- err }()
			if kind == "cancel" {
				<-entered
				used, err := manager.GetUsage(context.Background(), "brave")
				require.NoError(t, err)
				require.Equal(t, int64(1), used)
				stop()
			}
			// Join the Manager before inspecting final usage and wrapper counters; its
			// result follows confirmed-reservation cleanup and registered operation
			// completion.
			err = <-done
			switch kind {
			case "cancel":
				close(release)
				require.ErrorIs(t, err, context.Canceled)
				require.Equal(t, int64(1), executor.calls.Load())
				require.Zero(t, executor.classifications.Load())
			case "failure":
				require.Error(t, err)
			default:
				require.NoError(t, err)
			}
			used, err := manager.GetUsage(context.Background(), "brave")
			require.NoError(t, err)
			if kind == "success" {
				require.Equal(t, int64(1), used)
			} else {
				require.Zero(t, used)
			}
			require.NoError(t, work.Stop(context.Background()))
			manager.Retire()
		})
	}
	t.Run("repair-missing-ttl-and-reset", func(t *testing.T) {
		require.NoError(t, rdb.FlushDB(context.Background()).Err())
		// Seed usage without expiration to exercise the Lua repair branch on an
		// existing count above zero instead of the initial-count branch.
		require.NoError(t, rdb.Set(context.Background(), "websearch:quota:brave", int64(2), 0).Err())
		state := rediscache.New(rdb)
		value, err := state.Increment(context.Background(), "brave", time.Hour)
		require.NoError(t, err)
		require.Equal(t, int64(3), value)
		ttl, err := rdb.TTL(context.Background(), "websearch:quota:brave").Result()
		require.NoError(t, err)
		require.Greater(t, ttl, 59*time.Minute)
		require.NoError(t, state.Reset(context.Background(), "brave"))
		value, err = state.Usage(context.Background(), "brave")
		require.NoError(t, err)
		require.Zero(t, value)
	})
}
