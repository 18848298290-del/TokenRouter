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

// A failed provider attempt needs to release its registered session before
// the idle window expires, letting another session use max_sessions capacity.
// Redis-backed scenarios check immediate replacement, targeted deletion and
// successful batch/individual reads after the script cache is flushed.

// SessionLimitCacheSuite exercises session registration cleanup against
// isolated Redis key namespaces.
type SessionLimitCacheSuite struct {
	IntegrationRedisSuite // namespaced Redis test fixture
	// cache is recreated after the namespaced client in SetupTest, giving each
	// method independent session keys with a five-minute default.
	cache scheduler.SessionLimitCache // session limit test subject
}

// TestSessionBatchAfterScriptFlush registers a session before clearing
// Redis's script cache. It checks batch counts for present and absent
// providers, followed by a successful individual refresh and count.
func TestSessionBatchAfterScriptFlush(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewSessionLimitCache(rdb, 5)
	allowed, err := cache.RegisterSession(ctx, 91001, "session-a", 2, time.Minute)
	require.NoError(t, err)
	require.True(t, allowed)
	// Clear loaded scripts while leaving the registered member in Redis so the
	// next queries exercise script-cache recovery with existing session state.
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

// SetupTest creates this method's namespaced Redis fixture and session cache
// with a five-minute default. The embedded fixture registers client and key
// cleanup with the current test.
func (s *SessionLimitCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.cache = NewSessionLimitCache(s.rdb, 5)
}

// TestUnregisterSession_FreesSlotForNewSession registers session A at
// capacity one and initially rejects B. It checks that deleting A lets B
// register immediately and leaves one active session.
func (s *SessionLimitCacheSuite) TestUnregisterSession_FreesSlotForNewSession() {
	const providerID = int64(101)
	maxSessions := 1
	idleTimeout := 5 * time.Minute

	allowed, err := s.cache.RegisterSession(s.ctx, providerID, "session-A", maxSessions, idleTimeout)
	s.RequireNoError(err, "RegisterSession A")
	s.True(allowed, "首个会话应被允许")

	allowed, err = s.cache.RegisterSession(s.ctx, providerID, "session-B", maxSessions, idleTimeout)
	s.RequireNoError(err, "RegisterSession B before release")
	s.False(allowed, "槽位被 A 占用时新会话应被拒绝")

	// Model a failed forwarding attempt by removing A's registration
	// immediately; the next B admission verifies this cleanup releases
	// idle-window capacity.
	s.RequireNoError(s.cache.UnregisterSession(s.ctx, providerID, "session-A"), "UnregisterSession A")

	allowed, err = s.cache.RegisterSession(s.ctx, providerID, "session-B", maxSessions, idleTimeout)
	s.RequireNoError(err, "RegisterSession B after release")
	s.True(allowed, "释放后新会话应立即可注册")

	count, err := s.cache.GetActiveSessionCount(s.ctx, providerID)
	s.RequireNoError(err, "GetActiveSessionCount")
	s.Equal(1, count, "释放后活跃会话数应为 1（仅 B）")
}

// TestUnregisterSession_IdempotentAndMissing releases a missing session twice
// and also releases an empty ID. It checks successful cleanup calls for those
// inputs through the Redis adapter.
func (s *SessionLimitCacheSuite) TestUnregisterSession_IdempotentAndMissing() {
	const providerID = int64(102)

	s.RequireNoError(s.cache.UnregisterSession(s.ctx, providerID, "missing"), "释放不存在的会话")
	s.RequireNoError(s.cache.UnregisterSession(s.ctx, providerID, "missing"), "重复释放")

	s.RequireNoError(s.cache.UnregisterSession(s.ctx, providerID, ""), "空 sessionUUID 应为 no-op")
}

// TestUnregisterSession_OnlyTargetsSpecifiedSession registers two sessions
// before removing one of them. It checks that the removed member is inactive
// while the peer registration remains active.
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

// TestSessionLimitCacheSuite runs the Redis-backed registration and
// unregistration scenarios in the suite. Its per-method fixture isolates
// session keys and owns their cleanup after each test.
func TestSessionLimitCacheSuite(t *testing.T) {
	suite.Run(t, new(SessionLimitCacheSuite))
}
