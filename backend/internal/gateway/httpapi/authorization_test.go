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

// gatewayLimitCache returns a configured admission result and counts lease releases.
type gatewayLimitCache struct {
	apikey.APIKeyCache
	err      error
	calls    int
	released int
}

func (s *gatewayLimitCache) ReserveRequest(context.Context, int64, string, int, int) (time.Duration, error) {
	s.calls++
	return 1500 * time.Millisecond, s.err
}

func (s *gatewayLimitCache) RefreshRequest(context.Context, int64, string) error { return nil }

func (s *gatewayLimitCache) ReleaseRequest(context.Context, int64, string) error {
	s.released++
	return nil
}

// TestKeyRequestAdmissionHTTP covers both error formats, non-consuming endpoints, and request-end release.
func TestKeyRequestAdmissionHTTP(t *testing.T) {
	for _, google := range []bool{false, true} {
		for _, tc := range []struct {
			name, method, path      string
			err                     error
			status, calls, released int
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
				defer keys.Stop()
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(tc.method, tc.path, nil)
				options := APIKeyAuthorizationOptions{}
				options.Authentication.Google = google
				release, ok := acquireKeyRequest(c, keys, &apikey.APIKey{ID: 1, ConcurrencyLimit: 2, RPMLimit: 3}, options)
				if ok {
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
func TestResponsesAdmissionUsesTurns(t *testing.T) {
	for _, tc := range []struct {
		method, path  string
		upgrade, want bool
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
