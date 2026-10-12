//go:build integration

package rediscontainer

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// New starts an isolated Redis instance for token, lock, and cancellation tests.
func New(t *testing.T) *redis.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	container, err := Run(ctx, "redis:8.4-alpine")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err)
	options, err := redis.ParseURL(uri)
	require.NoError(t, err)
	client := redis.NewClient(options)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}
