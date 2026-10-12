//go:build integration

package apikey_test

import (
	"time"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// TestRotateCredentialPreservesConcurrentChanges checks in-place rotation, revocation, and invalidation outbox writes.
func (s *APIKeyRepoSuite) TestRotateCredentialPreservesConcurrentChanges() {
	user := s.mustCreateUser("apikey-rotation@example.com")
	group := s.mustCreateGroup("rotation-group")
	key := &apikey.APIKey{
		UserID: user.ID, Key: "sk-before-rotation", Name: "before",
		Status: billing.StatusActive, Quota: 100, RateLimit5h: 50,
		IsComposite:     true,
		CompositeGroups: []apikey.APIKeyCompositeGroup{{GroupID: group.ID, Prefix: "GPT", NormalizedPrefix: "gpt"}},
	}
	s.Require().NoError(s.repo.Create(s.ctx, key))
	stale, err := s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err)
	createdAt := stale.CreatedAt
	s.Require().NoError(s.repo.IncrementRateLimitUsage(s.ctx, key.ID, 17))
	_, err = s.repo.IncrementQuotaUsed(s.ctx, key.ID, 30)
	s.Require().NoError(err)
	key.Name = "concurrent edit"
	key.Status = apikey.StatusAPIKeyDisabled
	s.Require().NoError(s.repo.Update(s.ctx, key, apikey.APIKeyUpdateFields{Name: true, Status: true}))
	before, err := s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err)
	_, err = s.client.ExecContext(s.ctx, "DELETE FROM auth_cache_invalidation_outbox")
	s.Require().NoError(err)

	oldCredential := stale.Key
	stale.Key = "sk-after-rotation"
	s.Require().NoError(s.repo.RotateCredential(s.ctx, stale, oldCredential))
	got, err := s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err)
	s.Require().Equal("sk-after-rotation", got.Key)
	s.Require().Equal(createdAt, got.CreatedAt)
	before.Key = got.Key
	before.UpdatedAt = got.UpdatedAt
	s.Require().Equal(before, got, "轮换只允许改变凭据和更新时间")
	_, err = s.repo.GetByKeyForAuth(s.ctx, oldCredential)
	s.Require().ErrorIs(err, apikey.ErrAPIKeyNotFound)
	authKey, err := s.repo.GetByKeyForAuth(s.ctx, got.Key)
	s.Require().NoError(err)
	s.Require().Equal(key.ID, authKey.ID)

	for _, credential := range []string{oldCredential, got.Key} {
		rows, err := s.client.QueryContext(s.ctx, "SELECT COUNT(*) FROM auth_cache_invalidation_outbox WHERE cache_key = $1", apikey.AuthCacheKey(credential))
		s.Require().NoError(err)
		s.Require().True(rows.Next())
		var count int
		s.Require().NoError(rows.Scan(&count))
		s.Require().NoError(rows.Close())
		s.Require().NoError(rows.Err())
		s.Require().Positive(count, "新旧凭据都必须有持久失效事件")
	}

	// A second rotation using the stale credential must conflict.
	stale.Key = "sk-losing-rotation"
	s.Require().ErrorIs(s.repo.RotateCredential(s.ctx, stale, oldCredential), apikey.ErrAPIKeyRotationConflict)
}

func (s *APIKeyRepoSuite) TestRotateCredentialRejectsDeletedAndManagedKeys() {
	user := s.mustCreateUser("apikey-rotation-protected@example.com")
	key := &apikey.APIKey{UserID: user.ID, Key: "sk-rotation-protected", Name: "protected", Status: billing.StatusActive}
	s.Require().NoError(s.repo.Create(s.ctx, key))
	oldCredential := key.Key
	key.Key = "sk-replacement"
	key.UserID++
	s.Require().ErrorIs(s.repo.RotateCredential(s.ctx, key, oldCredential), apikey.ErrAPIKeyRotationConflict)
	key.UserID = user.ID
	s.Require().NoError(s.client.APIKey.UpdateOneID(key.ID).SetManagedBy("creative_studio").Exec(s.ctx))
	s.Require().ErrorIs(s.repo.RotateCredential(s.ctx, key, oldCredential), apikey.ErrAPIKeyRotationConflict)
	s.Require().NoError(s.client.APIKey.UpdateOneID(key.ID).ClearManagedBy().SetDeletedAt(time.Now()).Exec(s.ctx))
	s.Require().ErrorIs(s.repo.RotateCredential(s.ctx, key, oldCredential), apikey.ErrAPIKeyRotationConflict)
	got, err := s.client.APIKey.Get(mixins.SkipSoftDelete(s.ctx), key.ID)
	s.Require().NoError(err)
	s.Require().Equal(oldCredential, got.Key)
}

