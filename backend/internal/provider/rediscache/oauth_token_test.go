package rediscache

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Token deletion must return a Redis connection failure to callers that
// observe cleanup results. A client pointed at an unavailable local endpoint
// exercises that error path while short connection timeouts and registered
// cleanup control the fixture's client resources.

// TestGeminiTokenCache_DeleteAccessToken_RedisError uses the shared OAuth
// adapter with an unavailable Redis endpoint. DeleteAccessToken returns an
// error, distinguishing failed cleanup from a successful deletion of an absent
// key.
func TestGeminiTokenCache_DeleteAccessToken_RedisError(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	// Expected connection failures can still allocate a client pool; register
	// closing with test cleanup so resources are released after the error assertion.
	t.Cleanup(func() {
		// Discard the close result after the connection-failure assertion; this fixture
		// observes command failure and uses cleanup to release the client pool.
		_ = rdb.Close()
	})

	cache := NewOAuthTokenCache(rdb)
	err := cache.DeleteAccessToken(context.Background(), "broken")
	require.Error(t, err)
}
