//go:build integration

package rediscache

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

// testSlotTTLMinutes matches the default fifteen-minute lifetime.
const testSlotTTLMinutes = 15

// testSlotTTL is the duration used in TTL assertions.
var testSlotTTL = time.Duration(testSlotTTLMinutes) * time.Minute

type ConcurrencyCacheSuite struct {
	IntegrationRedisSuite
	cache    scheduler.ConcurrencyCache
	rawCache *concurrencyCache
}

type apiKeyConcurrencyCacheForTest interface {
	TrackAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error
	ReleaseAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error
	GetAPIKeyConcurrencyBatch(ctx context.Context, apiKeyIDs []int64) (map[int64]int, error)
}

// waitIncrementFault injects admission transport errors while reads and releases use test Redis.
type waitIncrementFault struct{ scheduler.ConcurrencyCache }

func TestConcurrencyCacheSuite(t *testing.T) {
	suite.Run(t, new(ConcurrencyCacheSuite))
}

func (s *ConcurrencyCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	cache, ok := NewConcurrencyCache(s.rdb, testSlotTTLMinutes, int(testSlotTTL.Seconds())).(*concurrencyCache)
	s.Require().True(ok, "concurrency cache constructor type")
	s.rawCache = cache
	s.cache = s.rawCache
}

func (s *ConcurrencyCacheSuite) apiKeyConcurrencyCache() apiKeyConcurrencyCacheForTest {
	cache, ok := s.cache.(apiKeyConcurrencyCacheForTest)
	require.True(s.T(), ok)
	return cache
}

func (s *ConcurrencyCacheSuite) TestOpenAIWSIngressAPIKeySlot_HardLimitRefreshAndRelease() {
	apiKeyID := int64(9011)
	firstLeaseID := "ingress-first"
	secondLeaseID := "ingress-second"

	ok, err := s.rawCache.AcquireOpenAIWSIngressLease(s.ctx, apiKeyID, 1, firstLeaseID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	ok, err = s.rawCache.AcquireOpenAIWSIngressLease(s.ctx, apiKeyID, 1, secondLeaseID)
	require.NoError(s.T(), err)
	require.False(s.T(), ok, "a second live session must not exceed the API key limit")

	ok, err = s.rawCache.RefreshOpenAIWSIngressLease(s.ctx, apiKeyID, firstLeaseID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok, "the current owner must be able to refresh its lease")

	require.NoError(s.T(), s.rawCache.ReleaseOpenAIWSIngressLease(s.ctx, apiKeyID, firstLeaseID))
	ok, err = s.rawCache.AcquireOpenAIWSIngressLease(s.ctx, apiKeyID, 1, secondLeaseID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok, "released capacity must become available immediately")
}

func (s *ConcurrencyCacheSuite) TestOpenAIWSIngressAPIKeySlot_ReapsCrashedLeaseWithoutDeletingLiveOtherInstance() {
	apiKeyID := int64(9012)
	key := openAIWSIngressLeaseKey(apiKeyID)
	now, err := s.rawCache.redisUnixSeconds(s.ctx)
	require.NoError(s.T(), err)
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, key,
		redis.Z{Score: float64(now - openAIWSIngressLeaseTTLSeconds - 1), Member: "crashed-instance"},
		redis.Z{Score: float64(now), Member: "live-other-instance"},
	).Err())
	require.NoError(s.T(), s.rdb.Expire(s.ctx, key, time.Duration(openAIWSIngressLeaseTTLSeconds)*time.Second).Err())

	ok, err := s.rawCache.AcquireOpenAIWSIngressLease(s.ctx, apiKeyID, 2, "new-instance")
	require.NoError(s.T(), err)
	require.True(s.T(), ok, "the crashed member should be reaped before enforcing the limit")

	_, err = s.rdb.ZScore(s.ctx, key, "crashed-instance").Result()
	require.ErrorIs(s.T(), err, redis.Nil)
	_, err = s.rdb.ZScore(s.ctx, key, "live-other-instance").Result()
	require.NoError(s.T(), err, "a live lease owned by another instance must be preserved")
	count, err := s.rdb.ZCard(s.ctx, key).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(2), count)
}

func (s *ConcurrencyCacheSuite) TestLiveLease_CountsTowardRegularProviderAndUserLimits() {
	liveCache, ok := s.cache.(scheduler.LiveConcurrencyCache)
	require.True(s.T(), ok)

	providerID := int64(9101)
	userID := int64(9102)
	apiKeyID := int64(9103)
	acquired, err := liveCache.AcquireLiveLease(
		s.ctx,
		providerID,
		1,
		userID,
		1,
		apiKeyID,
		"live-integration",
		false,
	)
	require.NoError(s.T(), err)
	require.True(s.T(), acquired)

	regularProvider, err := s.cache.AcquireProviderSlot(s.ctx, providerID, 1, "regular-provider")
	require.NoError(s.T(), err)
	require.False(s.T(), regularProvider)
	regularUser, err := s.cache.AcquireUserSlot(s.ctx, userID, 1, "regular-user")
	require.NoError(s.T(), err)
	require.False(s.T(), regularUser)

	refreshed, err := liveCache.RefreshLiveLease(s.ctx, providerID, userID, apiKeyID, "live-integration")
	require.NoError(s.T(), err)
	require.True(s.T(), refreshed)
	require.NoError(s.T(), liveCache.ReleaseLiveLease(s.ctx, providerID, userID, apiKeyID, "live-integration"))
	regularProvider, err = s.cache.AcquireProviderSlot(s.ctx, providerID, 1, "regular-provider")
	require.NoError(s.T(), err)
	require.True(s.T(), regularProvider)
}

