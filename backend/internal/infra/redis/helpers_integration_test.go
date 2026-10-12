//go:build integration

package redis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// redisImageTag selects the Redis image for integration tests.
const redisImageTag = "redis:8.4-alpine"

// startRedis starts a container and registers client and container cleanup.
func startRedis(t *testing.T, ctx context.Context) *redis.Client {
	t.Helper()
	ensureDockerAvailable(t)

	redisContainer, err := rediscontainer.Run(ctx, redisImageTag)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = redisContainer.Terminate(ctx)
	})

	redisHost, err := redisContainer.Host(ctx)
	require.NoError(t, err)
	redisPort, err := redisContainer.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)

	rdb := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%d", redisHost, redisPort.Int()),
		DB:   0,
	})
	require.NoError(t, rdb.Ping(ctx).Err())

	t.Cleanup(func() {
		_ = rdb.Close()
	})

	return rdb
}

// ensureDockerAvailable fails in CI or strict mode, and otherwise skips when Docker is absent.
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

// dockerAvailable checks DOCKER_HOST and common socket paths; it does not probe the daemon.
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

// userHomeDir returns the home directory, or an empty string on error.
func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
