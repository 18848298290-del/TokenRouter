package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	batchhttp "github.com/TokenFlux/TokenRouter/internal/batchimage/httpapi"
)

// A batch HTTP lookup can remain active after task admission closes. The
// fixture announces entry and blocks Get until the test releases it, so the
// manager's task barrier expires while later worker and Redis hooks remain
// uncalled. Those hooks record invocation with flags, allowing the test to
// observe which shutdown phases were reached.
//
// Once admission is sealed, another HTTP lookup returns TASKS_STOPPED before
// reaching the fake use case. Releasing the first lookup lets its response
// finish, but Manager retains the error from its first Stop attempt. The test
// checks both the completed response and that repeated Stop result.

// taskActivityUseCases holds one batch lookup open during task shutdown.
type taskActivityUseCases struct {
	// UseCases supplies unused interface methods through a nil embedded value;
	// this fixture implements Get and supports no other use-case calls.
	batchhttp.UseCases
	// entered and release synchronize the HTTP goroutine with the test: Get
	// closes entered before blocking, and the test closes release after its
	// timeout and rejection assertions.
	entered chan struct{} // lookup entry notification
	release chan struct{} // lookup completion permission
	// calls records Get invocations atomically while the first lookup is blocked,
	// letting the test detect any second call through sealed admission.
	calls atomic.Int64
}

// Get records a fake lookup, announces entry, and waits for release before
// returning an empty public batch. Each fixture serves one admitted request
// because Get closes entered on entry. The test waits for that announcement
// before stopping the manager, so shutdown encounters an unfinished handler.
// Get ignores its context: cancellation alone cannot finish the lookup, and
// the test must close release to let the HTTP goroutine return.
func (s *taskActivityUseCases) Get(context.Context, batchimage.BatchImageOwner, string) (*batchimage.BatchImagePublicBatch, error) {
	// Publish the invocation count before announcing entry, so assertions can
	// read it while this lookup remains blocked waiting for release.
	s.calls.Add(1)
	close(s.entered)
	<-s.release
	return &batchimage.BatchImagePublicBatch{}, nil
}

// TestTaskHTTPActivityTimeoutRetainsStorage holds a batch lookup across a task
// shutdown deadline. Worker and Redis hooks remain uncalled, later HTTP calls
// receive TASKS_STOPPED, and the first response completes after release while
// repeated manager stops retain the timeout error.
func TestTaskHTTPActivityTimeoutRetainsStorage(t *testing.T) {
	manager := lifecycle.New()
	activity := provideTaskActivity(manager)
	calls := &taskActivityUseCases{entered: make(chan struct{}), release: make(chan struct{})}
	h := batchhttp.NewBatchImageHandler(calls, nil, nil, batchhttp.AccessPorts{Key: func(*gin.Context) (*apikey.APIKey, bool) { return &apikey.APIKey{ID: 1, UserID: 2}, true }})
	h.BindActivity(activity.Enter)
	router := gin.New()
	router.GET("/tasks/:id", h.Get)
	response := httptest.NewRecorder()
	completed := make(chan struct{})
	// Wait for use-case entry after starting HTTP independently; this keeps the
	// shutdown deadline focused on an admitted request that cannot yet finish.
	go func() {
		defer close(completed)
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/tasks/task", nil))
	}()
	<-calls.entered
	// Hook flags expose whether later shutdown phases ran while task draining
	// was incomplete, with atomic access shared between hooks and assertions.
	var workerStopped, redisClosed atomic.Bool
	manager.Register(lifecycle.Hook{Name: "task-worker", StopOrder: 20, Stop: func(context.Context) error { workerStopped.Store(true); return nil }})
	manager.Register(lifecycle.Hook{Name: "redis", StopOrder: 900, Stop: func(context.Context) error { redisClosed.Store(true); return nil }})
	// The short stop budget expires while the fixture holds Get open.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := manager.Stop(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "TaskRequestsAndDownloads")
	require.False(t, workerStopped.Load())
	require.False(t, redisClosed.Load())
	rejected := httptest.NewRecorder()
	router.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, "/tasks/next", nil))
	require.Equal(t, 503, rejected.Code)
	require.Contains(t, rejected.Body.String(), "TASKS_STOPPED")
	require.Equal(t, int64(1), calls.calls.Load())
	// Release the blocked lookup, then wait for ServeHTTP to finish before
	// reading its recorder; use-case return precedes response writing and activity release.
	close(calls.release)
	<-completed
	require.Equal(t, 200, response.Code)
	require.ErrorIs(t, manager.Stop(context.Background()), context.DeadlineExceeded)
}
