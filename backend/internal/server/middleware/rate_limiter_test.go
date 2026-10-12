package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	ippkg "github.com/TokenFlux/TokenRouter/internal/server/clientip"
)

// fakeFixedWindow provides controllable counts and errors for HTTP rate limit tests.
type fakeFixedWindow struct {
	counts  map[string]int64
	failure error
}

func (f *fakeFixedWindow) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, int64, time.Duration, error) {
	if f.failure != nil {
		return false, 0, 0, f.failure
	}
	f.counts[key]++
	count := f.counts[key]
	if count > int64(limit) {
		return false, count, window, nil
	}
	return true, count, 0, nil
}

// TestRateLimiterFailureModes covers allowing and rejecting requests on counter errors.
func TestRateLimiterFailureModes(t *testing.T) {
	for _, mode := range []RateLimitFailureMode{RateLimitFailOpen, RateLimitFailClose} {
		limiter := NewRateLimiter(&fakeFixedWindow{failure: errors.New("redis unavailable")})
		router := gin.New()
		router.Use(limiter.LimitWithOptions("failure", 1, time.Second, RateLimitOptions{FailureMode: mode}))
		router.GET("/", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if mode == RateLimitFailClose {
			require.Equal(t, http.StatusTooManyRequests, w.Code)
		} else {
			require.Equal(t, http.StatusOK, w.Code)
		}
	}
}

func TestRateLimiterDifferentIPsIndependent(t *testing.T) {
	callCounts := make(map[string]int64)
	limiter := NewRateLimiter(&fakeFixedWindow{counts: callCounts})

	router := gin.New()
	router.Use(limiter.LimitWithOptions("api", 1, time.Second, RateLimitOptions{}))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// The first IP starts with an unused bucket.
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.RemoteAddr = "10.0.0.1:1234"
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusOK, rec1.Code, "第一个 IP 的第一次请求应通过")

	// The second IP has an independent bucket.
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.RemoteAddr = "10.0.0.2:5678"
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code, "第二个 IP 的第一次请求应独立通过")

	// The first IP has exhausted its limit.
	req3 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req3.RemoteAddr = "10.0.0.1:1234"
	rec3 := httptest.NewRecorder()
	router.ServeHTTP(rec3, req3)
	require.Equal(t, http.StatusTooManyRequests, rec3.Code, "第一个 IP 的第二次请求应被限流")
}

func TestRateLimiterHonorsForwardedIPSnapshot(t *testing.T) {
	callCounts := make(map[string]int64)
	limiter := NewRateLimiter(&fakeFixedWindow{counts: callCounts})

	router := gin.New()
	// Simulate the forwarded-IP compatibility snapshot installed by SessionBindingContext.
	router.Use(func(c *gin.Context) {
		ippkg.SetForwardedIPSettings(c, true, nil)
		c.Next()
	})
	router.Use(limiter.LimitWithOptions("fwd", 1, time.Second, RateLimitOptions{}))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	send := func(xff string) int {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		// Both clients connect through the same reverse proxy.
		req.RemoteAddr = "127.0.0.1:5678"
		req.Header.Set("X-Forwarded-For", xff)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	// Forwarded client addresses select independent buckets behind the proxy.
	require.Equal(t, http.StatusOK, send("198.51.100.1"))
	require.Equal(t, http.StatusOK, send("198.51.100.2"))
	// The same forwarded client still exhausts its own limit.
	require.Equal(t, http.StatusTooManyRequests, send("198.51.100.1"))

	require.Contains(t, callCounts, "fwd:198.51.100.1")
	require.Contains(t, callCounts, "fwd:198.51.100.2")
}

// TestRateLimiterSuccessAndLimit checks the rejection response and Retry-After header.
func TestRateLimiterSuccessAndLimit(t *testing.T) {
	limiter := NewRateLimiter(&fakeFixedWindow{counts: map[string]int64{}})
	router := gin.New()
	router.Use(limiter.LimitWithOptions("test", 1, time.Second, RateLimitOptions{}))
	router.GET("/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/test", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	require.Equal(t, http.StatusOK, request().Code)
	limited := request()
	require.Equal(t, http.StatusTooManyRequests, limited.Code)
	require.Equal(t, "1", limited.Header().Get("Retry-After"))
	require.JSONEq(t, `{"error":"rate limit exceeded","message":"Too many requests, please try again later"}`, limited.Body.String())
}
