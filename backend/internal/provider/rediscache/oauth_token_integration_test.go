//go:build integration

package rediscache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// The shared OAuth adapter must remove a stored token and acknowledge deletion
// when its key is already absent. A real Redis fixture verifies those results
// through the AccessTokenCache interface, distinguishing redis.Nil on a later
// read from deletion errors.

// GeminiTokenCacheSuite exercises shared OAuth token deletion against a
// per-test Redis fixture.
type GeminiTokenCacheSuite struct {
	suite.Suite // suite assertions and runner state
	// ctx, rdb and cache are replaced together by SetupTest for each method.
	// cache borrows rdb, whose closure and container cleanup belong to that test.
	ctx   context.Context           // shared test operation context
	rdb   *redis.Client             // per-test Redis client dependency
	cache provider.AccessTokenCache // shared OAuth adapter under test
}

// SetupTest creates a Redis client and shared token adapter for each suite
// method, using a background operation context. rediscontainer.New registers
// client and container cleanup with the current test.
func (s *GeminiTokenCacheSuite) SetupTest() {
	s.ctx = context.Background()
	s.rdb = rediscontainer.New(s.T())
	s.cache = NewOAuthTokenCache(s.rdb)
}

// TestDeleteAccessToken writes and reads a token through the shared OAuth
// adapter before deleting it. A later read returns redis.Nil, confirming that
// deletion removed the stored value.
func (s *GeminiTokenCacheSuite) TestDeleteAccessToken() {
	cacheKey := "project-123"
	token := "token-value"
	require.NoError(s.T(), s.cache.SetAccessToken(s.ctx, cacheKey, token, time.Minute))

	got, err := s.cache.GetAccessToken(s.ctx, cacheKey)
	require.NoError(s.T(), err)
	require.Equal(s.T(), token, got)

	require.NoError(s.T(), s.cache.DeleteAccessToken(s.ctx, cacheKey))

	_, err = s.cache.GetAccessToken(s.ctx, cacheKey)
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil after delete")
}

// TestDeleteAccessToken_MissingKey asks the shared OAuth adapter to delete a
// key that has no stored token. The nil error protects Redis deletion's
// idempotent missing-key behavior.
func (s *GeminiTokenCacheSuite) TestDeleteAccessToken_MissingKey() {
	require.NoError(s.T(), s.cache.DeleteAccessToken(s.ctx, "missing-key"))
}

// TestGeminiTokenCacheSuite runs the OAuth adapter's deletion checks against
// actual Redis fixtures. Suite setup provides fresh dependencies for each
// method so stored values from one check do not affect another.
func TestGeminiTokenCacheSuite(t *testing.T) {
	suite.Run(t, new(GeminiTokenCacheSuite))
}