func (s *ConcurrencyCacheSuite) TestProviderSlot_AcquireAndRelease() {
	providerID := int64(10)
	reqID1, reqID2, reqID3 := "req1", "req2", "req3"

	ok, err := s.cache.AcquireProviderSlot(s.ctx, providerID, 2, reqID1)
	require.NoError(s.T(), err, "AcquireProviderSlot 1")
	require.True(s.T(), ok)

	ok, err = s.cache.AcquireProviderSlot(s.ctx, providerID, 2, reqID2)
	require.NoError(s.T(), err, "AcquireProviderSlot 2")
	require.True(s.T(), ok)

	ok, err = s.cache.AcquireProviderSlot(s.ctx, providerID, 2, reqID3)
	require.NoError(s.T(), err, "AcquireProviderSlot 3")
	require.False(s.T(), ok, "expected third acquire to fail")

	cur, err := s.cache.GetProviderConcurrency(s.ctx, providerID)
	require.NoError(s.T(), err, "GetProviderConcurrency")
	require.Equal(s.T(), 2, cur, "concurrency mismatch")

	require.NoError(s.T(), s.cache.ReleaseProviderSlot(s.ctx, providerID, reqID1), "ReleaseProviderSlot")

	cur, err = s.cache.GetProviderConcurrency(s.ctx, providerID)
	require.NoError(s.T(), err, "GetProviderConcurrency after release")
	require.Equal(s.T(), 1, cur, "expected 1 after release")
}

