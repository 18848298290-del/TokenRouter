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

type EmailCacheSuite struct {
	rediscontainer.Suite
	cache identity.EmailCache
}

func (s *EmailCacheSuite) TestGetVerificationCode_Missing() {
	_, err := s.cache.GetVerificationCode(s.Ctx, "nonexistent@example.com")
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil for missing verification code")
}

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

func (s *EmailCacheSuite) TestDeleteVerificationCode_NonExistent() {
	// Deleting a missing key is idempotent.
	require.NoError(s.T(), s.cache.DeleteVerificationCode(s.Ctx, "nonexistent@example.com"), "DeleteVerificationCode non-existent")
}

func (s *EmailCacheSuite) TestGetVerificationCode_JSONCorruption() {
	emailKey := "verify_code:" + "corrupted@example.com"

	require.NoError(s.T(), s.RDB.Set(s.Ctx, emailKey, "not-json", 1*time.Minute).Err(), "Set invalid JSON")

	_, err := s.cache.GetVerificationCode(s.Ctx, "corrupted@example.com")
	require.Error(s.T(), err, "expected error for corrupted JSON")
	require.False(s.T(), errors.Is(err, redis.Nil), "expected decoding error, not redis.Nil")
}

func TestEmailCacheSuite(t *testing.T) {
	suite.Run(t, new(EmailCacheSuite))
}

func (s *EmailCacheSuite) SetupTest() {
	s.Suite.SetupTest()
	s.cache = NewEmailCache(s.RDB)
}
