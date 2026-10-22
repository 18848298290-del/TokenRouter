//go:build integration

package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Registration attempts consume the public authentication Redis limit before
// business validation returns a bad request. The integration scenario repeats
// one client IP until the sixth request is rejected, while local Docker
// discovery determines whether the container-backed test can run or must fail
// under strict verification.

// authRouteRedisImageTag selects the Redis container version used to exercise
// persistent authentication rate counters in this integration fixture.
const authRouteRedisImageTag = "redis:8.4-alpine"

// TestAuthRegisterRateLimitThresholdHitReturns429 sends six invalid
// registration requests from one client IP. The first five must reach
// BadRequest business validation, and the sixth must return TooManyRequests
// with the rate-limit message.
func TestAuthRegisterRateLimitThresholdHitReturns429(t *testing.T) {
	ctx := context.Background()
	rdb := startAuthRouteRedis(t, ctx)

	router := newAuthRoutesTestRouter(rdb)
	const path = "/api/v1/auth/register"

	// Public registration permits five attempts in this IP window. A sixth
	// request distinguishes rate-counter admission from the bad-body validation reached by earlier requests.
	for i := 1; i <= 6; i++ {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "198.51.100.10:23456"

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if i <= 5 {
			require.Equal(t, http.StatusBadRequest, w.Code, "第 %d 次请求应先进入业务校验", i)
			continue
		}
		require.Equal(t, http.StatusTooManyRequests, w.Code, "第 6 次请求应命中限流")
		require.Contains(t, w.Body.String(), "rate limit exceeded")
	}
}

// startAuthRouteRedis starts and pings a Redis container after Docker
// discovery. The supplied context governs setup. Test cleanup closes the
// client before terminating any created container with a fresh thirty-second
// context, and cleanup failures are reported to t.
func startAuthRouteRedis(t *testing.T, ctx context.Context) *redis.Client {
	t.Helper()
	ensureAuthRouteDockerAvailable(t)

	redisContainer, err := rediscontainer.Run(ctx, authRouteRedisImageTag)
	// Register any created container before startup errors can stop this test.
	// Later client cleanups run first because testing reverses registration order.
	if redisContainer != nil {
		t.Cleanup(func() {
			// Setup or test cancellation must not cancel Docker removal. Start the
			// bounded cleanup context when this callback runs.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := redisContainer.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate Redis container: %v", err)
			}
		})
	}
	require.NoError(t, err)

	redisHost, err := redisContainer.Host(ctx)
	require.NoError(t, err)
	redisPort, err := redisContainer.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)

	rdb := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%d", redisHost, redisPort.Int()),
		DB:   0,
	})
	// Register the client before ping can fail so setup failures release its
	// connections while the earlier container cleanup still waits its turn.
	t.Cleanup(func() {
		if err := rdb.Close(); err != nil {
			t.Errorf("close Redis client: %v", err)
		}
	})
	require.NoError(t, rdb.Ping(ctx).Err())
	return rdb
}

// ensureAuthRouteDockerAvailable permits container setup when its discovery
// heuristic succeeds. Missing Docker evidence fails CI or strict verification,
// while ordinary local runs skip the integration scenario instead of attempting
// setup.
func ensureAuthRouteDockerAvailable(t *testing.T) {
	t.Helper()
	if authRouteDockerAvailable() {
		return
	}
	if os.Getenv("CI") != "" || os.Getenv("TOKENROUTER_VERIFY_STRICT") == "1" {
		t.Fatal("Docker 未启用，无法执行集成测试")
	}
	t.Skip("Docker 未启用，跳过认证限流集成测试")
}

// authRouteDockerAvailable reports configured DOCKER_HOST or an existing
// candidate socket path. This is a discovery heuristic based on environment and
// filesystem presence; container setup performs the actual daemon operation
// afterward.
func authRouteDockerAvailable() bool {
	if os.Getenv("DOCKER_HOST") != "" {
		return true
	}

	socketCandidates := []string{
		"/var/run/docker.sock",
		filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "docker.sock"),
		filepath.Join(authRouteUserHomeDir(), ".docker", "run", "docker.sock"),
		filepath.Join(authRouteUserHomeDir(), ".docker", "desktop", "docker.sock"),
		filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "docker.sock"),
	}

	for _, socket := range socketCandidates {
		if socket == "" {
			continue
		}
		// Missing or unreadable candidates do not end discovery. Continue across
		// platform socket locations until a present path or the exhausted candidate list determines the result.
		if _, err := os.Stat(socket); err == nil {
			return true
		}
	}
	return false
}

// authRouteUserHomeDir returns the user directory used for Docker socket
// candidates. A lookup error becomes an empty string, leaving the discovery
// helper to continue its other environment and path checks.
func authRouteUserHomeDir() string {
	home, err := os.UserHomeDir()
	// Directory lookup failure supplies no home-based prefix. Docker discovery
	// still considers the environment and remaining platform socket candidates before rejecting availability.
	if err != nil {
		return ""
	}
	return home
}