func (s *ConcurrencyCacheSuite) TestProviderActiveIndex_AcquireAndRelease() {
	providerID := int64(610)
	member := strconv.FormatInt(providerID, 10)
	reqID := "active-index-req"

	now, err := s.rawCache.redisUnixSeconds(s.ctx)
	require.NoError(s.T(), err)

	ok, err := s.cache.AcquireProviderSlot(s.ctx, providerID, 2, reqID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	score, err := s.rdb.ZScore(s.ctx, providerActiveIndexKey, member).Result()
	require.NoError(s.T(), err)
	require.Greater(s.T(), int64(score), now, "index score should be a future expiry")

	require.NoError(s.T(), s.cache.ReleaseProviderSlot(s.ctx, providerID, reqID))

	_, err = s.rdb.ZScore(s.ctx, providerActiveIndexKey, member).Result()
	require.ErrorIs(s.T(), err, redis.Nil, "index member should be removed after load drops to zero")
}

func (s *ConcurrencyCacheSuite) TestProviderActiveIndex_WaitLifecycle() {
	providerID := int64(611)
	member := strconv.FormatInt(providerID, 10)

	ok, err := s.cache.IncrementProviderWaitCount(s.ctx, providerID, 2)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	_, err = s.rdb.ZScore(s.ctx, providerActiveIndexKey, member).Result()
	require.NoError(s.T(), err, "wait increment should register index member")

	require.NoError(s.T(), s.cache.DecrementProviderWaitCount(s.ctx, providerID))

	_, err = s.rdb.ZScore(s.ctx, providerActiveIndexKey, member).Result()
	require.ErrorIs(s.T(), err, redis.Nil, "index member should be removed after wait drops to zero")
}

func (s *ConcurrencyCacheSuite) TestUserActiveIndex_AcquireAndRelease() {
	userID := int64(612)
	member := strconv.FormatInt(userID, 10)
	reqID := "user-active-index-req"

	ok, err := s.cache.AcquireUserSlot(s.ctx, userID, 2, reqID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	_, err = s.rdb.ZScore(s.ctx, userActiveIndexKey, member).Result()
	require.NoError(s.T(), err, "acquire should register user index member")

	require.NoError(s.T(), s.cache.ReleaseUserSlot(s.ctx, userID, reqID))

	_, err = s.rdb.ZScore(s.ctx, userActiveIndexKey, member).Result()
	require.ErrorIs(s.T(), err, redis.Nil, "user index member should be removed after release")
}

func (s *ConcurrencyCacheSuite) TestProviderSlot_TTL() {
	providerID := int64(11)
	reqID := "req_ttl_test"
	slotKey := fmt.Sprintf("%s%d", providerSlotKeyPrefix, providerID)

	ok, err := s.cache.AcquireProviderSlot(s.ctx, providerID, 5, reqID)
	require.NoError(s.T(), err, "AcquireProviderSlot")
	require.True(s.T(), ok)

	ttl, err := s.rdb.TTL(s.ctx, slotKey).Result()
	require.NoError(s.T(), err, "TTL")
	s.AssertTTLWithin(ttl, 1*time.Second, testSlotTTL)
}

func (s *ConcurrencyCacheSuite) TestProviderSlot_DuplicateReqID() {
	providerID := int64(12)
	reqID := "dup-req"

	ok, err := s.cache.AcquireProviderSlot(s.ctx, providerID, 2, reqID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	// Acquiring with same reqID should be idempotent
	ok, err = s.cache.AcquireProviderSlot(s.ctx, providerID, 2, reqID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	cur, err := s.cache.GetProviderConcurrency(s.ctx, providerID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 1, cur, "expected concurrency=1 (idempotent)")
}

func (s *ConcurrencyCacheSuite) TestProviderSlot_ReleaseIdempotent() {
	providerID := int64(13)
	reqID := "release-test"

	ok, err := s.cache.AcquireProviderSlot(s.ctx, providerID, 1, reqID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	require.NoError(s.T(), s.cache.ReleaseProviderSlot(s.ctx, providerID, reqID), "ReleaseProviderSlot")
	// Releasing again should not error
	require.NoError(s.T(), s.cache.ReleaseProviderSlot(s.ctx, providerID, reqID), "ReleaseProviderSlot again")
	// Releasing non-existent should not error
	require.NoError(s.T(), s.cache.ReleaseProviderSlot(s.ctx, providerID, "non-existent"), "ReleaseProviderSlot non-existent")

	cur, err := s.cache.GetProviderConcurrency(s.ctx, providerID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 0, cur)
}

func (s *ConcurrencyCacheSuite) TestProviderSlot_MaxZero() {
	providerID := int64(14)
	reqID := "max-zero-test"

	ok, err := s.cache.AcquireProviderSlot(s.ctx, providerID, 0, reqID)
	require.NoError(s.T(), err)
	require.False(s.T(), ok, "expected acquire to fail with max=0")
}

func (s *ConcurrencyCacheSuite) TestUserSlot_AcquireAndRelease() {
	userID := int64(42)
	reqID1, reqID2 := "req1", "req2"

	ok, err := s.cache.AcquireUserSlot(s.ctx, userID, 1, reqID1)
	require.NoError(s.T(), err, "AcquireUserSlot")
	require.True(s.T(), ok)

	ok, err = s.cache.AcquireUserSlot(s.ctx, userID, 1, reqID2)
	require.NoError(s.T(), err, "AcquireUserSlot 2")
	require.False(s.T(), ok, "expected second acquire to fail at max=1")

	cur, err := s.cache.GetUserConcurrency(s.ctx, userID)
	require.NoError(s.T(), err, "GetUserConcurrency")
	require.Equal(s.T(), 1, cur, "expected concurrency=1")

	require.NoError(s.T(), s.cache.ReleaseUserSlot(s.ctx, userID, reqID1), "ReleaseUserSlot")
	// Releasing a non-existent slot should not error
	require.NoError(s.T(), s.cache.ReleaseUserSlot(s.ctx, userID, "non-existent"), "ReleaseUserSlot non-existent")

	cur, err = s.cache.GetUserConcurrency(s.ctx, userID)
	require.NoError(s.T(), err, "GetUserConcurrency after release")
	require.Equal(s.T(), 0, cur, "expected concurrency=0 after release")
}

func (s *ConcurrencyCacheSuite) TestUserSlot_TTL() {
	userID := int64(200)
	reqID := "req_ttl_test"
	slotKey := fmt.Sprintf("%s%d", userSlotKeyPrefix, userID)

	ok, err := s.cache.AcquireUserSlot(s.ctx, userID, 5, reqID)
	require.NoError(s.T(), err, "AcquireUserSlot")
	require.True(s.T(), ok)

	ttl, err := s.rdb.TTL(s.ctx, slotKey).Result()
	require.NoError(s.T(), err, "TTL")
	s.AssertTTLWithin(ttl, 1*time.Second, testSlotTTL)
}

func (s *ConcurrencyCacheSuite) TestAPIKeySlot_TrackReleaseAndBatchCount() {
	cache := s.apiKeyConcurrencyCache()
	apiKeyID := int64(300)
	emptyAPIKeyID := int64(301)
	slotKey := fmt.Sprintf("%s%d", apiKeySlotKeyPrefix, apiKeyID)

	require.NoError(s.T(), cache.TrackAPIKeySlot(s.ctx, apiKeyID, "req1"))
	require.NoError(s.T(), cache.TrackAPIKeySlot(s.ctx, apiKeyID, "req2"))

	counts, err := cache.GetAPIKeyConcurrencyBatch(s.ctx, []int64{apiKeyID, emptyAPIKeyID})
	require.NoError(s.T(), err)
	require.Equal(s.T(), map[int64]int{apiKeyID: 2, emptyAPIKeyID: 0}, counts)

	ttl, err := s.rdb.TTL(s.ctx, slotKey).Result()
	require.NoError(s.T(), err, "TTL")
	s.AssertTTLWithin(ttl, 1*time.Second, testSlotTTL)

	require.NoError(s.T(), cache.ReleaseAPIKeySlot(s.ctx, apiKeyID, "req1"))
	counts, err = cache.GetAPIKeyConcurrencyBatch(s.ctx, []int64{apiKeyID})
	require.NoError(s.T(), err)
	require.Equal(s.T(), 1, counts[apiKeyID])

	require.NoError(s.T(), cache.ReleaseAPIKeySlot(s.ctx, apiKeyID, "req2"))
	counts, err = cache.GetAPIKeyConcurrencyBatch(s.ctx, []int64{apiKeyID})
	require.NoError(s.T(), err)
	require.Equal(s.T(), 0, counts[apiKeyID])
}

func (s *ConcurrencyCacheSuite) TestWaitQueue_IncrementAndDecrement() {
	userID := int64(20)
	waitKey := fmt.Sprintf("%s%d", waitQueueKeyPrefix, userID)

	ok, err := s.cache.IncrementWaitCount(s.ctx, userID, 2)
	require.NoError(s.T(), err, "IncrementWaitCount 1")
	require.True(s.T(), ok)

	ok, err = s.cache.IncrementWaitCount(s.ctx, userID, 2)
	require.NoError(s.T(), err, "IncrementWaitCount 2")
	require.True(s.T(), ok)

	ok, err = s.cache.IncrementWaitCount(s.ctx, userID, 2)
	require.NoError(s.T(), err, "IncrementWaitCount 3")
	require.False(s.T(), ok, "expected wait increment over max to fail")

	ttl, err := s.rdb.TTL(s.ctx, waitKey).Result()
	require.NoError(s.T(), err, "TTL waitKey")
	s.AssertTTLWithin(ttl, 1*time.Second, testSlotTTL)

	require.NoError(s.T(), s.cache.DecrementWaitCount(s.ctx, userID), "DecrementWaitCount")

	val, err := s.rdb.Get(s.ctx, waitKey).Int()
	if !errors.Is(err, redis.Nil) {
		require.NoError(s.T(), err, "Get waitKey")
	}
	require.Equal(s.T(), 1, val, "expected wait count 1")
}

func (s *ConcurrencyCacheSuite) TestWaitQueue_DecrementNoNegative() {
	userID := int64(300)
	waitKey := fmt.Sprintf("%s%d", waitQueueKeyPrefix, userID)

	// Test decrement on non-existent key - should not error and should not create negative value
	require.NoError(s.T(), s.cache.DecrementWaitCount(s.ctx, userID), "DecrementWaitCount on non-existent key")

	// Verify no key was created or it's not negative
	val, err := s.rdb.Get(s.ctx, waitKey).Int()
	if !errors.Is(err, redis.Nil) {
		require.NoError(s.T(), err, "Get waitKey")
	}
	require.GreaterOrEqual(s.T(), val, 0, "expected non-negative wait count after decrement on empty")

	// Set count to 1, then decrement twice
	ok, err := s.cache.IncrementWaitCount(s.ctx, userID, 5)
	require.NoError(s.T(), err, "IncrementWaitCount")
	require.True(s.T(), ok)

	// Decrement once (1 -> 0)
	require.NoError(s.T(), s.cache.DecrementWaitCount(s.ctx, userID), "DecrementWaitCount")

	// Decrement again on 0 - should not go negative
	require.NoError(s.T(), s.cache.DecrementWaitCount(s.ctx, userID), "DecrementWaitCount on zero")

	// Check that repeated decrements keep the count nonnegative.
	val, err = s.rdb.Get(s.ctx, waitKey).Int()
	if !errors.Is(err, redis.Nil) {
		require.NoError(s.T(), err, "Get waitKey after double decrement")
	}
	require.GreaterOrEqual(s.T(), val, 0, "expected non-negative wait count")
}

func (s *ConcurrencyCacheSuite) TestProviderWaitQueue_IncrementAndDecrement() {
	providerID := int64(30)
	waitKey := fmt.Sprintf("%s%d", providerWaitKeyPrefix, providerID)

	ok, err := s.cache.IncrementProviderWaitCount(s.ctx, providerID, 2)
	require.NoError(s.T(), err, "IncrementProviderWaitCount 1")
	require.True(s.T(), ok)

	ok, err = s.cache.IncrementProviderWaitCount(s.ctx, providerID, 2)
	require.NoError(s.T(), err, "IncrementProviderWaitCount 2")
	require.True(s.T(), ok)

	ok, err = s.cache.IncrementProviderWaitCount(s.ctx, providerID, 2)
	require.NoError(s.T(), err, "IncrementProviderWaitCount 3")
	require.False(s.T(), ok, "expected provider wait increment over max to fail")

	ttl, err := s.rdb.TTL(s.ctx, waitKey).Result()
	require.NoError(s.T(), err, "TTL provider waitKey")
	s.AssertTTLWithin(ttl, 1*time.Second, testSlotTTL)

	require.NoError(s.T(), s.cache.DecrementProviderWaitCount(s.ctx, providerID), "DecrementProviderWaitCount")

	val, err := s.rdb.Get(s.ctx, waitKey).Int()
	if !errors.Is(err, redis.Nil) {
		require.NoError(s.T(), err, "Get waitKey")
	}
	require.Equal(s.T(), 1, val, "expected provider wait count 1")
}

func (s *ConcurrencyCacheSuite) TestCleanupStaleProcessSlots() {
	// Preseed the migration marker to exercise index cleanup independently of the legacy sweep.
	require.NoError(s.T(), s.rdb.Set(s.ctx, legacyWaitSweepMarkerKey, "1", 0).Err())
	providerID := int64(901)
	userID := int64(902)
	apiKeyID := int64(903)
	unindexedProviderID := int64(1901)
	providerKey := fmt.Sprintf("%s%d", providerSlotKeyPrefix, providerID)
	userKey := fmt.Sprintf("%s%d", userSlotKeyPrefix, userID)
	apiKeyKey := fmt.Sprintf("%s%d", apiKeySlotKeyPrefix, apiKeyID)
	unindexedProviderKey := fmt.Sprintf("%s%d", providerSlotKeyPrefix, unindexedProviderID)
	userWaitKey := fmt.Sprintf("%s%d", waitQueueKeyPrefix, userID)
	providerWaitKey := fmt.Sprintf("%s%d", providerWaitKeyPrefix, providerID)
	unindexedProviderWaitKey := fmt.Sprintf("%s%d", providerWaitKeyPrefix, unindexedProviderID)

	now, err := s.rawCache.redisUnixSeconds(s.ctx)
	require.NoError(s.T(), err)
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, providerKey,
		redis.Z{Score: float64(now), Member: "oldproc-1"},
		redis.Z{Score: float64(now), Member: "keep-1"},
	).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, userKey,
		redis.Z{Score: float64(now), Member: "oldproc-2"},
		redis.Z{Score: float64(now), Member: "keep-2"},
	).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, unindexedProviderKey,
		redis.Z{Score: float64(now), Member: "oldproc-unindexed"},
	).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, apiKeyKey,
		redis.Z{Score: float64(now), Member: "oldproc-3"},
		redis.Z{Score: float64(now), Member: "keep-3"},
	).Err())
	require.NoError(s.T(), s.rdb.Set(s.ctx, userWaitKey, 3, time.Minute).Err())
	require.NoError(s.T(), s.rdb.Set(s.ctx, providerWaitKey, 2, time.Minute).Err())
	require.NoError(s.T(), s.rdb.Set(s.ctx, unindexedProviderWaitKey, 2, time.Minute).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, providerActiveIndexKey, redis.Z{
		Score:  float64(now + 60),
		Member: strconv.FormatInt(providerID, 10),
	}).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, userActiveIndexKey, redis.Z{
		Score:  float64(now + 60),
		Member: strconv.FormatInt(userID, 10),
	}).Err())

	require.NoError(s.T(), s.cache.CleanupStaleProcessSlots(s.ctx, "keep-"))

	providerMembers, err := s.rdb.ZRange(s.ctx, providerKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), []string{"keep-1"}, providerMembers)

	userMembers, err := s.rdb.ZRange(s.ctx, userKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), []string{"keep-2"}, userMembers)

	// API key statistics slots are cleaned by score pruning and key TTL.
	apiKeyMembers, err := s.rdb.ZRange(s.ctx, apiKeyKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.ElementsMatch(s.T(), []string{"keep-3", "oldproc-3"}, apiKeyMembers)

	_, err = s.rdb.Get(s.ctx, userWaitKey).Result()
	require.True(s.T(), errors.Is(err, redis.Nil))

	_, err = s.rdb.Get(s.ctx, providerWaitKey).Result()
	require.True(s.T(), errors.Is(err, redis.Nil))

	unindexedMembers, err := s.rdb.ZRange(s.ctx, unindexedProviderKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), []string{"oldproc-unindexed"}, unindexedMembers)
	_, err = s.rdb.Get(s.ctx, unindexedProviderWaitKey).Result()
	require.NoError(s.T(), err)
}

