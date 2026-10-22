//go:build integration

package redis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Integration fixtures start a dedicated Redis container and register its
// client for cleanup after tests finish. Docker availability is inferred from
// configuration or path existence before startup; local absence skips the
// test, while CI or strict verification treats it as failure.

// redisImageTag pins the Redis container image used by these integration
// fixtures so their script and session checks run against the selected server.
const redisImageTag = "redis:8.4-alpine"

// startRedis launches the selected Redis container, resolves its address, and
// returns a pinged client. Test cleanup closes the client before terminating
// any created container with a fresh thirty-second context. The supplied
// context governs setup, and cleanup failures are reported to t.
func startRedis(t *testing.T, ctx context.Context) *redis.Client {
	t.Helper()
	ensureDockerAvailable(t)

	redisContainer, err := rediscontainer.Run(ctx, redisImageTag)
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

// ensureDockerAvailable accepts Docker configuration or a detected candidate
// path. If neither is found, CI or TOKENROUTER_VERIFY_STRICT=1 fails the
// test; other local runs skip before creating a container.
func ensureDockerAvailable(t *testing.T) {
	t.Helper()
	if dockerAvailable() {
		return
	}
	if os.Getenv("CI") != "" || os.Getenv("TOKENROUTER_VERIFY_STRICT") == "1" {
		t.Fatal("Docker 未启用，无法执行集成测试")
	}
	t.Skip("Docker 未启用，跳过依赖 testcontainers 的集成测试")
}

// dockerAvailable reports a nonempty DOCKER_HOST or an existing candidate
// Docker path. Path checks do not verify a daemon or socket type; failed Stat
// calls continue through later platform locations.
func dockerAvailable() bool {
	if os.Getenv("DOCKER_HOST") != "" {
		return true
	}

	socketCandidates := []string{
		"/var/run/docker.sock",
		filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "docker.sock"),
		filepath.Join(userHomeDir(), ".docker", "run", "docker.sock"),
		filepath.Join(userHomeDir(), ".docker", "desktop", "docker.sock"),
		filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "docker.sock"),
	}

	// Missing or inaccessible candidates do not establish Docker availability.
	// Continue through the remaining locations so one absent platform path
	// cannot prevent detection.
	for _, socket := range socketCandidates {
		if socket == "" {
			continue
		}
		if _, err := os.Stat(socket); err == nil {
			return true
		}
	}
	return false
}

// userHomeDir returns the process home directory for Docker path candidates.
// A lookup error becomes empty text, allowing callers to continue checking
// their remaining configured and platform-specific locations.
func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Home lookup is optional detection evidence. Keep the other Docker
		// paths available by returning empty text when this lookup cannot contribute.
		return ""
	}
	return home
}
