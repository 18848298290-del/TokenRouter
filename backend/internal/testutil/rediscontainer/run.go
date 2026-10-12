//go:build integration

package rediscontainer

import (
	"context"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Run waits for the Redis port and readiness log, allowing one minute for each wait strategy.
// The caller's context can cancel startup before either wait times out.
func Run(ctx context.Context, image string) (*redis.RedisContainer, error) {
	return redis.Run(ctx, image, testcontainers.WithWaitStrategy(
		wait.ForListeningPort("6379/tcp").WithStartupTimeout(time.Minute),
		wait.ForLog("* Ready to accept connections").WithStartupTimeout(time.Minute),
	))
}
