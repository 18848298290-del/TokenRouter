//go:build integration

package rediscache

import (
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type UserMsgQueueCacheSuite struct {
	IntegrationRedisSuite
	cache *userMsgQueueCache
}

func TestUserMsgQueueCacheSuite(t *testing.T) {
	suite.Run(t, new(UserMsgQueueCacheSuite))
}

func (s *UserMsgQueueCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	cache, ok := NewUserMsgQueueCache(s.rdb).(*userMsgQueueCache)
	s.Require().True(ok, "message queue cache constructor type")
	s.cache = cache
}

func (s *UserMsgQueueCacheSuite) TestAcquireLockWritesIndexAndReleaseRemovesIt() {
	providerID := int64(701)
	nowMs, err := s.cache.GetCurrentTimeMs(s.ctx)
	require.NoError(s.T(), err)

	acquired, err := s.cache.AcquireLock(s.ctx, providerID, "req-701", 10_000)
	require.NoError(s.T(), err)
	require.True(s.T(), acquired)

	score, err := s.rdb.ZScore(s.ctx, umqLockIndexKey, "701").Result()
	require.NoError(s.T(), err)
	require.Greater(s.T(), int64(score), nowMs)

	released, err := s.cache.ReleaseLock(s.ctx, providerID, "req-701")
	require.NoError(s.T(), err)
	require.True(s.T(), released)

	_, err = s.rdb.ZScore(s.ctx, umqLockIndexKey, "701").Result()
	require.ErrorIs(s.T(), err, redis.Nil)
}

func (s *UserMsgQueueCacheSuite) TestReconcileExpiredLockCandidatesRemovesNaturallyExpiredLockIndex() {
	providerID := int64(702)
	acquired, err := s.cache.AcquireLock(s.ctx, providerID, "req-702", 20)
	require.NoError(s.T(), err)
	require.True(s.T(), acquired)

	_, err = s.rdb.ZScore(s.ctx, umqLockIndexKey, "702").Result()
	require.NoError(s.T(), err)
	require.Eventually(s.T(), func() bool {
		_, err := s.rdb.Get(s.ctx, umqLockKey(providerID)).Result()
		return errors.Is(err, redis.Nil)
	}, time.Second, 10*time.Millisecond)

	cleaned, err := s.cache.ReconcileExpiredLockCandidates(s.ctx, 1000)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 0, cleaned)

	_, err = s.rdb.ZScore(s.ctx, umqLockIndexKey, "702").Result()
	require.ErrorIs(s.T(), err, redis.Nil)
}

func (s *UserMsgQueueCacheSuite) TestReconcileExpiredLockCandidatesRefreshesLiveLockIndex() {
	providerID := int64(703)
	nowMs, err := s.cache.GetCurrentTimeMs(s.ctx)
	require.NoError(s.T(), err)
	require.NoError(s.T(), s.rdb.Set(s.ctx, umqLockKey(providerID), "req-703", time.Minute).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, umqLockIndexKey, redis.Z{
		Score:  float64(nowMs - 1),
		Member: "703",
	}).Err())

	cleaned, err := s.cache.ReconcileExpiredLockCandidates(s.ctx, 1000)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 0, cleaned)

	score, err := s.rdb.ZScore(s.ctx, umqLockIndexKey, "703").Result()
	require.NoError(s.T(), err)
	require.Greater(s.T(), int64(score), nowMs)
	exists, err := s.rdb.Exists(s.ctx, umqLockKey(providerID)).Result()
	require.NoError(s.T(), err)
	require.EqualValues(s.T(), 1, exists)
}

