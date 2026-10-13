//go:build integration

package rediscache

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/TokenFlux/TokenRouter/internal/gateway/session"
	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// GatewayCacheSuite runs gateway and inherited sticky operations against the
// Redis container fixture.
type GatewayCacheSuite struct {
	rediscontainer.Suite // shared Redis container test lifecycle
	// cache is rebuilt after Suite installs a fresh namespaced test client.
	// That test owns namespace and client cleanup while the suite keeps its shared
	// Redis base connection available for inherited and gateway assertions.
	cache session.GatewayCache
}

// SetupTest installs the suite's fresh namespaced client and binds the gateway
// adapter to it. Supported keys receive an isolated test prefix; that test owns
// namespace/client cleanup while the suite owns the shared Redis base lifecycle.
func (s *GatewayCacheSuite) SetupTest() {
	s.Suite.SetupTest()
	s.cache = NewGatewayCache(s.RDB)
}

// TestGetSessionProviderID_Missing reads a group/session binding that has not
// been created in real Redis. The inherited sticky adapter must preserve Redis
// nil as its absence result.
func (s *GatewayCacheSuite) TestGetSessionProviderID_Missing() {
	_, err := s.cache.GetSessionProviderID(s.Ctx, 1, "nonexistent")
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil for missing session")
}

// TestSetAndGetSessionProviderID stores one provider under its group/session
// identity and reads it back. The inherited gateway cache must return the same
// numeric provider identifier.
func (s *GatewayCacheSuite) TestSetAndGetSessionProviderID() {
	sessionID := "s1"
	providerID := int64(99)
	groupID := int64(1)
	sessionTTL := 1 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionProviderID(s.Ctx, groupID, sessionID, providerID, sessionTTL), "SetSessionProviderID")

	sid, err := s.cache.GetSessionProviderID(s.Ctx, groupID, sessionID)
	require.NoError(s.T(), err, "GetSessionProviderID")
	require.Equal(s.T(), providerID, sid, "session id mismatch")
}

// TestSessionProviderID_TTL stores a sticky binding with one-minute retention
// and inspects its Redis key. The measured TTL must be at least one second
// and at most the requested one-minute lifetime.
func (s *GatewayCacheSuite) TestSessionProviderID_TTL() {
	sessionID := "s2"
	providerID := int64(100)
	groupID := int64(1)
	sessionTTL := 1 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionProviderID(s.Ctx, groupID, sessionID, providerID, sessionTTL), "SetSessionProviderID")

	sessionKey := buildSessionKey(groupID, sessionID)
	ttl, err := s.RDB.TTL(s.Ctx, sessionKey).Result()
	require.NoError(s.T(), err, "TTL sessionKey after Set")
	// Use a broad retention interval to detect nearly expired or overlong keys;
	// this assertion does not require closeness to the requested lifetime.
	s.AssertTTLWithin(ttl, 1*time.Second, sessionTTL)
}

// TestRefreshSessionTTL replaces a stored binding's one-minute expiry with
// three minutes and measures it through Redis. The refreshed key must retain
// a TTL of at least one second and at most three minutes.
func (s *GatewayCacheSuite) TestRefreshSessionTTL() {
	sessionID := "s3"
	providerID := int64(101)
	groupID := int64(1)
	initialTTL := 1 * time.Minute
	refreshTTL := 3 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionProviderID(s.Ctx, groupID, sessionID, providerID, initialTTL), "SetSessionProviderID")

	require.NoError(s.T(), s.cache.RefreshSessionTTL(s.Ctx, groupID, sessionID, refreshTTL), "RefreshSessionTTL")

	sessionKey := buildSessionKey(groupID, sessionID)
	ttl, err := s.RDB.TTL(s.Ctx, sessionKey).Result()
	require.NoError(s.T(), err, "TTL after Refresh")
	// Use a broad retention interval to detect nearly expired or overlong keys;
	// this assertion does not require closeness to the requested lifetime.
	s.AssertTTLWithin(ttl, 1*time.Second, refreshTTL)
}

