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

type GatewayCacheSuite struct {
	rediscontainer.Suite
	cache session.GatewayCache
}

func (s *GatewayCacheSuite) SetupTest() {
	s.Suite.SetupTest()
	s.cache = NewGatewayCache(s.RDB)
}

func (s *GatewayCacheSuite) TestGetSessionProviderID_Missing() {
	_, err := s.cache.GetSessionProviderID(s.Ctx, 1, "nonexistent")
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil for missing session")
}

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

func (s *GatewayCacheSuite) TestSessionProviderID_TTL() {
	sessionID := "s2"
	providerID := int64(100)
	groupID := int64(1)
	sessionTTL := 1 * time.Minute

	require.NoError(s.T(), s.cache.SetSessionProviderID(s.Ctx, groupID, sessionID, providerID, sessionTTL), "SetSessionProviderID")

	sessionKey := buildSessionKey(groupID, sessionID)
	ttl, err := s.RDB.TTL(s.Ctx, sessionKey).Result()
	require.NoError(s.T(), err, "TTL sessionKey after Set")
	s.AssertTTLWithin(ttl, 1*time.Second, sessionTTL)
}

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
	s.AssertTTLWithin(ttl, 1*time.Second, refreshTTL)
}

func (s *GatewayCacheSuite) TestRefreshSessionTTL_MissingKey() {
	// RefreshSessionTTL on a missing key should not error (no-op)
	err := s.cache.RefreshSessionTTL(s.Ctx, 1, "missing-session", 1*time.Minute)
	require.NoError(s.T(), err, "RefreshSessionTTL on missing key should not error")
}

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

func (s *GatewayCacheSuite) TestGetSessionProviderID_CorruptedValue() {
	sessionID := "corrupted"
	groupID := int64(1)
	sessionKey := buildSessionKey(groupID, sessionID)

	// Set a non-integer value
	require.NoError(s.T(), s.RDB.Set(s.Ctx, sessionKey, "not-a-number", 1*time.Minute).Err(), "Set invalid value")

	_, err := s.cache.GetSessionProviderID(s.Ctx, groupID, sessionID)
	require.Error(s.T(), err, "expected error for corrupted value")
	require.False(s.T(), errors.Is(err, redis.Nil), "expected parsing error, not redis.Nil")
}

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

func (s *GatewayCacheSuite) TestSessionOwnerGroupID_TTLAndRefresh() {
	initialTTL := 1 * time.Minute
	refreshTTL := 3 * time.Minute
	key := buildSessionOwnerKey(7, session.SessionIsolationSourceOpenAI, "session-owner-ttl")

	written, err := s.cache.SetSessionOwnerGroupID(s.Ctx, 7, session.SessionIsolationSourceOpenAI, "session-owner-ttl", 11, initialTTL)
	require.NoError(s.T(), err)
	require.True(s.T(), written)

	ttl, err := s.RDB.TTL(s.Ctx, key).Result()
	require.NoError(s.T(), err)
	s.AssertTTLWithin(ttl, 1*time.Second, initialTTL)

	require.NoError(s.T(), s.cache.RefreshSessionOwnerTTL(s.Ctx, 7, session.SessionIsolationSourceOpenAI, "session-owner-ttl", refreshTTL))
	ttl, err = s.RDB.TTL(s.Ctx, key).Result()
	require.NoError(s.T(), err)
	s.AssertTTLWithin(ttl, 1*time.Second, refreshTTL)
}

func (s *GatewayCacheSuite) TestSessionOwnerGroupID_ConcurrentFirstBindAllowsSingleOwner() {
	start := make(chan struct{})
	var wg sync.WaitGroup
	var writtenCount int32
	var winner int64
	errCh := make(chan error, 8)
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

func TestGatewayCacheSuite(t *testing.T) {
	suite.Run(t, new(GatewayCacheSuite))
}

// TestReasoningHistoryNativeRoundTrip checks request/output reasoning keys and the seven-day Redis TTL.
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
		s.AssertTTLWithin(ttl, time.Second, 7*24*time.Hour)
	}
	require.Empty(s.T(), history.Lookup("missing_reasoning"))
}

// buildSessionKey builds the key used to inspect persisted session bindings.
func buildSessionKey(groupID int64, hash string) string {
	return fmt.Sprintf("sticky_session:%d:%s", groupID, hash)
}

func buildSessionOwnerKey(userID int64, source, hash string) string {
	return fmt.Sprintf("sticky_session_owner:%d:%s:%s", userID, source, hash)
}
