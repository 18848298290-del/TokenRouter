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

type taskActivityUseCases struct {
	batchhttp.UseCases
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func (s *taskActivityUseCases) Get(context.Context, batchimage.BatchImageOwner, string) (*batchimage.BatchImagePublicBatch, error) {
	s.calls.Add(1)
	close(s.entered)
	<-s.release
	return &batchimage.BatchImagePublicBatch{}, nil
}

// TestTaskHTTPActivityTimeoutRetainsStorage checks that a shutdown timeout leaves dependencies open while an HTTP call remains active.
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
	go func() {
		defer close(completed)
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/tasks/task", nil))
	}()
	<-calls.entered
	var workerStopped, redisClosed atomic.Bool
	manager.Register(lifecycle.Hook{Name: "task-worker", StopOrder: 20, Stop: func(context.Context) error { workerStopped.Store(true); return nil }})
	manager.Register(lifecycle.Hook{Name: "redis", StopOrder: 900, Stop: func(context.Context) error { redisClosed.Store(true); return nil }})
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
	close(calls.release)
	<-completed
	require.Equal(t, 200, response.Code)
	require.ErrorIs(t, manager.Stop(context.Background()), context.DeadlineExceeded)
}
