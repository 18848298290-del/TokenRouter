//go:build integration

package rediscontainer

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// redis.go starts a dedicated Redis fixture and registers client and container
// cleanup.
// Some Redis integration fixtures need their own server because they test
// shared keys, locks, or cancellation independently of other tests. New starts
// a container and returns its client, with reverse cleanup order releasing the
// client before server termination.

// New returns a client connected to a dedicated Redis integration container or
// fails the test. Startup and connection-string discovery use a one-minute
// context. Cleanup closes the client before terminating any created container
// with a fresh thirty-second context, including after startup errors. Callers
// register application cleanup after obtaining the client.
func New(t *testing.T) *redis.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	container, err := Run(ctx, "redis:8.4-alpine")
	// Register any created container before startup errors can stop this test.
	// Later client cleanups run first because testing reverses registration order.
	if container != nil {
		t.Cleanup(func() {
			// Setup or test cancellation must not cancel Docker removal. Start the
			// bounded cleanup context when this callback runs.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := container.Terminate(cleanupCtx); err != nil {
				t.Errorf("terminate Redis container: %v", err)
			}
		})
	}
	require.NoError(t, err)
	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err)
	options, err := redis.ParseURL(uri)
	require.NoError(t, err)
	client := redis.NewClient(options)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}