// IncrementQuotaUsed and IncrementRateLimitUsage atomically add billing usage.
// A whole-row edit from a stale snapshot would overwrite those increments.
// These tests keep usage counters outside the edit mask.

func (s *APIKeyRepoSuite) TestUpdate_DoesNotRevertConcurrentQuotaUsage() {
	user := s.mustCreateUser("apikey-lost-update-quota@example.com")
	key := &apikey.APIKey{
		UserID: user.ID,
		Key:    "sk-lost-update-quota",
		Name:   "before",
		Status: billing.StatusActive,
		Quota:  100,
	}
	s.Require().NoError(s.repo.Create(s.ctx, key), "Create")

	stale, err := s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Zero(stale.QuotaUsed)

	newUsed, err := s.repo.IncrementQuotaUsed(s.ctx, key.ID, 30)
	s.Require().NoError(err, "IncrementQuotaUsed")
	s.Require().InDelta(30, newUsed, 1e-9)

	stale.Name = "after"
	s.Require().NoError(
		s.repo.Update(s.ctx, stale, apikey.APIKeyUpdateFields{Name: true}),
		"Update",
	)

	got, err := s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().Equal("after", got.Name, "declared column must still be written")
	s.Require().InDelta(30, got.QuotaUsed, 1e-9, "quota_used must not be reverted by a stale key edit")
}

func (s *APIKeyRepoSuite) TestUpdate_DoesNotRevertConcurrentRateLimitUsage() {
	user := s.mustCreateUser("apikey-lost-update-ratelimit@example.com")
	key := &apikey.APIKey{
		UserID:      user.ID,
		Key:         "sk-lost-update-ratelimit",
		Name:        "before",
		Status:      billing.StatusActive,
		RateLimit5h: 100,
	}
	s.Require().NoError(s.repo.Create(s.ctx, key), "Create")

	stale, err := s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err, "GetByID")
	s.Require().Zero(stale.Usage5h)

	s.Require().NoError(s.repo.IncrementRateLimitUsage(s.ctx, key.ID, 42), "IncrementRateLimitUsage")

	stale.Name = "after"
	s.Require().NoError(
		s.repo.Update(s.ctx, stale, apikey.APIKeyUpdateFields{Name: true}),
		"Update",
	)

	got, err := s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err, "GetByID after update")
	s.Require().InDelta(42, got.Usage5h, 1e-9, "usage_5h must not be reverted by a stale key edit")
	s.Require().InDelta(42, got.Usage1d, 1e-9, "usage_1d must not be reverted by a stale key edit")
	s.Require().InDelta(42, got.Usage7d, 1e-9, "usage_7d must not be reverted by a stale key edit")
}

// TestUpdate_StillResetsUsageWhenDeclared checks usage resets selected by the update mask.
func (s *APIKeyRepoSuite) TestUpdate_StillResetsUsageWhenDeclared() {
	user := s.mustCreateUser("apikey-reset-usage@example.com")
	key := &apikey.APIKey{
		UserID: user.ID,
		Key:    "sk-reset-usage",
		Name:   "reset",
		Status: billing.StatusActive,
		Quota:  100,
	}
	s.Require().NoError(s.repo.Create(s.ctx, key), "Create")

	_, err := s.repo.IncrementQuotaUsed(s.ctx, key.ID, 30)
	s.Require().NoError(err, "IncrementQuotaUsed")
	s.Require().NoError(s.repo.IncrementRateLimitUsage(s.ctx, key.ID, 42), "IncrementRateLimitUsage")

	current, err := s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err, "GetByID")
	current.QuotaUsed = 0
	current.Usage5h = 0
	current.Usage1d = 0
	current.Usage7d = 0
	current.Window5hStart = nil
	current.Window1dStart = nil
	current.Window7dStart = nil
	s.Require().NoError(
		s.repo.Update(s.ctx, current, apikey.APIKeyUpdateFields{QuotaUsed: true, RateLimitUsage: true}),
		"Update",
	)

	got, err := s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err, "GetByID after reset")
	s.Require().Zero(got.QuotaUsed, "explicit quota reset must still apply")
	s.Require().Zero(got.Usage5h, "explicit rate limit reset must still apply")
	s.Require().Zero(got.Usage1d)
	s.Require().Zero(got.Usage7d)
	s.Require().Nil(got.Window5hStart)
}
