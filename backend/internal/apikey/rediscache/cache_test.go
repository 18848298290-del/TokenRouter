package rediscache

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// TestApiKeyRateLimitKey supplies positive, zero, negative and maximum signed
// user identifiers. It checks the exact creation-failure Redis name produced
// for each input without validating user existence.
func TestApiKeyRateLimitKey(t *testing.T) {
	tests := []struct {
		name     string // counter naming scenario label
		userID   int64  // supplied signed user identifier
		expected string // expected Redis counter name
	}{
		{
			name:     "normal_user_id",
			userID:   123,
			expected: "apikey:ratelimit:123",
		},
		{
			name:     "zero_user_id",
			userID:   0,
			expected: "apikey:ratelimit:0",
		},
		{
			name:     "negative_user_id",
			userID:   -1,
			expected: "apikey:ratelimit:-1",
		},
		{
			name:     "max_int64",
			userID:   math.MaxInt64,
			expected: "apikey:ratelimit:9223372036854775807",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ApiKeyRateLimitKey(tc.userID)
			require.Equal(t, tc.expected, got)
		})
	}
}

// TestAPIKeyCacheSubscriber_BlocksUntilContextCancellation receives a
// publication from an active in-memory Redis subscription. It checks the loop
// remains running while connected and returns a cancellation error after the
// caller ends its context.
func TestAPIKeyCacheSubscriber_BlocksUntilContextCancellation(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	// The test owns this Redis client. Close it after subscription cancellation
	// and the final return observation so its background connection work can end.
	defer func() { _ = client.Close() }()
	cache := NewAPIKeyCache(client)
	ctx, cancel := context.WithCancel(context.Background())
	received := make(chan string, 1)
	returned := make(chan error, 1)
	// Keep subscription processing in a separate goroutine while the test
	// publishes and observes its lifetime; the buffered return channel records final teardown.
	go func() {
		returned <- cache.SubscribeAuthCacheInvalidation(ctx, func(value string) { received <- value })
	}()

	var value string
	require.Eventually(t, func() bool {
		require.NoError(t, client.Publish(context.Background(), AuthCacheInvalidateChannel, "hash").Err())
		select {
		case value = <-received:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, "hash", value)
	select {
	case err := <-returned:
		t.Fatalf("subscriber returned while connection was active: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-returned:
		require.True(t, errors.Is(err, context.Canceled))
	case <-time.After(time.Second):
		t.Fatal("subscriber did not stop after context cancellation")
	}
}