// TestRefreshSessionTTL_MissingKey refreshes a group/session key that has
// never been stored. The inherited operation must complete successfully when
// Redis has no corresponding binding.
func (s *GatewayCacheSuite) TestRefreshSessionTTL_MissingKey() {
	err := s.cache.RefreshSessionTTL(s.Ctx, 1, "missing-session", 1*time.Minute)
	require.NoError(s.T(), err, "RefreshSessionTTL on missing key should not error")
}

// TestDeleteSessionProviderID creates then deletes a stored sticky provider
// binding. Reading the same group/session identity afterward must return Redis
// nil rather than the earlier provider.
func (s *GatewayCacheSuite) TestDeleteSessionProviderID() {
	sessionID := "openai:s4"
	providerID := int64(102)
	groupID := int64(1)
	sessionTTL := 1 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionProviderID(s.Ctx, groupID, sessionID, providerID, sessionTTL), "SetSessionProviderID")
	require.NoError(s.T(), s.cache.DeleteSessionProviderID(s.Ctx, groupID, sessionID), "DeleteSessionProviderID")

	_, err := s.cache.GetSessionProviderID(s.Ctx, groupID, sessionID)
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil after delete")
}

// TestGetSessionProviderID_CorruptedValue inserts a nonnumeric string under a
// sticky provider key. Reading must report a parsing error distinguishable
// from Redis nil for a missing binding.
func (s *GatewayCacheSuite) TestGetSessionProviderID_CorruptedValue() {
	sessionID := "corrupted"
	groupID := int64(1)
	sessionKey := buildSessionKey(groupID, sessionID)

	// Write corrupt storage directly so the inherited reader's integer decoding
	// is exercised after a successful Redis command, keeping parse failure
	// distinct from a missing key.
	require.NoError(s.T(), s.RDB.Set(s.Ctx, sessionKey, "not-a-number", 1*time.Minute).Err(), "Set invalid value")

	_, err := s.cache.GetSessionProviderID(s.Ctx, groupID, sessionID)
	require.Error(s.T(), err, "expected error for corrupted value")
	require.False(s.T(), errors.Is(err, redis.Nil), "expected parsing error, not redis.Nil")
}

// TestSessionOwnerGroupID_SetNXAndGet tries to bind two groups to the same
// user/source/session owner key. The first write must succeed and the later
// write must retain that first group.
func (s *GatewayCacheSuite) TestSessionOwnerGroupID_SetNXAndGet() {
	sessionTTL := 1 * time.Minute

	written, err := s.cache.SetSessionOwnerGroupID(s.Ctx, 7, session.SessionIsolationSourceGateway, "session-owner", 11, sessionTTL)
	require.NoError(s.T(), err)
	require.True(s.T(), written)

	written, err = s.cache.SetSessionOwnerGroupID(s.Ctx, 7, session.SessionIsolationSourceGateway, "session-owner", 22, sessionTTL)
	require.NoError(s.T(), err)
	require.False(s.T(), written)

	ownerID, err := s.cache.GetSessionOwnerGroupID(s.Ctx, 7, session.SessionIsolationSourceGateway, "session-owner")
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(11), ownerID)
}

// TestSessionOwnerGroupID_TTLAndRefresh inspects initial first-owner retention
// and then requests a longer lifetime. The first TTL must be between one second
// and one minute; the refreshed TTL must be between one second and three minutes.
func (s *GatewayCacheSuite) TestSessionOwnerGroupID_TTLAndRefresh() {
	initialTTL := 1 * time.Minute
	refreshTTL := 3 * time.Minute
	key := buildSessionOwnerKey(7, session.SessionIsolationSourceOpenAI, "session-owner-ttl")

	written, err := s.cache.SetSessionOwnerGroupID(s.Ctx, 7, session.SessionIsolationSourceOpenAI, "session-owner-ttl", 11, initialTTL)
	require.NoError(s.T(), err)
	require.True(s.T(), written)

	ttl, err := s.RDB.TTL(s.Ctx, key).Result()
	require.NoError(s.T(), err)
	// Use a broad retention interval to detect nearly expired or overlong keys;
	// this assertion does not require closeness to the requested lifetime.
	s.AssertTTLWithin(ttl, 1*time.Second, initialTTL)

	require.NoError(s.T(), s.cache.RefreshSessionOwnerTTL(s.Ctx, 7, session.SessionIsolationSourceOpenAI, "session-owner-ttl", refreshTTL))
	ttl, err = s.RDB.TTL(s.Ctx, key).Result()
	require.NoError(s.T(), err)
	// Use a broad retention interval to detect nearly expired or overlong keys;
	// this assertion does not require closeness to the requested lifetime.
	s.AssertTTLWithin(ttl, 1*time.Second, refreshTTL)
}

