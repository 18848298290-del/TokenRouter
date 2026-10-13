//go:build integration

package rediscache

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
	"github.com/TokenFlux/TokenRouter/internal/upstream/anthropic"
)

// IdentityCacheSuite exercises fingerprint persistence against the suite's
// Redis container.
type IdentityCacheSuite struct {
	rediscontainer.Suite                   // container fixture and assertions
	cache                *FingerprintStore // current test identity store
}

// SetupTest prepares the Redis fixture and assigns its typed fingerprint store.
// It checks the constructor's concrete result before suite methods use that
// store for persistence assertions.
func (s *IdentityCacheSuite) SetupTest() {
	s.Suite.SetupTest()
	cache, ok := NewFingerprintStore(s.RDB).(*FingerprintStore)
	s.Require().True(ok, "identity cache constructor type")
	s.cache = cache
}

// TestGetFingerprint_Missing reads a provider fingerprint absent from the Redis
// fixture. The returned error matches redis.Nil, distinguishing a missing
// snapshot from successful decoding and other cache failures.
func (s *IdentityCacheSuite) TestGetFingerprint_Missing() {
	_, err := s.cache.GetFingerprint(s.Ctx, 1)
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil for missing fingerprint")
}

// TestSetAndGetFingerprint writes a fingerprint through the store and loads it
// again. The decoded client identifier and User-Agent equal the supplied
// values, verifying the JSON snapshot round trip.
func (s *IdentityCacheSuite) TestSetAndGetFingerprint() {
	fp := &anthropic.Fingerprint{ClientID: "c1", UserAgent: "ua"}
	require.NoError(s.T(), s.cache.SetFingerprint(s.Ctx, 1, fp), "SetFingerprint")
	gotFP, err := s.cache.GetFingerprint(s.Ctx, 1)
	require.NoError(s.T(), err, "GetFingerprint")
	require.Equal(s.T(), "c1", gotFP.ClientID)
	require.Equal(s.T(), "ua", gotFP.UserAgent)
}

// TestFingerprint_TTL writes a fingerprint and reads the Redis key's lifetime.
// The assertion requires at least one second and at most FingerprintTTL,
// checking a live bounded expiry.
func (s *IdentityCacheSuite) TestFingerprint_TTL() {
	fp := &anthropic.Fingerprint{ClientID: "c1", UserAgent: "ua"}
	require.NoError(s.T(), s.cache.SetFingerprint(s.Ctx, 2, fp))

	fpKey := fmt.Sprintf("%s%d", FingerprintKeyPrefix, 2)
	ttl, err := s.RDB.TTL(s.Ctx, fpKey).Result()
	require.NoError(s.T(), err, "TTL fpKey")
	s.AssertTTLWithin(ttl, 1*time.Second, FingerprintTTL)
}

// TestGetFingerprint_JSONCorruption writes invalid JSON directly into a
// fingerprint key before loading through the store. The result is an error that
// is distinct from redis.Nil.
func (s *IdentityCacheSuite) TestGetFingerprint_JSONCorruption() {
	fpKey := fmt.Sprintf("%s%d", FingerprintKeyPrefix, 999)
	require.NoError(s.T(), s.RDB.Set(s.Ctx, fpKey, "invalid-json-data", 1*time.Minute).Err(), "Set invalid JSON")

	_, err := s.cache.GetFingerprint(s.Ctx, 999)
	require.Error(s.T(), err, "expected error for corrupted JSON")
	require.False(s.T(), errors.Is(err, redis.Nil), "expected decoding error, not redis.Nil")
}

// TestSetFingerprint_Nil passes a nil fingerprint pointer to the Redis setter.
// The write returns no error, recording the accepted serialization path without
// asserting the contents of a later read.
func (s *IdentityCacheSuite) TestSetFingerprint_Nil() {
	err := s.cache.SetFingerprint(s.Ctx, 100, nil)
	require.NoError(s.T(), err, "SetFingerprint(nil) should succeed")
}

// TestIdentityCacheSuite runs identity snapshot assertions against the Redis
// container fixture. The suite covers missing keys, value round trips, TTL,
// corrupted JSON, and acceptance of a nil fingerprint write.
func TestIdentityCacheSuite(t *testing.T) {
	suite.Run(t, new(IdentityCacheSuite))
}
