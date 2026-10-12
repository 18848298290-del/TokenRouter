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

var (
	integrationRedis  *redisclient.Client
	redisNamespaceSeq uint64
)

// TestMain starts the Redis fixture and runs integration tests.
func TestMain(m *testing.M) { os.Exit(runRedisTests(m)) }

func runRedisTests(m *testing.M) int {
	ctx := context.Background()
	c, err := rediscontainer.Run(ctx, "redis:8.4-alpine")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
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
	defer func() { _ = integrationRedis.Close() }()
	return m.Run()
}
