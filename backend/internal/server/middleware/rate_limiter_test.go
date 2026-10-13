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

// HTTP rate limiting separates route-and-client buckets and exposes rejection
// through status, JSON, and retry headers. A deterministic counter fixture
// keeps counts indefinitely so these tests can isolate failure policy and the
// request's captured forwarding mode.

// fakeFixedWindow supplies serial, nonexpiring request counts or one injected
// counter failure.
type fakeFixedWindow struct {
	// counts retains every call per exact key and never expires. Tests allocate
	// the map before successful counting and use the fixture serially.
	counts map[string]int64
	// failure is returned before counts is accessed. Set it during fixture setup
	// to exercise HTTP counter-error policy independently of admitted counts.
	failure error
}

// Allow increments the serial fixture's exact-key count and compares it with
// limit, including rejected calls. counts must be initialized and calls
// serialized; failure returns immediately. Rejection returns window as its
// retry delay because this fixture does not track expiry.
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

// TestRateLimiterFailureModes injects a counter error into each supported HTTP
// failure policy. Fail-close returns 429 while fail-open allows the successful
// endpoint to run.
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

// TestRateLimiterDifferentIPsIndependent sends two client addresses through one
// route limiter. Each address starts with its own budget, and a repeated
// request exhausts only the first client's counter.
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

// TestRateLimiterHonorsForwardedIPSnapshot pins compatibility forwarding policy
// before HTTP limiting behind one shared peer address. Forwarded clients get
// distinct buckets, and a repeated forwarded address exhausts its own budget.
func TestRateLimiterHonorsForwardedIPSnapshot(t *testing.T) {
	callCounts := make(map[string]int64)
	limiter := NewRateLimiter(&fakeFixedWindow{counts: callCounts})

	router := gin.New()
	// Pin compatibility policy before limiting so request consumers resolve
	// forwarded clients using one captured mode, as they do after
	// SessionBindingContext in application assembly.
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

// TestRateLimiterSuccessAndLimit sends two requests into a one-request client
// budget. The second response protects the 429 JSON shape and the integer retry
// delay.
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
