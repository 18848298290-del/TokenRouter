package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/idempotency"
	idempotencytest "github.com/TokenFlux/TokenRouter/internal/idempotency/testkit"
)

// storeUnavailableRepoStub fails every persistence operation for administrator outage scenarios.
type storeUnavailableRepoStub struct{}

// CreateProcessing injects a store outage before administrator callback execution.
func (storeUnavailableRepoStub) CreateProcessing(context.Context, *idempotency.IdempotencyRecord) (bool, error) {
	return false, errors.New("store unavailable")
}

// GetByScopeAndKeyHash injects a store outage during duplicate record lookup.
func (storeUnavailableRepoStub) GetByScopeAndKeyHash(context.Context, string, string) (*idempotency.IdempotencyRecord, error) {
	return nil, errors.New("store unavailable")
}

// TryReclaim injects a store outage while claiming an existing retryable record.
func (storeUnavailableRepoStub) TryReclaim(context.Context, int64, string, time.Time, time.Time, time.Time) (bool, error) {
	return false, errors.New("store unavailable")
}

// ExtendProcessingLock injects a store outage during an attempted lock extension.
func (storeUnavailableRepoStub) ExtendProcessingLock(context.Context, int64, string, time.Time, time.Time) (bool, error) {
	return false, errors.New("store unavailable")
}

// MarkSucceeded injects a store outage while storing a successful callback result.
func (storeUnavailableRepoStub) MarkSucceeded(context.Context, int64, int, string, time.Time) error {
	return errors.New("store unavailable")
}

// MarkFailedRetryable injects a store outage while storing the callback's retry state.
func (storeUnavailableRepoStub) MarkFailedRetryable(context.Context, int64, string, time.Time, time.Time) error {
	return errors.New("store unavailable")
}

// DeleteExpired injects a store outage if a scenario attempts expiry cleanup.
func (storeUnavailableRepoStub) DeleteExpired(context.Context, time.Time, int) (int64, error) {
	return 0, errors.New("store unavailable")
}

// TestExecuteAdminIdempotentJSONFailCloseOnStoreUnavailable fails initial claim
// storage before business execution. The default administrator helper must return
// service unavailable and leave its callback invocation count at zero.
func TestExecuteAdminIdempotentJSONFailCloseOnStoreUnavailable(t *testing.T) {
	executor := Executor{coordinator: idempotency.NewIdempotencyCoordinator(storeUnavailableRepoStub{}, idempotency.DefaultIdempotencyConfig())}

	var executed int
	router := gin.New()
	router.POST("/idempotent", func(c *gin.Context) {
		executor.ExecuteAdminIdempotentJSON(c, "admin.test.high", map[string]any{"a": 1}, time.Minute, func(ctx context.Context) (any, error) {
			executed++
			return gin.H{"ok": true}, nil
		})
	})

	req := httptest.NewRequest(http.MethodPost, "/idempotent", bytes.NewBufferString(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-key-1")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, 0, executed, "fail-close should block business execution when idempotency store is unavailable")
}

// TestExecuteAdminIdempotentJSONFailOpenOnStoreUnavailable opts an administrator
// command into direct execution after initial claim storage fails. It must invoke
// the callback once and return success with the store-unavailable degraded marker.
func TestExecuteAdminIdempotentJSONFailOpenOnStoreUnavailable(t *testing.T) {
	executor := Executor{coordinator: idempotency.NewIdempotencyCoordinator(storeUnavailableRepoStub{}, idempotency.DefaultIdempotencyConfig())}

	var executed int
	router := gin.New()
	router.POST("/idempotent", func(c *gin.Context) {
		executor.ExecuteAdminIdempotentJSONWithMode(c, "admin.test.medium", map[string]any{"a": 1}, time.Minute, IdempotencyStoreUnavailableFailOpen, func(ctx context.Context) (any, error) {
			executed++
			return gin.H{"ok": true}, nil
		})
	})

	req := httptest.NewRequest(http.MethodPost, "/idempotent", bytes.NewBufferString(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-key-2")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "store-unavailable", rec.Header().Get("X-Idempotency-Degraded"))
	require.Equal(t, 1, executed, "fail-open strategy should allow semantic idempotent path to continue")
}

// TestExecuteAdminIdempotentJSONConcurrentRetryOnlyOneSideEffect issues two same-key
// administrator requests around a slow callback. This scenario must invoke that
// callback once, and a later retry must return a stored replay with its header.
func TestExecuteAdminIdempotentJSONConcurrentRetryOnlyOneSideEffect(t *testing.T) {
	repo := idempotencytest.NewMemoryStore()
	cfg := idempotency.DefaultIdempotencyConfig()
	cfg.ProcessingTimeout = 2 * time.Second
	executor := Executor{coordinator: idempotency.NewIdempotencyCoordinator(repo, cfg)}

	var executed atomic.Int32
	router := gin.New()
	router.POST("/idempotent", func(c *gin.Context) {
		executor.ExecuteAdminIdempotentJSON(c, "admin.test.concurrent", map[string]any{"a": 1}, time.Minute, func(ctx context.Context) (any, error) {
			executed.Add(1)
			// Keep the callback active briefly so the second goroutine can encounter its
			// processing record; a later arrival may instead observe the completed stored response.
			time.Sleep(120 * time.Millisecond)
			return gin.H{"ok": true}, nil
		})
	})

	call := func() (int, http.Header) {
		req := httptest.NewRequest(http.MethodPost, "/idempotent", bytes.NewBufferString(`{"a":1}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "same-key")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code, rec.Header()
	}

	var status1, status2 int
	var wg sync.WaitGroup
	// Launch both requests before waiting for status assertions, allowing their claims
	// to overlap while the shared atomic counter distinguishes callback work from replay delivery.
	wg.Add(2)
	go func() {
		defer wg.Done()
		status1, _ = call()
	}()
	go func() {
		defer wg.Done()
		status2, _ = call()
	}()
	wg.Wait()

	require.Contains(t, []int{http.StatusOK, http.StatusConflict}, status1)
	require.Contains(t, []int{http.StatusOK, http.StatusConflict}, status2)
	require.Equal(t, int32(1), executed.Load(), "same idempotency key should execute side-effect only once")

	status3, headers3 := call()
	require.Equal(t, http.StatusOK, status3)
	require.Equal(t, "true", headers3.Get("X-Idempotency-Replayed"))
	require.Equal(t, int32(1), executed.Load())
}
