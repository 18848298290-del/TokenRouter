//go:build integration

package rediscache

import (
	"context"
	"fmt"
	"os"
	"testing"

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
// fixtures, and returns m.Run's status. The client closes before deferred
// container termination on helper return, with cleanup errors ignored; setup
// failures print to stderr and return one.
func runRedisTests(m *testing.M) int {
	ctx := context.Background()
	c, err := rediscontainer.Run(ctx, "redis:8.4-alpine")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Register container cleanup before further setup so helper returns after
	// address or URL parsing failure still attempts termination.
	defer func() { _ = c.Terminate(ctx) }()
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
	// Close the shared client before the earlier container defer runs; both
	// cleanup errors are suppressed while the test-run status is retained.
	defer func() { _ = integrationRedis.Close() }()
	return m.Run()
}