// TestSessionOwnerGroupID_ConcurrentFirstBindAllowsSingleOwner releases eight
// competing first-owner writers together against one Redis key. Exactly one
// write must win, and the persisted group must match that observed winner.
func (s *GatewayCacheSuite) TestSessionOwnerGroupID_ConcurrentFirstBindAllowsSingleOwner() {
	start := make(chan struct{})
	var wg sync.WaitGroup
	var writtenCount int32
	var winner int64
	// Buffer one error per competing writer so failed Redis calls can finish
	// before the test waits for all goroutines and begins draining their results.
	errCh := make(chan error, 8)
	// Start each writer behind the same barrier, then release them together and
	// wait for every completion before closing the error channel and checking the
	// single retained owner.
	for groupID := int64(1); groupID <= 8; groupID++ {
		wg.Go(func() {
			<-start
			written, err := s.cache.SetSessionOwnerGroupID(s.Ctx, 7, session.SessionIsolationSourceGemini, "session-owner-race", groupID, time.Minute)
			if err != nil {
				errCh <- err
				return
			}
			if written {
				atomic.AddInt32(&writtenCount, 1)
				atomic.StoreInt64(&winner, groupID)
			}
		})
	}

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(s.T(), err)
	}

	require.Equal(s.T(), int32(1), atomic.LoadInt32(&writtenCount))
	ownerID, err := s.cache.GetSessionOwnerGroupID(s.Ctx, 7, session.SessionIsolationSourceGemini, "session-owner-race")
	require.NoError(s.T(), err)
	require.Equal(s.T(), atomic.LoadInt64(&winner), ownerID)
}

// TestGatewayCacheSuite runs the full container-backed gateway cache suite
// under the integration build tag. The shared server and per-test namespace
// lifecycle supply isolated supported keys for protocol and sticky assertions.
func TestGatewayCacheSuite(t *testing.T) {
	suite.Run(t, new(GatewayCacheSuite))
}

// TestReasoningHistoryNativeRoundTrip captures request and output reasoning
// summaries through production history recovery. Text must be retrievable
// under both item IDs, with each stored TTL between one second and seven days.
// A missing item must return empty reasoning.
func (s *GatewayCacheSuite) TestReasoningHistoryNativeRoundTrip() {
	store, ok := s.cache.(session.ReasoningContentCache)
	require.True(s.T(), ok)
	history := &session.ReasoningHistory{Cache: store}
	history.FromInput(json.RawMessage(`[{"type":"reasoning","id":"ri_request","summary":[{"type":"summary_text","text":"request history"}]}]`))
	require.Equal(s.T(), "request history", history.Lookup("ri_request"))

	var output []openai.ResponsesOutput
	require.NoError(s.T(), json.Unmarshal([]byte(`[{"type":"reasoning","id":"ri_response","summary":[{"type":"summary_text","text":" response history "}]}]`), &output))
	history.FromOutput(output)
	require.Equal(s.T(), "response history", history.Lookup("ri_response"))
	for _, id := range []string{"ri_request", "ri_response"} {
		ttl, err := s.RDB.TTL(s.Ctx, "reasoning_content:"+id).Result()
		require.NoError(s.T(), err)
		// Check a broad positive retention range bounded by the seven-day request;
		// this assertion does not require the TTL to remain near that upper bound.
		s.AssertTTLWithin(ttl, time.Second, 7*24*time.Hour)
	}
	require.Empty(s.T(), history.Lookup("missing_reasoning"))
}

// buildSessionKey formats the inherited sticky key used for direct Redis
// inspection.
func buildSessionKey(groupID int64, hash string) string {
	return fmt.Sprintf("sticky_session:%d:%s", groupID, hash)
}

// buildSessionOwnerKey combines user, source, and session hash for owner-key
// assertions.
func buildSessionOwnerKey(userID int64, source, hash string) string {
	return fmt.Sprintf("sticky_session_owner:%d:%s:%s", userID, source, hash)
}