func (s *ConcurrencyCacheSuite) TestGetProviderConcurrency_Missing() {
	// When no slots exist, GetProviderConcurrency should return 0
	cur, err := s.cache.GetProviderConcurrency(s.ctx, 999)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 0, cur)
}

func (s *ConcurrencyCacheSuite) TestGetUserConcurrency_Missing() {
	// When no slots exist, GetUserConcurrency should return 0
	cur, err := s.cache.GetUserConcurrency(s.ctx, 999)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 0, cur)
}

func (s *ConcurrencyCacheSuite) TestGetProvidersLoadBatch() {
	// Setup: Create providers with different load states
	provider1 := int64(100)
	provider2 := int64(101)
	provider3 := int64(102)

	// Provider 1: 2/3 slots used, 1 waiting
	ok, err := s.cache.AcquireProviderSlot(s.ctx, provider1, 3, "req1")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	ok, err = s.cache.AcquireProviderSlot(s.ctx, provider1, 3, "req2")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	ok, err = s.cache.IncrementProviderWaitCount(s.ctx, provider1, 5)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	// Provider 2: 1/2 slots used, 0 waiting
	ok, err = s.cache.AcquireProviderSlot(s.ctx, provider2, 2, "req3")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	// Provider 3: 0/1 slots used, 0 waiting (idle)

	// Query batch load
	providers := []scheduler.ProviderWithConcurrency{
		{ID: provider1, MaxConcurrency: 3},
		{ID: provider2, MaxConcurrency: 2},
		{ID: provider3, MaxConcurrency: 1},
	}

	loadMap, err := s.cache.GetProvidersLoadBatch(s.ctx, providers)
	require.NoError(s.T(), err)
	require.Len(s.T(), loadMap, 3)

	// Verify provider1: (2 + 1) / 3 = 100%
	load1 := loadMap[provider1]
	require.NotNil(s.T(), load1)
	require.Equal(s.T(), provider1, load1.ProviderID)
	require.Equal(s.T(), 2, load1.CurrentConcurrency)
	require.Equal(s.T(), 1, load1.WaitingCount)
	require.Equal(s.T(), 100, load1.LoadRate)

	// Verify provider2: (1 + 0) / 2 = 50%
	load2 := loadMap[provider2]
	require.NotNil(s.T(), load2)
	require.Equal(s.T(), provider2, load2.ProviderID)
	require.Equal(s.T(), 1, load2.CurrentConcurrency)
	require.Equal(s.T(), 0, load2.WaitingCount)
	require.Equal(s.T(), 50, load2.LoadRate)

	// Verify provider3: (0 + 0) / 1 = 0%
	load3 := loadMap[provider3]
	require.NotNil(s.T(), load3)
	require.Equal(s.T(), provider3, load3.ProviderID)
	require.Equal(s.T(), 0, load3.CurrentConcurrency)
	require.Equal(s.T(), 0, load3.WaitingCount)
	require.Equal(s.T(), 0, load3.LoadRate)
}

