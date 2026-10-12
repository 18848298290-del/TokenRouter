//go:build integration

package rediscache

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/TokenFlux/TokenRouter/internal/scheduler"
)

type SessionLimitCacheSuite struct {
	IntegrationRedisSuite
	cache scheduler.SessionLimitCache
}

// TestSessionBatchAfterScriptFlush checks that batch reads find active sessions after Redis loses its script cache.
func TestSessionBatchAfterScriptFlush(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewSessionLimitCache(rdb, 5)
	allowed, err := cache.RegisterSession(ctx, 91001, "session-a", 2, time.Minute)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, rdb.ScriptFlush(ctx).Err())

	counts, err := cache.GetActiveSessionCountBatch(ctx, []int64{91001, 91002}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, counts[91001])
	require.Equal(t, 0, counts[91002])

	// A subsequent individual refresh keeps the same registration active.
	require.NoError(t, cache.RefreshSession(ctx, 91001, "session-a", time.Minute))
	count, err := cache.GetActiveSessionCount(ctx, 91001)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func (s *SessionLimitCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.cache = NewSessionLimitCache(s.rdb, 5)
}

// TestUnregisterSession_FreesSlotForNewSession checks immediate replacement of session A by B at max_sessions=1.
// A failed attempt left registered would consume capacity for the five-minute idle window.
func (s *SessionLimitCacheSuite) TestUnregisterSession_FreesSlotForNewSession() {
	const providerID = int64(101)
	maxSessions := 1
	idleTimeout := 5 * time.Minute

	// Session A represents a request beginning forwarding.
	allowed, err := s.cache.RegisterSession(s.ctx, providerID, "session-A", maxSessions, idleTimeout)
	s.RequireNoError(err, "RegisterSession A")
	s.True(allowed, "首个会话应被允许")

	// A still holds the slot, so B is rejected within the idle window.
	allowed, err = s.cache.RegisterSession(s.ctx, providerID, "session-B", maxSessions, idleTimeout)
	s.RequireNoError(err, "RegisterSession B before release")
	s.False(allowed, "槽位被 A 占用时新会话应被拒绝")

	// A failed attempt unregisters A immediately.
	s.RequireNoError(s.cache.UnregisterSession(s.ctx, providerID, "session-A"), "UnregisterSession A")

	// B can register as soon as A is released.
	allowed, err = s.cache.RegisterSession(s.ctx, providerID, "session-B", maxSessions, idleTimeout)
	s.RequireNoError(err, "RegisterSession B after release")
	s.True(allowed, "释放后新会话应立即可注册")

	count, err := s.cache.GetActiveSessionCount(s.ctx, providerID)
	s.RequireNoError(err, "GetActiveSessionCount")
	s.Equal(1, count, "释放后活跃会话数应为 1（仅 B）")
}

// TestUnregisterSession_IdempotentAndMissing checks repeated cleanup.
// Missing and empty session IDs can be released without errors.
func (s *SessionLimitCacheSuite) TestUnregisterSession_IdempotentAndMissing() {
	const providerID = int64(102)

	s.RequireNoError(s.cache.UnregisterSession(s.ctx, providerID, "missing"), "释放不存在的会话")
	s.RequireNoError(s.cache.UnregisterSession(s.ctx, providerID, "missing"), "重复释放")

	s.RequireNoError(s.cache.UnregisterSession(s.ctx, providerID, ""), "空 sessionUUID 应为 no-op")
}

// TestUnregisterSession_OnlyTargetsSpecifiedSession checks that releasing one session leaves its peers active.
func (s *SessionLimitCacheSuite) TestUnregisterSession_OnlyTargetsSpecifiedSession() {
	const providerID = int64(103)
	maxSessions := 2
	idleTimeout := 5 * time.Minute

	for _, sid := range []string{"session-A", "session-B"} {
		allowed, err := s.cache.RegisterSession(s.ctx, providerID, sid, maxSessions, idleTimeout)
		s.RequireNoError(err, "RegisterSession "+sid)
		s.True(allowed)
	}

	s.RequireNoError(s.cache.UnregisterSession(s.ctx, providerID, "session-A"), "UnregisterSession A")

	active, err := s.cache.IsSessionActive(s.ctx, providerID, "session-A")
	s.RequireNoError(err, "IsSessionActive A")
	s.False(active, "A 应已释放")

	active, err = s.cache.IsSessionActive(s.ctx, providerID, "session-B")
	s.RequireNoError(err, "IsSessionActive B")
	s.True(active, "B 不应受影响")
}

func TestSessionLimitCacheSuite(t *testing.T) {
	suite.Run(t, new(SessionLimitCacheSuite))
}
