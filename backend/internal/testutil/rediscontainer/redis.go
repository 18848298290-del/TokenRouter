//go:build integration

package rediscontainer

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Some Redis integration fixtures need their own server because they test
// shared keys, locks, or cancellation independently of other tests. New starts
// a container and returns its client, with reverse cleanup order releasing the
// client before server termination.

// New returns a client connected to a dedicated Redis integration container or
// fails the test. Startup and connection-string discovery use a one-minute
// context; cleanup closes the client before terminating the container. Callers
// register application cleanup after obtaining the client.
func New(t *testing.T) *redis.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	container, err := Run(ctx, "redis:8.4-alpine")
	require.NoError(t, err)
	// Register the container first so client and later application cleanup run
	// before server termination. Cleanup uses a fresh context after startup's budget ends.
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err)
	options, err := redis.ParseURL(uri)
	require.NoError(t, err)
	client := redis.NewClient(options)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}