func (s *ConcurrencyCacheSuite) TestGetProvidersLoadBatch_Empty() {
	// Test with empty provider list
	loadMap, err := s.cache.GetProvidersLoadBatch(s.ctx, []scheduler.ProviderWithConcurrency{})
	require.NoError(s.T(), err)
	require.Empty(s.T(), loadMap)
}

func (s *ConcurrencyCacheSuite) TestCleanupExpiredProviderSlots() {
	providerID := int64(200)
	slotKey := fmt.Sprintf("%s%d", providerSlotKeyPrefix, providerID)

	// Acquire 3 slots
	ok, err := s.cache.AcquireProviderSlot(s.ctx, providerID, 5, "req1")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	ok, err = s.cache.AcquireProviderSlot(s.ctx, providerID, 5, "req2")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	ok, err = s.cache.AcquireProviderSlot(s.ctx, providerID, 5, "req3")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	// Verify 3 slots exist
	cur, err := s.cache.GetProviderConcurrency(s.ctx, providerID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 3, cur)

	// Manually set old timestamps for req1 and req2 (simulate expired slots)
	now := time.Now().Unix()
	expiredTime := now - int64(testSlotTTL.Seconds()) - 10 // 10 seconds past TTL
	err = s.rdb.ZAdd(s.ctx, slotKey, redis.Z{Score: float64(expiredTime), Member: "req1"}).Err()
	require.NoError(s.T(), err)
	err = s.rdb.ZAdd(s.ctx, slotKey, redis.Z{Score: float64(expiredTime), Member: "req2"}).Err()
	require.NoError(s.T(), err)

	// Run cleanup
	err = s.cache.CleanupExpiredProviderSlots(s.ctx, providerID)
	require.NoError(s.T(), err)

	// Verify only 1 slot remains (req3)
	cur, err = s.cache.GetProviderConcurrency(s.ctx, providerID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 1, cur)

	// Verify req3 still exists
	members, err := s.rdb.ZRange(s.ctx, slotKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.Len(s.T(), members, 1)
	require.Equal(s.T(), "req3", members[0])
}

func (s *ConcurrencyCacheSuite) TestCleanupExpiredProviderSlots_NoExpired() {
	providerID := int64(201)

	// Acquire 2 fresh slots
	ok, err := s.cache.AcquireProviderSlot(s.ctx, providerID, 5, "req1")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	ok, err = s.cache.AcquireProviderSlot(s.ctx, providerID, 5, "req2")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	// Run cleanup (should not remove anything)
	err = s.cache.CleanupExpiredProviderSlots(s.ctx, providerID)
	require.NoError(s.T(), err)

	// Verify both slots still exist
	cur, err := s.cache.GetProviderConcurrency(s.ctx, providerID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 2, cur)
}

func (s *ConcurrencyCacheSuite) TestCleanupExpiredProviderSlotKeys() {
	now, err := s.rawCache.redisUnixSeconds(s.ctx)
	require.NoError(s.T(), err)
	expiredTime := now - int64(testSlotTTL.Seconds()) - 10
	providerKeyWithFresh := fmt.Sprintf("%s%d", providerSlotKeyPrefix, 301)
	providerKeyExpiredOnly := fmt.Sprintf("%s%d", providerSlotKeyPrefix, 302)
	userKey := fmt.Sprintf("%s%d", userSlotKeyPrefix, 303)
	unindexedProviderKey := fmt.Sprintf("%s%d", providerSlotKeyPrefix, 304)

	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, providerKeyWithFresh,
		redis.Z{Score: float64(expiredTime), Member: "expired"},
		redis.Z{Score: float64(now), Member: "fresh"},
	).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, providerKeyExpiredOnly,
		redis.Z{Score: float64(expiredTime), Member: "expired-only"},
	).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, userKey,
		redis.Z{Score: float64(expiredTime), Member: "user-expired"},
	).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, unindexedProviderKey,
		redis.Z{Score: float64(expiredTime), Member: "unindexed-expired"},
	).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, providerActiveIndexKey,
		redis.Z{Score: float64(now), Member: "301"},
		redis.Z{Score: float64(now), Member: "302"},
	).Err())

	require.NoError(s.T(), s.cache.CleanupExpiredProviderSlotKeys(s.ctx))

	providerMembers, err := s.rdb.ZRange(s.ctx, providerKeyWithFresh, 0, -1).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), []string{"fresh"}, providerMembers)

	exists, err := s.rdb.Exists(s.ctx, providerKeyExpiredOnly).Result()
	require.NoError(s.T(), err)
	require.EqualValues(s.T(), 0, exists)

	userMembers, err := s.rdb.ZRange(s.ctx, userKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), []string{"user-expired"}, userMembers)

	unindexedMembers, err := s.rdb.ZRange(s.ctx, unindexedProviderKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), []string{"unindexed-expired"}, unindexedMembers)

	score, err := s.rdb.ZScore(s.ctx, providerActiveIndexKey, "301").Result()
	require.NoError(s.T(), err)
	require.Greater(s.T(), int64(score), now)
	_, err = s.rdb.ZScore(s.ctx, providerActiveIndexKey, "302").Result()
	require.ErrorIs(s.T(), err, redis.Nil)
}