func (s *UserMsgQueueCacheSuite) TestReconcileExpiredLockCandidatesDeletesNoTTLLock() {
	providerID := int64(704)
	nowMs, err := s.cache.GetCurrentTimeMs(s.ctx)
	require.NoError(s.T(), err)
	require.NoError(s.T(), s.rdb.Set(s.ctx, umqLockKey(providerID), "req-704", 0).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, umqLockIndexKey, redis.Z{
		Score:  float64(nowMs),
		Member: "704",
	}).Err())

	cleaned, err := s.cache.ReconcileExpiredLockCandidates(s.ctx, 1000)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 1, cleaned)

	exists, err := s.rdb.Exists(s.ctx, umqLockKey(providerID)).Result()
	require.NoError(s.T(), err)
	require.EqualValues(s.T(), 0, exists)
	_, err = s.rdb.ZScore(s.ctx, umqLockIndexKey, "704").Result()
	require.ErrorIs(s.T(), err, redis.Nil)
}

func (s *UserMsgQueueCacheSuite) TestReconcileExpiredLockCandidatesRemovesInvalidMember() {
	nowMs, err := s.cache.GetCurrentTimeMs(s.ctx)
	require.NoError(s.T(), err)
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, umqLockIndexKey, redis.Z{
		Score:  float64(nowMs),
		Member: "not-an-provider-id",
	}).Err())

	cleaned, err := s.cache.ReconcileExpiredLockCandidates(s.ctx, 1000)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 0, cleaned)

	_, err = s.rdb.ZScore(s.ctx, umqLockIndexKey, "not-an-provider-id").Result()
	require.True(s.T(), errors.Is(err, redis.Nil))
}

func (s *UserMsgQueueCacheSuite) TestAcquireLockBusyPathReindexesUnindexedLiveLock() {
	// Simulate a missing index entry after an upgrade, failed index write, or release race.
	// The lock has a TTL but no index member.
	providerID := int64(705)
	nowMs, err := s.cache.GetCurrentTimeMs(s.ctx)
	require.NoError(s.T(), err)
	require.NoError(s.T(), s.rdb.Set(s.ctx, umqLockKey(providerID), "holder-705", time.Minute).Err())

	// A failed contention attempt repairs the observed owner's index entry.
	acquired, err := s.cache.AcquireLock(s.ctx, providerID, "contender-705", 10_000)
	require.NoError(s.T(), err)
	require.False(s.T(), acquired)

	score, err := s.rdb.ZScore(s.ctx, umqLockIndexKey, "705").Result()
	require.NoError(s.T(), err, "busy acquire should re-index the observed live lock")
	require.Greater(s.T(), int64(score), nowMs)
	// The contender leaves the lock owner unchanged.
	val, err := s.rdb.Get(s.ctx, umqLockKey(providerID)).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), "holder-705", val)
}

func (s *UserMsgQueueCacheSuite) TestAcquireLockBusyPathMakesNoTTLLockReconcilable() {
	// An unindexed persistent lock would be invisible to reconciliation.
	// Failed acquisition indexes it as already due so background cleanup can remove it.
	providerID := int64(706)
	require.NoError(s.T(), s.rdb.Set(s.ctx, umqLockKey(providerID), "holder-706", 0).Err())

	acquired, err := s.cache.AcquireLock(s.ctx, providerID, "contender-706", 10_000)
	require.NoError(s.T(), err)
	require.False(s.T(), acquired)

	nowMs, err := s.cache.GetCurrentTimeMs(s.ctx)
	require.NoError(s.T(), err)
	score, err := s.rdb.ZScore(s.ctx, umqLockIndexKey, "706").Result()
	require.NoError(s.T(), err, "busy acquire should index the anomalous lock")
	require.LessOrEqual(s.T(), int64(score), nowMs, "anomalous lock should be an immediately-expired candidate")

	cleaned, err := s.cache.ReconcileExpiredLockCandidates(s.ctx, 1000)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 1, cleaned, "reconcile should delete the no-TTL lock")

	exists, err := s.rdb.Exists(s.ctx, umqLockKey(providerID)).Result()
	require.NoError(s.T(), err)
	require.EqualValues(s.T(), 0, exists, "queue is unblocked after reconcile")
	_, err = s.rdb.ZScore(s.ctx, umqLockIndexKey, "706").Result()
	require.ErrorIs(s.T(), err, redis.Nil)
}
