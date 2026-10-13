//go:build integration

package rediscache

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// RedeemCacheSuite exercises attempt counters and code exclusion against the
// Redis container fixture.
type RedeemCacheSuite struct {
	// Suite owns Redis fixture setup, context and cleanup for these cases.
	// Embedded methods remain available to establish isolated clients and assert
	// TTL bounds.
	rediscontainer.Suite // inherited Redis fixture lifecycle
	// cache is rebound after the inherited per-test setup using that fixture's
	// Redis client. Its lifetime ends with the owning suite test.
	cache *RedeemCache // current test redemption cache
}

// SetupTest invokes the embedded Redis fixture setup and binds a redemption
// cache to its RDB. The suite owns fixture cleanup, and the cache reuses that
// client for the current test's counter and lock operations.
func (s *RedeemCacheSuite) SetupTest() {
	s.Suite.SetupTest()
	s.cache = NewRedeemCache(s.RDB)
}

// TestGetRedeemAttemptCount_Missing reads an absent user counter through the
// Redis fixture. It checks the cache returns zero with no error for that
// missing key.
func (s *RedeemCacheSuite) TestGetRedeemAttemptCount_Missing() {
	missingUserID := int64(99999)
	count, err := s.cache.GetRedeemAttemptCount(s.Ctx, missingUserID)
	require.NoError(s.T(), err, "expected nil error for missing rate-limit key")
	require.Equal(s.T(), 0, count, "expected zero count for missing key")
}

// TestIncrementAndGetRedeemAttemptCount records one attempt and reads its
// stored counter. It checks count one and a positive remaining TTL bounded by
// twenty-four hours.
func (s *RedeemCacheSuite) TestIncrementAndGetRedeemAttemptCount() {
	userID := int64(1)
	key := fmt.Sprintf("%s%d", "redeem:ratelimit:", userID)

	require.NoError(s.T(), s.cache.IncrementRedeemAttemptCount(s.Ctx, userID), "IncrementRedeemAttemptCount")
	count, err := s.cache.GetRedeemAttemptCount(s.Ctx, userID)
	require.NoError(s.T(), err, "GetRedeemAttemptCount")
	require.Equal(s.T(), 1, count, "count mismatch")

	ttl, err := s.RDB.TTL(s.Ctx, key).Result()
	require.NoError(s.T(), err, "TTL")
	s.AssertTTLWithin(ttl, 1*time.Second, (24 * time.Hour))
}

// TestMultipleIncrements records three attempts for one user through the Redis
// fixture. It checks the saved counter reaches three after those sequential
// increments.
func (s *RedeemCacheSuite) TestMultipleIncrements() {
	userID := int64(2)

	require.NoError(s.T(), s.cache.IncrementRedeemAttemptCount(s.Ctx, userID))
	require.NoError(s.T(), s.cache.IncrementRedeemAttemptCount(s.Ctx, userID))
	require.NoError(s.T(), s.cache.IncrementRedeemAttemptCount(s.Ctx, userID))

	count, err := s.cache.GetRedeemAttemptCount(s.Ctx, userID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 3, count, "count after 3 increments")
}

// TestAcquireAndReleaseRedeemLock acquires a code key, attempts a second
// acquisition, releases it and acquires again. It checks the held key excludes
// the second acquisition and release permits the next.
func (s *RedeemCacheSuite) TestAcquireAndReleaseRedeemLock() {
	ok, err := s.cache.AcquireRedeemLock(s.Ctx, "CODE", 10*time.Second)
	require.NoError(s.T(), err, "AcquireRedeemLock")
	require.True(s.T(), ok)

	// Second acquire should fail
	ok, err = s.cache.AcquireRedeemLock(s.Ctx, "CODE", 10*time.Second)
	require.NoError(s.T(), err, "AcquireRedeemLock 2")
	require.False(s.T(), ok, "expected lock to be held")

	// Release
	require.NoError(s.T(), s.cache.ReleaseRedeemLock(s.Ctx, "CODE"), "ReleaseRedeemLock")

	// Now acquire should succeed
	ok, err = s.cache.AcquireRedeemLock(s.Ctx, "CODE", 10*time.Second)
	require.NoError(s.T(), err, "AcquireRedeemLock after release")
	require.True(s.T(), ok)
}

// TestAcquireRedeemLock_TTL acquires a code key with a fifteen-second
// lifetime. It checks acquisition and a positive Redis TTL no larger than the
// supplied duration.
func (s *RedeemCacheSuite) TestAcquireRedeemLock_TTL() {
	lockKey := "redeem:lock:" + "CODE2"
	lockTTL := 15 * time.Second

	ok, err := s.cache.AcquireRedeemLock(s.Ctx, "CODE2", lockTTL)
	require.NoError(s.T(), err, "AcquireRedeemLock CODE2")
	require.True(s.T(), ok)

	ttl, err := s.RDB.TTL(s.Ctx, lockKey).Result()
	require.NoError(s.T(), err, "TTL lock key")
	s.AssertTTLWithin(ttl, 1*time.Second, lockTTL)
}

// TestReleaseRedeemLock_Idempotent deletes a missing key, then acquires and
// deletes another key twice. It checks those sequential releases succeed in
// the exercised absence/release states.
func (s *RedeemCacheSuite) TestReleaseRedeemLock_Idempotent() {
	// Release a lock that doesn't exist should not error
	require.NoError(s.T(), s.cache.ReleaseRedeemLock(s.Ctx, "NONEXISTENT"))

	// Acquire, release, release again
	ok, err := s.cache.AcquireRedeemLock(s.Ctx, "IDEMPOTENT", 10*time.Second)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	require.NoError(s.T(), s.cache.ReleaseRedeemLock(s.Ctx, "IDEMPOTENT"))
	require.NoError(s.T(), s.cache.ReleaseRedeemLock(s.Ctx, "IDEMPOTENT"), "second release should be idempotent")
}

// TestRedeemCacheSuite runs the Redis-backed redemption suite with its
// inherited fixture lifecycle. It dispatches the counter, TTL and sequential
// lock cases against the fixture server.
func TestRedeemCacheSuite(t *testing.T) {
	suite.Run(t, new(RedeemCacheSuite))
}