func (s *ConcurrencyCacheSuite) TestCleanupExpiredProviderSlotKeys_ReapsUserIndex() {
	now, err := s.rawCache.redisUnixSeconds(s.ctx)
	require.NoError(s.T(), err)
	expiredScore := float64(now - 10)
	userKeyWithFresh := fmt.Sprintf("%s%d", userSlotKeyPrefix, 401)

	// User 401 has an expired index score but still has load, so cleanup must refresh its score.
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, userKeyWithFresh,
		redis.Z{Score: float64(now), Member: "fresh"},
	).Err())
	// User 402 has no load, so cleanup removes its expired index entry.
	// Malformed members are removed with expired candidates.
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, userActiveIndexKey,
		redis.Z{Score: expiredScore, Member: "401"},
		redis.Z{Score: expiredScore, Member: "402"},
		redis.Z{Score: expiredScore, Member: "not-a-user-id"},
	).Err())

	require.NoError(s.T(), s.cache.CleanupExpiredProviderSlotKeys(s.ctx))

	score, err := s.rdb.ZScore(s.ctx, userActiveIndexKey, "401").Result()
	require.NoError(s.T(), err)
	require.Greater(s.T(), int64(score), now, "loaded user should be re-scheduled, not dropped")

	_, err = s.rdb.ZScore(s.ctx, userActiveIndexKey, "402").Result()
	require.ErrorIs(s.T(), err, redis.Nil, "idle expired user member should be reaped")

	_, err = s.rdb.ZScore(s.ctx, userActiveIndexKey, "not-a-user-id").Result()
	require.ErrorIs(s.T(), err, redis.Nil, "invalid member should be reaped")
}

