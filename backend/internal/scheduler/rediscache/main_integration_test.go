//go:build integration

package rediscache

import (
	"context"
	"log"
	"os"
	"testing"

	redisclient "github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Redis adapter integration tests need one process-owned server while each
// fixture isolates its keys through a prefix hook. TestMain starts Redis 8.4
// in a container, publishes its client for fixture options, then closes the
// client and terminates the server after the suite.

// TestMain starts the Redis container and shared integration client before
// running adapter tests. Startup failures exit the process, and completed
// tests are followed by client and container cleanup.
func TestMain(m *testing.M) {
	ctx := context.Background()
	container, err := rediscontainer.Run(ctx, "redis:8.4-alpine")
	if err != nil {
		log.Printf("启动隔离 Redis 失败: %v", err)
		os.Exit(1)
	}
	host, err := container.Host(ctx)
	if err != nil {
		// Terminate the started container when endpoint discovery fails; cleanup
		// errors are discarded while the discovery error is logged before process
		// exit.
		_ = container.Terminate(ctx)
		log.Print(err)
		os.Exit(1)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		// Terminate the started container when endpoint discovery fails; cleanup
		// errors are discarded while the discovery error is logged before process
		// exit.
		_ = container.Terminate(ctx)
		log.Print(err)
		os.Exit(1)
	}
	integrationRedis = redisclient.NewClient(&redisclient.Options{Addr: host + ":" + port.Port()})
	m.Run()
	// Close the process-owned client before terminating its server after tests
	// finish; teardown errors are discarded and fixture key assertions remain
	// test-owned.
	_ = integrationRedis.Close()
	_ = container.Terminate(ctx)
}
