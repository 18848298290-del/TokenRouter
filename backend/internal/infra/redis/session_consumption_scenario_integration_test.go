//go:build integration

package redis

// Session scenarios exercise JSON storage and single consumption against the
// Redis container created by helpers_integration_test.go.

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/redis/session"
)

// TestRedisSessionConcurrentConsumption checks shared key formatting and one successful claimant.
func TestRedisSessionConcurrentConsumption(t *testing.T) {
	client := startRedis(t, t.Context())
	first := session.New(client, "test:session", time.Minute)
	second := session.New(client, "test:session:", time.Minute)
	expected := map[string]string{"state": "opaque", "verifier": "test-value"}
	require.NoError(t, first.Set(t.Context(), " id ", expected))
	var decoded map[string]string
	found, err := second.Get(t.Context(), "id", &decoded)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, expected, decoded)
	type result struct {
		claimed bool
		err     error
	}
	results := make(chan result, 16)
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			claimed, err := second.TryConsume(t.Context(), "id")
			results <- result{claimed, err}
		})
	}
	group.Wait()
	close(results)
	winners := 0
	for value := range results {
		require.NoError(t, value.err)
		if value.claimed {
			winners++
		}
	}
	require.Equal(t, 1, winners)
	require.NoError(t, first.Delete(t.Context(), "id"))
	found, err = second.Get(t.Context(), "id", &decoded)
	require.NoError(t, err)
	require.False(t, found)
	exists, err := client.Exists(t.Context(), "test:session:id", "test:session:used:id").Result()
	require.NoError(t, err)
	require.Zero(t, exists)
}