func (s *ConcurrencyCacheSuite) TestCleanupStaleProcessSlots_LegacyWaitSweepRunsOnce() {
	unindexedProviderWaitKey := fmt.Sprintf("%s%d", providerWaitKeyPrefix, 2901)
	unindexedUserWaitKey := fmt.Sprintf("%s%d", waitQueueKeyPrefix, 2902)
	require.NoError(s.T(), s.rdb.Set(s.ctx, unindexedProviderWaitKey, 5, time.Minute).Err())
	require.NoError(s.T(), s.rdb.Set(s.ctx, unindexedUserWaitKey, 3, time.Minute).Err())

	// Without a marker, the first sweep removes legacy wait keys, including unindexed ones.
	require.NoError(s.T(), s.cache.CleanupStaleProcessSlots(s.ctx, "keep-"))

	_, err := s.rdb.Get(s.ctx, unindexedProviderWaitKey).Result()
	require.ErrorIs(s.T(), err, redis.Nil, "legacy provider wait key should be swept on first startup")
	_, err = s.rdb.Get(s.ctx, unindexedUserWaitKey).Result()
	require.ErrorIs(s.T(), err, redis.Nil, "legacy user wait key should be swept on first startup")

	exists, err := s.rdb.Exists(s.ctx, legacyWaitSweepMarkerKey).Result()
	require.NoError(s.T(), err)
	require.EqualValues(s.T(), 1, exists, "sweep marker should be set after first run")

	// The marker skips later sweeps, leaving unindexed wait counters intact.
	require.NoError(s.T(), s.rdb.Set(s.ctx, unindexedProviderWaitKey, 5, time.Minute).Err())
	require.NoError(s.T(), s.cache.CleanupStaleProcessSlots(s.ctx, "keep-"))
	val, err := s.rdb.Get(s.ctx, unindexedProviderWaitKey).Int()
	require.NoError(s.T(), err, "sweep must not run twice")
	require.Equal(s.T(), 5, val)
}

func (s *ConcurrencyCacheSuite) TestCleanupStaleProcessSlots_ProcessesExpiredIndexMembers() {
	// Startup cleanup includes expired index members to find crash leftovers.
	require.NoError(s.T(), s.rdb.Set(s.ctx, legacyWaitSweepMarkerKey, "1", 0).Err())
	providerID := int64(3901)
	userID := int64(3902)
	providerKey := fmt.Sprintf("%s%d", providerSlotKeyPrefix, providerID)
	userKey := fmt.Sprintf("%s%d", userSlotKeyPrefix, userID)
	providerWaitKey := fmt.Sprintf("%s%d", providerWaitKeyPrefix, providerID)

	now, err := s.rawCache.redisUnixSeconds(s.ctx)
	require.NoError(s.T(), err)
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, providerKey,
		redis.Z{Score: float64(now), Member: "oldproc-1"},
	).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, userKey,
		redis.Z{Score: float64(now), Member: "oldproc-2"},
	).Err())
	require.NoError(s.T(), s.rdb.Set(s.ctx, providerWaitKey, 4, time.Minute).Err())
	// A past score represents an index entry left across a long shutdown.
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, providerActiveIndexKey, redis.Z{
		Score:  float64(now - 100),
		Member: strconv.FormatInt(providerID, 10),
	}).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, userActiveIndexKey, redis.Z{
		Score:  float64(now - 100),
		Member: strconv.FormatInt(userID, 10),
	}).Err())

	require.NoError(s.T(), s.cache.CleanupStaleProcessSlots(s.ctx, "keep-"))

	exists, err := s.rdb.Exists(s.ctx, providerKey).Result()
	require.NoError(s.T(), err)
	require.EqualValues(s.T(), 0, exists, "stale slot key of expired index member should be purged")

	exists, err = s.rdb.Exists(s.ctx, userKey).Result()
	require.NoError(s.T(), err)
	require.EqualValues(s.T(), 0, exists)

	_, err = s.rdb.Get(s.ctx, providerWaitKey).Result()
	require.ErrorIs(s.T(), err, redis.Nil, "wait counter of expired index member should be deleted")

	_, err = s.rdb.ZScore(s.ctx, providerActiveIndexKey, strconv.FormatInt(providerID, 10)).Result()
	require.ErrorIs(s.T(), err, redis.Nil, "emptied member should be removed from index")
	_, err = s.rdb.ZScore(s.ctx, userActiveIndexKey, strconv.FormatInt(userID, 10)).Result()
	require.ErrorIs(s.T(), err, redis.Nil)
}

