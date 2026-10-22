//go:build integration

package rediscache

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	redisclient "github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// These package fixtures share one container-backed Redis endpoint while the
// atomic sequence distinguishes per-test command namespaces.
var (
	integrationRedis  *redisclient.Client // shared unprefixed fixture client
	redisNamespaceSeq uint64              // atomic namespace sequence counter
)

// TestMain runs the Redis integration harness and exits with its returned
// status after the helper's deferred cleanup. Startup failures return a nonzero
// status; this harness does not locally skip missing Docker.
func TestMain(m *testing.M) { os.Exit(runRedisTests(m)) }

// runRedisTests starts a Redis container, publishes its client for suite
// fixtures, and returns the suite status after cleanup. Setup failures print
// to stderr and return one. The client closes before bounded container cleanup,
// whose failure turns a successful suite status into one.
func runRedisTests(m *testing.M) (exitCode int) {
	ctx := context.Background()
	c, err := rediscontainer.Run(ctx, "redis:8.4-alpine")
	// Startup can return a created container with an error. Register removal
	// before handling that error so every return path releases the fixture.
	if c != nil {
		defer func() {
			// Begin the cleanup budget on return so suite execution and failed
			// setup cannot consume the time available for Docker removal.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := c.Terminate(cleanupCtx); err != nil {
				fmt.Fprintf(os.Stderr, "terminate Redis container: %v\n", err)
				if exitCode == 0 {
					exitCode = 1
				}
			}
		}()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	url, err := c.ConnectionString(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	options, err := redisclient.ParseURL(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	integrationRedis = redisclient.NewClient(options)
	// Close the shared client before the earlier container defer runs; its close
	// error is discarded after the suite has completed all Redis command assertions.
	defer func() { _ = integrationRedis.Close() }()
	return m.Run()
}
