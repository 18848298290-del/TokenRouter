//go:build integration

package rediscache

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	redisclient "github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// Redis adapter integration tests need one process-owned server while each
// fixture isolates its keys through a prefix hook. TestMain starts Redis 8.4
// in a container, publishes its client for fixture options, then closes the
// client and terminates the server after the suite.

// TestMain runs the Redis integration harness and exits after deferred cleanup
// finishes. Startup or container cleanup failure produces a nonzero status,
// alongside failures reported by the adapter tests.
func TestMain(m *testing.M) { os.Exit(runRedisTests(m)) }

// runRedisTests starts the Redis container and shared client before running
// adapter tests. Setup failures log and return one. Cleanup closes the client
// before removing any created container with a fresh thirty-second context;
// removal failure turns a successful suite status into one.
func runRedisTests(m *testing.M) (exitCode int) {
	ctx := context.Background()
	container, err := rediscontainer.Run(ctx, "redis:8.4-alpine")
	// Startup can return a created container with an error. Register removal
	// before handling that error so endpoint failures also release the fixture.
	if container != nil {
		defer func() {
			// Begin the cleanup budget on return so suite execution and failed
			// setup cannot consume the time available for Docker removal.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := container.Terminate(cleanupCtx); err != nil {
				log.Printf("terminate Redis container: %v", err)
				if exitCode == 0 {
					exitCode = 1
				}
			}
		}()
	}
	if err != nil {
		log.Printf("启动隔离 Redis 失败: %v", err)
		return 1
	}
	host, err := container.Host(ctx)
	if err != nil {
		log.Print(err)
		return 1
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		log.Print(err)
		return 1
	}
	integrationRedis = redisclient.NewClient(&redisclient.Options{Addr: host + ":" + port.Port()})
	// Close the process-owned client before terminating its server after tests
	// finish; its close error is discarded after the suite's Redis assertions.
	defer func() { _ = integrationRedis.Close() }()
	return m.Run()
}
