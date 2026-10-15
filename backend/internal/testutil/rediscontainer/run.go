//go:build integration

package rediscontainer

import (
	"context"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// run.go starts Redis containers with listening-port and readiness-log checks.
// A container listener can appear before Redis announces readiness for command
// handling. Run delegates startup to Testcontainers with both a listening-port
// wait and the readiness-log wait, giving fixture callers one returned
// container after both conditions are met.

// Run starts the supplied Redis image and waits for its port and readiness log.
// Each wait strategy has a one-minute startup timeout and observes ctx; callers
// own termination of any container returned by Testcontainers, including a
// nonnil container accompanying an error.
func Run(ctx context.Context, image string) (*redis.RedisContainer, error) {
	return redis.Run(ctx, image, testcontainers.WithWaitStrategy(
		wait.ForListeningPort("6379/tcp").WithStartupTimeout(time.Minute),
		wait.ForLog("* Ready to accept connections").WithStartupTimeout(time.Minute),
	))
}
