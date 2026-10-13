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

// Message-lock maintenance relies on a separately written expiry index, which
// can be missing or stale while a lock remains in Redis. Integration cases
// check index creation/removal, naturally expired and persistent locks,
// live-score repair, malformed members and discovery repair during
// contention.

// UserMsgQueueCacheSuite exercises serialization lock indexing and cleanup in
// namespaced Redis fixtures.
type UserMsgQueueCacheSuite struct {
	IntegrationRedisSuite // namespaced Redis test fixture
	// cache is rebuilt after SetupTest creates the fixture client, giving each
	// method independent lock and index keys for its maintenance assertions.
	cache *userMsgQueueCache // message lock test subject
}

// TestUserMsgQueueCacheSuite runs serialization ownership and index
// maintenance against the integration Redis fixture. Every method receives
// its own key namespace and automatic fixture cleanup.
func TestUserMsgQueueCacheSuite(t *testing.T) {
	suite.Run(t, new(UserMsgQueueCacheSuite))
}

// SetupTest creates the namespaced Redis fixture and asserts the
// constructor's concrete message-lock cache type. It keeps that adapter for
// maintenance methods absent from the public interface.
func (s *UserMsgQueueCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	cache, ok := NewUserMsgQueueCache(s.rdb).(*userMsgQueueCache)
	s.Require().True(ok, "message queue cache constructor type")
	s.cache = cache
}

// TestAcquireLockWritesIndexAndReleaseRemovesIt acquires and releases one
// request owner. It checks that acquisition creates a future expiry score and
// successful release removes that provider's index member.
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

// TestReconcileExpiredLockCandidatesRemovesNaturallyExpiredLockIndex lets a
// short-lived lock expire before reconciliation. It checks removal of its
// index member with zero persistent-lock deletions returned.
func (s *UserMsgQueueCacheSuite) TestReconcileExpiredLockCandidatesRemovesNaturallyExpiredLockIndex() {
	providerID := int64(702)
	acquired, err := s.cache.AcquireLock(s.ctx, providerID, "req-702", 20)
	require.NoError(s.T(), err)
	require.True(s.T(), acquired)

	_, err = s.rdb.ZScore(s.ctx, umqLockIndexKey, "702").Result()
	require.NoError(s.T(), err)
	// Observe key expiry before running reconciliation so this scenario tests
	// stale index removal rather than deletion of an existing persistent lock.
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

// TestReconcileExpiredLockCandidatesRefreshesLiveLockIndex seeds a live lock
// with an already due score. It checks a future replacement score, retained
// lock key and zero deletion count.
func (s *UserMsgQueueCacheSuite) TestReconcileExpiredLockCandidatesRefreshesLiveLockIndex() {
	providerID := int64(703)
	nowMs, err := s.cache.GetCurrentTimeMs(s.ctx)
	require.NoError(s.T(), err)
	require.NoError(s.T(), s.rdb.Set(s.ctx, umqLockKey(providerID), "req-703", time.Minute).Err())
	// Seed an already due discovery score for a still-live lock so maintenance
	// must recheck remaining PTTL before deciding whether cleanup is needed.
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

// TestReconcileExpiredLockCandidatesDeletesNoTTLLock seeds a persistent lock
// and due index member. It checks one counted deletion and removal of both
// the lock and its discovery member.
func (s *UserMsgQueueCacheSuite) TestReconcileExpiredLockCandidatesDeletesNoTTLLock() {
	providerID := int64(704)
	nowMs, err := s.cache.GetCurrentTimeMs(s.ctx)
	require.NoError(s.T(), err)
	// Seed a persistent owner plus a due index entry to make the no-TTL anomaly
	// discoverable by the normal candidate reconciliation path.
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

// TestReconcileExpiredLockCandidatesRemovesInvalidMember seeds a due
// nonnumeric index member. It checks successful removal with zero deleted
// persistent locks, protecting maintenance from malformed discovery
// identities.
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

// TestAcquireLockBusyPathReindexesUnindexedLiveLock seeds a live owner
// without a discovery member. It checks that rejected contention restores a
// future index score while retaining the existing owner.
func (s *UserMsgQueueCacheSuite) TestAcquireLockBusyPathReindexesUnindexedLiveLock() {
	// Seed an existing holder with an expiring lock but no index member so
	// rejected contention has discovery state to repair.
	providerID := int64(705)
	nowMs, err := s.cache.GetCurrentTimeMs(s.ctx)
	require.NoError(s.T(), err)
	require.NoError(s.T(), s.rdb.Set(s.ctx, umqLockKey(providerID), "holder-705", time.Minute).Err())

	// Attempt acquisition as a different owner so the returned rejection can be
	// checked together with a newly indexed observation of the existing lock.
	acquired, err := s.cache.AcquireLock(s.ctx, providerID, "contender-705", 10_000)
	require.NoError(s.T(), err)
	require.False(s.T(), acquired)

	score, err := s.rdb.ZScore(s.ctx, umqLockIndexKey, "705").Result()
	require.NoError(s.T(), err, "busy acquire should re-index the observed live lock")
	require.Greater(s.T(), int64(score), nowMs)
	val, err := s.rdb.Get(s.ctx, umqLockKey(providerID)).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), "holder-705", val)
}

// TestAcquireLockBusyPathMakesNoTTLLockReconcilable seeds an unindexed
// persistent owner. It checks rejected contention indexing that lock as due,
// followed by one counted deletion and removal of its index member.
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
