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
	"github.com/TokenFlux/TokenRouter/internal/idempotency/testkit"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/authctx"
)

// userStoreUnavailableRepoStub rejects every persistence operation for user command outage tests.
type userStoreUnavailableRepoStub struct{}

// CreateProcessing reports store unavailability before the user callback can begin.
func (userStoreUnavailableRepoStub) CreateProcessing(context.Context, *idempotency.IdempotencyRecord) (bool, error) {
	return false, errors.New("store unavailable")
}

// GetByScopeAndKeyHash reports store unavailability while retrieving an existing user claim.
func (userStoreUnavailableRepoStub) GetByScopeAndKeyHash(context.Context, string, string) (*idempotency.IdempotencyRecord, error) {
	return nil, errors.New("store unavailable")
}

// TryReclaim reports store unavailability when a user request tries reclaiming ownership.
func (userStoreUnavailableRepoStub) TryReclaim(context.Context, int64, string, time.Time, time.Time, time.Time) (bool, error) {
	return false, errors.New("store unavailable")
}

// ExtendProcessingLock reports store unavailability while updating a user processing deadline.
func (userStoreUnavailableRepoStub) ExtendProcessingLock(context.Context, int64, string, time.Time, time.Time) (bool, error) {
	return false, errors.New("store unavailable")
}

// MarkSucceeded reports store unavailability when recording a successful user result.
func (userStoreUnavailableRepoStub) MarkSucceeded(context.Context, int64, int, string, time.Time) error {
	return errors.New("store unavailable")
}

// MarkFailedRetryable reports store unavailability when recording a user callback failure.
func (userStoreUnavailableRepoStub) MarkFailedRetryable(context.Context, int64, string, time.Time, time.Time) error {
	return errors.New("store unavailable")
}

// DeleteExpired reports store unavailability if cleanup is called in these scenarios.
func (userStoreUnavailableRepoStub) DeleteExpired(context.Context, time.Time, int) (int64, error) {
	return 0, errors.New("store unavailable")
}

// withUserSubject supplies the requested user identity to a Gin test request.
// The middleware runs before the command helper so its fingerprint includes that
// actor without requiring the production authentication dependencies in this fixture.
func withUserSubject(userID int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(string(authctx.ContextKeyUser), authctx.AuthSubject{UserID: userID})
		c.Next()
	}
}

// TestExecuteUserIdempotentJSONFallbackWithoutCoordinator uses an HTTP fixture
// whose Executor has no binding and no key header. Direct execution must invoke
// its callback once and return the successful user response.
func TestExecuteUserIdempotentJSONFallbackWithoutCoordinator(t *testing.T) {
	executor := Executor{coordinator: nil}

	var executed int
	router := gin.New()
	router.Use(withUserSubject(1))
	router.POST("/idempotent", func(c *gin.Context) {
		executor.ExecuteUserIdempotentJSON(c, "user.test.scope", map[string]any{"a": 1}, time.Minute, func(ctx context.Context) (any, error) {
			executed++
			return gin.H{"ok": true}, nil
		})
	})

	req := httptest.NewRequest(http.MethodPost, "/idempotent", bytes.NewBufferString(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, executed)
}

// TestExecuteUserIdempotentJSONFailCloseOnStoreUnavailable injects an initial
// persistence failure for an authenticated user's keyed command. The helper must
// return service unavailable while leaving the business callback unexecuted.
func TestExecuteUserIdempotentJSONFailCloseOnStoreUnavailable(t *testing.T) {
	executor := Executor{coordinator: idempotency.NewIdempotencyCoordinator(userStoreUnavailableRepoStub{}, idempotency.DefaultIdempotencyConfig())}

	var executed int
	router := gin.New()
	router.Use(withUserSubject(2))
	router.POST("/idempotent", func(c *gin.Context) {
		executor.ExecuteUserIdempotentJSON(c, "user.test.scope", map[string]any{"a": 1}, time.Minute, func(ctx context.Context) (any, error) {
			executed++
			return gin.H{"ok": true}, nil
		})
	})

	req := httptest.NewRequest(http.MethodPost, "/idempotent", bytes.NewBufferString(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, 0, executed)
}

// TestExecuteUserIdempotentJSONConcurrentRetrySingleSideEffectAndReplay overlaps
// requests for the same authenticated actor and key around a slow callback.
// The callback must run once in this scenario, and a subsequent request must
// return the stored response with its replay header.
func TestExecuteUserIdempotentJSONConcurrentRetrySingleSideEffectAndReplay(t *testing.T) {
	repo := testkit.NewMemoryStore()
	cfg := idempotency.DefaultIdempotencyConfig()
	cfg.ProcessingTimeout = 2 * time.Second
	executor := Executor{coordinator: idempotency.NewIdempotencyCoordinator(repo, cfg)}

	var executed atomic.Int32
	router := gin.New()
	router.Use(withUserSubject(3))
	router.POST("/idempotent", func(c *gin.Context) {
		executor.ExecuteUserIdempotentJSON(c, "user.test.scope", map[string]any{"a": 1}, time.Minute, func(ctx context.Context) (any, error) {
			executed.Add(1)
			// Delay callback completion so a concurrent duplicate can encounter processing
			// state before the successful result is stored, while allowing later arrivals to replay it.
			time.Sleep(80 * time.Millisecond)
			return gin.H{"ok": true}, nil
		})
	})

	call := func() (int, http.Header) {
		req := httptest.NewRequest(http.MethodPost, "/idempotent", bytes.NewBufferString(`{"a":1}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "same-user-key")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code, rec.Header()
	}

	var status1, status2 int
	var wg sync.WaitGroup
	// Start both request goroutines before waiting so the shared memory store arbitrates
	// competing claims, and read their statuses only after each goroutine has completed.
	wg.Add(2)
	go func() { defer wg.Done(); status1, _ = call() }()
	go func() { defer wg.Done(); status2, _ = call() }()
	wg.Wait()

	require.Contains(t, []int{http.StatusOK, http.StatusConflict}, status1)
	require.Contains(t, []int{http.StatusOK, http.StatusConflict}, status2)
	require.Equal(t, int32(1), executed.Load())

	status3, headers3 := call()
	require.Equal(t, http.StatusOK, status3)
	require.Equal(t, "true", headers3.Get("X-Idempotency-Replayed"))
	require.Equal(t, int32(1), executed.Load())
}
