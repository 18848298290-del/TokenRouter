//go:build integration

package rediscache

import (
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/testutil/rediscontainer"
)

// EmailCacheSuite exercises identity challenge storage against the Redis container fixture.
type EmailCacheSuite struct {
	rediscontainer.Suite                     // container lifecycle and Redis state
	cache                identity.EmailCache // challenge cache under test
}

// TestGetVerificationCode_Missing queries an address with no saved challenge in
// the isolated Redis fixture. The returned error matches redis.Nil, retaining the
// absence signal used by the identity service.
func (s *EmailCacheSuite) TestGetVerificationCode_Missing() {
	_, err := s.cache.GetVerificationCode(s.Ctx, "nonexistent@example.com")
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil for missing verification code")
}

// TestSetAndGetVerificationCode stores a challenge record with an attempt count
// and then reads it through the cache. The decoded code and attempts retain the
// values written into Redis.
func (s *EmailCacheSuite) TestSetAndGetVerificationCode() {
	email := "a@example.com"
	emailTTL := 2 * time.Minute
	data := &identity.VerificationCodeData{Code: "123456", Attempts: 1, CreatedAt: time.Now()}

	require.NoError(s.T(), s.cache.SetVerificationCode(s.Ctx, email, data, emailTTL), "SetVerificationCode")

	got, err := s.cache.GetVerificationCode(s.Ctx, email)
	require.NoError(s.T(), err, "GetVerificationCode")
	require.Equal(s.T(), "123456", got.Code)
	require.Equal(s.T(), 1, got.Attempts)
}

// TestVerificationCode_TTL writes a challenge with a two-minute lifetime and
// reads the Redis key's TTL. The measured lifetime is at least one second and
// no greater than the requested duration in the fixture assertion.
func (s *EmailCacheSuite) TestVerificationCode_TTL() {
	email := "ttl@example.com"
	emailTTL := 2 * time.Minute
	data := &identity.VerificationCodeData{Code: "654321", Attempts: 0, CreatedAt: time.Now()}

	require.NoError(s.T(), s.cache.SetVerificationCode(s.Ctx, email, data, emailTTL), "SetVerificationCode")

	emailKey := "verify_code:" + email
	ttl, err := s.RDB.TTL(s.Ctx, emailKey).Result()
	require.NoError(s.T(), err, "TTL emailKey")
	s.AssertTTLWithin(ttl, 1*time.Second, emailTTL)
}

// TestDeleteVerificationCode saves and reads a challenge before deleting its key.
// A subsequent read returns redis.Nil, showing that cleanup removes the stored
// credential used by the verification flow.
func (s *EmailCacheSuite) TestDeleteVerificationCode() {
	email := "delete@example.com"
	data := &identity.VerificationCodeData{Code: "999999", Attempts: 0, CreatedAt: time.Now()}

	require.NoError(s.T(), s.cache.SetVerificationCode(s.Ctx, email, data, 2*time.Minute), "SetVerificationCode")

	_, err := s.cache.GetVerificationCode(s.Ctx, email)
	require.NoError(s.T(), err, "GetVerificationCode before delete")

	require.NoError(s.T(), s.cache.DeleteVerificationCode(s.Ctx, email), "DeleteVerificationCode")

	_, err = s.cache.GetVerificationCode(s.Ctx, email)
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil after delete")
}

// TestDeleteVerificationCode_NonExistent deletes an address whose challenge key
// is absent from Redis. Cleanup still succeeds, protecting callers that repeat
// credential removal after another path already removed it.
func (s *EmailCacheSuite) TestDeleteVerificationCode_NonExistent() {
	require.NoError(s.T(), s.cache.DeleteVerificationCode(s.Ctx, "nonexistent@example.com"), "DeleteVerificationCode non-existent")
}

// TestGetVerificationCode_JSONCorruption saves invalid JSON at a present challenge
// key. The read returns an error outside redis.Nil, distinguishing corrupted stored
// state from an absent verification credential.
func (s *EmailCacheSuite) TestGetVerificationCode_JSONCorruption() {
	emailKey := "verify_code:" + "corrupted@example.com"

	require.NoError(s.T(), s.RDB.Set(s.Ctx, emailKey, "not-json", 1*time.Minute).Err(), "Set invalid JSON")

	_, err := s.cache.GetVerificationCode(s.Ctx, "corrupted@example.com")
	require.Error(s.T(), err, "expected error for corrupted JSON")
	require.False(s.T(), errors.Is(err, redis.Nil), "expected decoding error, not redis.Nil")
}

// TestEmailCacheSuite runs credential persistence, removal, and TTL checks with
// the Redis container fixture. Each suite case receives isolated cache state before
// checking its observed record or error result.
func TestEmailCacheSuite(t *testing.T) {
	suite.Run(t, new(EmailCacheSuite))
}

// SetupTest obtains a client with a fresh test namespace and binds its cache
// to that client. The embedded suite shares the Redis container while its
// namespaced clients keep credential records separate between test cases.
func (s *EmailCacheSuite) SetupTest() {
	s.Suite.SetupTest()
	s.cache = NewEmailCache(s.RDB)
}
