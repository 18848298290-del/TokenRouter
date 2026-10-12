//go:build integration

package redis

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/infra/redis/session"
)

// Two stores with equivalent prefix spellings share session data and a
// consumption marker in a real Redis container. Concurrent callers compete
// for that marker; after their results are collected, Delete is checked
// against both the normalized data and marker keys.

// TestRedisSessionConcurrentConsumption writes a spaced ID through one prefix
// spelling and reads through another. It requires matching JSON, exactly one
// of sixteen concurrent claimants, and removal of both data and marker after
// Delete.
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
	// result carries one consumer's marker claim into the serial winner count.
	type result struct {
		claimed bool  // observed successful claim
		err     error // observed consume error
	}
	// Hold one result per consumer before waiting, then close after their
	// sends finish so winner counting observes every claim without blocking producers.
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
