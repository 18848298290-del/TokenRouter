package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
)

// Key request admission translates the limiter result into either standard or
// Google errors and releases successful HTTP leases after use. Responses upgrades
// defer admission to individual turns, while non-consuming paths bypass the limiter.

// gatewayLimitCache returns a configured admission result and counts lease releases.
type gatewayLimitCache struct {
	apikey.APIKeyCache       // unused cache methods
	err                error // configured reservation failure
	// calls and released record reservations and cleanup independently. Ambiguous
	// store failures can require cleanup despite failed admission, unlike known limit rejections.
	calls    int // reservation call count
	released int // lease release count
}

// ReserveRequest records admission and returns the configured reservation result.
// The fixed wait is one and a half seconds so the HTTP assertion can verify
// upward rounding of Retry-After for either protocol's limit error.
func (s *gatewayLimitCache) ReserveRequest(context.Context, int64, string, int, int) (time.Duration, error) {
	s.calls++
	// Exercise fractional-second waits in HTTP retry-header rounding.
	return 1500 * time.Millisecond, s.err
}

// RefreshRequest lets admitted test leases renew through the service's normal lifecycle.
func (s *gatewayLimitCache) RefreshRequest(context.Context, int64, string) error { return nil }

// ReleaseRequest records a cleanup attempt and reports a successful test release.
func (s *gatewayLimitCache) ReleaseRequest(context.Context, int64, string) error {
	s.released++
	return nil
}

// TestKeyRequestAdmissionHTTP exercises standard and Google admission options
// across allowed, limited, unavailable-store, and non-consuming requests.
// Each case checks status, reservation count, and cleanup count; limit rejections
// also require a whole-second Retry-After rounded upward from the reported duration.
func TestKeyRequestAdmissionHTTP(t *testing.T) {
	for _, google := range []bool{false, true} {
		for _, tc := range []struct {
			// name identifies the case, while method and path select the admission
			// route; the same scenario runs through standard and Google error output.
			name, method, path string // name: case label; method: request verb; path: endpoint path
			err                error  // simulated store result
			// status, calls, and released describe one complete admission attempt.
			// Cleanup may occur after an uncertain store error even when the request is rejected.
			status, calls, released int // status: HTTP result; calls: reservation count; released: cleanup count
		}{
			{"allowed", "POST", "/v1/responses", nil, 200, 1, 1},
			{"concurrency", "POST", "/v1/responses", apikey.ErrKeyConcurrencyExceeded, 429, 1, 0},
			{"rpm", "POST", "/v1/responses", apikey.ErrKeyRPMExceeded, 429, 1, 0},
			{"unavailable", "POST", "/v1/responses", apikey.ErrKeyLimiterUnavailable, 503, 1, 1},
			{"usage", "GET", "/v1/usage", apikey.ErrKeyRPMExceeded, 200, 0, 0},
			{"models", "GET", "/v1/models", apikey.ErrKeyRPMExceeded, 200, 0, 0},
			{"cancel", "POST", "/v1/images/batches/1/cancel", apikey.ErrKeyRPMExceeded, 200, 0, 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cache := &gatewayLimitCache{err: tc.err}
				keys := apikey.NewAPIKeyService(nil, nil, nil, nil, nil, cache, nil)
				// Stop the service after each case so lease renewal workers finish
				// before the next case observes its independent reservation and release counts.
				defer keys.Stop()
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(tc.method, tc.path, nil)
				options := APIKeyAuthorizationOptions{}
				options.Authentication.Google = google
				release, ok := acquireKeyRequest(c, keys, &apikey.APIKey{ID: 1, ConcurrencyLimit: 2, RPMLimit: 3}, options)
				if ok {
					// Return admitted capacity before checking counts. Release joins its
					// renewal worker and records cleanup synchronously, so these reads include completed cleanup.
					release()
				}
				require.Equal(t, tc.status, recorder.Code)
				require.Equal(t, tc.calls, cache.calls)
				require.Equal(t, tc.released, cache.released)
				if tc.status == 429 {
					require.Equal(t, "2", recorder.Header().Get("Retry-After"))
				}
			})
		}
	}
}

// TestResponsesAdmissionUsesTurns checks that only Responses WebSocket upgrades use per-turn admission.
// Supported GET roots must match when upgrade headers are present; ordinary
// GET, POST, and another WebSocket path must retain their own admission timing.
func TestResponsesAdmissionUsesTurns(t *testing.T) {
	for _, tc := range []struct {
		// method and path choose the request target. upgrade supplies WebSocket
		// headers, and want records whether this combination defers admission to turns.
		method, path  string // method: request verb; path: endpoint path
		upgrade, want bool   // upgrade: header presence; want: turn-admission result
	}{
		{"GET", "/responses", true, true},
		{"GET", "/v1/responses", true, true},
		{"GET", "/backend-api/codex/responses", true, true},
		{"POST", "/v1/responses", true, false},
		{"GET", "/v1/responses", false, false},
		{"GET", "/realtime", true, false},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(tc.method, tc.path, nil)
		if tc.upgrade {
			c.Request.Header.Set("Upgrade", "websocket")
			c.Request.Header.Set("Connection", "Upgrade")
		}
		require.Equal(t, tc.want, isResponsesTurnAdmission(c))
	}
}

// TestGetAPIKeyIDFromContext supplies absent, mistyped, and nil-pointer context values.
// Each unusable value must return the zero sentinel, while a stored APIKey
// exposes its identifier for request observation.
func TestGetAPIKeyIDFromContext(t *testing.T) {
	t.Run("context 为 nil", func(t *testing.T) {
		require.Equal(t, int64(0), APIKeyIDFromContext(nil))
	})

	t.Run("上下文没有 api_key", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		require.Equal(t, int64(0), APIKeyIDFromContext(c))
	})

	t.Run("api_key 类型错误", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Set("api_key", "not-api-key")
		require.Equal(t, int64(0), APIKeyIDFromContext(c))
	})

	t.Run("api_key 指针为空", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		var k *apikey.APIKey
		c.Set("api_key", k)
		require.Equal(t, int64(0), APIKeyIDFromContext(c))
	})

	t.Run("正常读取 api_key_id", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Set("api_key", &apikey.APIKey{ID: 12345})
		require.Equal(t, int64(12345), APIKeyIDFromContext(c))
	})
}