func (s *ConcurrencyCacheSuite) TestCleanupStaleProcessSlots_RemovesOldPrefixesAndWaitCounters() {
	// With the marker present, the index cleanup path removes wait counters.
	require.NoError(s.T(), s.rdb.Set(s.ctx, legacyWaitSweepMarkerKey, "1", 0).Err())
	providerID := int64(901)
	userID := int64(902)
	providerSlotKey := fmt.Sprintf("%s%d", providerSlotKeyPrefix, providerID)
	userSlotKey := fmt.Sprintf("%s%d", userSlotKeyPrefix, userID)
	userWaitKey := fmt.Sprintf("%s%d", waitQueueKeyPrefix, userID)
	providerWaitKey := fmt.Sprintf("%s%d", providerWaitKeyPrefix, providerID)

	now, err := s.rawCache.redisUnixSeconds(s.ctx)
	require.NoError(s.T(), err)
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, providerSlotKey,
		redis.Z{Score: float64(now), Member: "oldproc-1"},
		redis.Z{Score: float64(now), Member: "activeproc-1"},
	).Err())
	require.NoError(s.T(), s.rdb.Expire(s.ctx, providerSlotKey, testSlotTTL).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, userSlotKey,
		redis.Z{Score: float64(now), Member: "oldproc-2"},
		redis.Z{Score: float64(now), Member: "activeproc-2"},
	).Err())
	require.NoError(s.T(), s.rdb.Expire(s.ctx, userSlotKey, testSlotTTL).Err())
	require.NoError(s.T(), s.rdb.Set(s.ctx, userWaitKey, 3, testSlotTTL).Err())
	require.NoError(s.T(), s.rdb.Set(s.ctx, providerWaitKey, 2, testSlotTTL).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, providerActiveIndexKey, redis.Z{
		Score:  float64(now + 60),
		Member: strconv.FormatInt(providerID, 10),
	}).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, userActiveIndexKey, redis.Z{
		Score:  float64(now + 60),
		Member: strconv.FormatInt(userID, 10),
	}).Err())

	require.NoError(s.T(), s.cache.CleanupStaleProcessSlots(s.ctx, "activeproc-"))

	providerMembers, err := s.rdb.ZRange(s.ctx, providerSlotKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), []string{"activeproc-1"}, providerMembers)

	userMembers, err := s.rdb.ZRange(s.ctx, userSlotKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), []string{"activeproc-2"}, userMembers)

	_, err = s.rdb.Get(s.ctx, userWaitKey).Result()
	require.ErrorIs(s.T(), err, redis.Nil)
	_, err = s.rdb.Get(s.ctx, providerWaitKey).Result()
	require.ErrorIs(s.T(), err, redis.Nil)
}

func (s *ConcurrencyCacheSuite) TestCleanupStaleProcessSlots_DeletesEmptySlotKeys() {
	providerID := int64(903)
	providerSlotKey := fmt.Sprintf("%s%d", providerSlotKeyPrefix, providerID)
	now, err := s.rawCache.redisUnixSeconds(s.ctx)
	require.NoError(s.T(), err)
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, providerSlotKey, redis.Z{Score: float64(now), Member: "oldproc-1"}).Err())
	require.NoError(s.T(), s.rdb.Expire(s.ctx, providerSlotKey, testSlotTTL).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, providerActiveIndexKey, redis.Z{
		Score:  float64(now + 60),
		Member: strconv.FormatInt(providerID, 10),
	}).Err())

	require.NoError(s.T(), s.cache.CleanupStaleProcessSlots(s.ctx, "activeproc-"))

	exists, err := s.rdb.Exists(s.ctx, providerSlotKey).Result()
	require.NoError(s.T(), err)
	require.EqualValues(s.T(), 0, exists)
}

func (c waitIncrementFault) IncrementWaitCount(context.Context, int64, int) (bool, error) {
	return false, errors.New("增加等待计数未确认")
}

func (c waitIncrementFault) IncrementProviderWaitCount(context.Context, int64, int) (bool, error) {
	return false, errors.New("增加提供商等待计数未确认")
}

// TestWaitFailOpenDoesNotReleaseOtherRequest checks that admission errors cannot release another request's user or provider wait count.
func TestWaitFailOpenDoesNotReleaseOtherRequest(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewConcurrencyCache(rdb, 15, 900)
	userAllowed, err := cache.IncrementWaitCount(ctx, 91001, 20)
	require.NoError(t, err)
	require.True(t, userAllowed)
	providerAllowed, err := cache.IncrementProviderWaitCount(ctx, 91002, 20)
	require.NoError(t, err)
	require.True(t, providerAllowed)
	concurrency := scheduler.NewConcurrencyService(waitIncrementFault{cache}, scheduler.Diagnostics{
		Logf: logging.LegacyPrintf,

		Event: logging.Event,
	},
	)
	user, err := concurrency.EnterUserWait(ctx, 91001, 20)
	require.NoError(t, err)
	require.True(t, user.Allowed)
	provider, err := concurrency.EnterProviderWait(ctx, 91002, 20)
	require.NoError(t, err)
	require.True(t, provider.Allowed)
	user.Release()
	provider.Release()
	user.Release()
	provider.Release()
	count, err := rdb.Get(ctx, "concurrency:wait:91001").Int()
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = cache.GetProviderWaitingCount(ctx, 91002)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}
