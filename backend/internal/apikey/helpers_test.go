package apikey_test

import (
	"context"
	"errors"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/apikey/testkit"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/team"
)

// errAuthCacheMiss simulates a cache miss. Internal tests use redis.Nil to check Redis miss handling.
var errAuthCacheMiss = errors.New("auth cache miss")

type compositeGroupRepoStub struct {
	routing.GroupRepository

	groups map[int64]*routing.Group
}

type authRepoStub struct {
	getByKeyForAuth   func(ctx context.Context, key string) (*apikey.APIKey, error)
	listKeysByUserID  func(ctx context.Context, userID int64) ([]string, error)
	listKeysByGroupID func(ctx context.Context, groupID int64) ([]string, error)
}

type authCacheStub struct {
	getAuthCache   func(ctx context.Context, key string) (*apikey.APIKeyAuthCacheEntry, error)
	setAuthKeys    []string
	deleteAuthKeys []string
}

// apiKeyTestDependencies supplies test doubles; each owning module constructs its own state.
type apiKeyTestDependencies struct {
	apiKeyRepo            apikey.APIKeyRepository
	userRepo              identity.UserRepository
	groupRepo             routing.GroupRepository
	userSubRepo           billing.UserSubscriptionRepository
	userGroupRateRepo     billing.UserGroupRateRepository
	teamRepo              team.TeamRepository
	cache                 apikey.APIKeyCache
	cfg                   *config.Config
	concurrencyService    *scheduler.ConcurrencyService
	rateLimitCacheInvalid apikey.RateLimitCacheInvalidator
}

func (s *compositeGroupRepoStub) GetByID(_ context.Context, id int64) (*routing.Group, error) {
	group, ok := s.groups[id]
	if !ok {
		return nil, routing.ErrGroupNotFound
	}
	copyGroup := *group
	return &copyGroup, nil
}

func (s *authRepoStub) Create(ctx context.Context, key *apikey.APIKey) error {
	panic("unexpected Create call")
}

func (s *authRepoStub) GetByID(ctx context.Context, id int64) (*apikey.APIKey, error) {
	panic("unexpected GetByID call")
}

func (s *authRepoStub) GetKeyAndOwnerID(ctx context.Context, id int64) (string, int64, error) {
	panic("unexpected GetKeyAndOwnerID call")
}

func (s *authRepoStub) GetByKey(ctx context.Context, key string) (*apikey.APIKey, error) {
	panic("unexpected GetByKey call")
}

func (s *authRepoStub) GetByKeyForAuth(ctx context.Context, key string) (*apikey.APIKey, error) {
	if s.getByKeyForAuth == nil {
		panic("unexpected GetByKeyForAuth call")
	}
	return s.getByKeyForAuth(ctx, key)
}

func (s *authRepoStub) RotateCredential(context.Context, *apikey.APIKey, string) error {
	panic("unexpected RotateCredential call")
}

func (s *authRepoStub) Update(ctx context.Context, key *apikey.APIKey, _ apikey.APIKeyUpdateFields) error {
	panic("unexpected Update call")
}

func (s *authRepoStub) Delete(ctx context.Context, id int64) error {
	panic("unexpected Delete call")
}

func (s *authRepoStub) DeleteWithAudit(ctx context.Context, id int64) error {
	panic("unexpected DeleteWithAudit call")
}

func (s *authRepoStub) ListByUserID(ctx context.Context, userID int64, params pagination.PaginationParams, filters apikey.APIKeyListFilters) ([]apikey.APIKey, *pagination.PaginationResult, error) {
	panic("unexpected ListByUserID call")
}

func (s *authRepoStub) VerifyOwnership(ctx context.Context, userID int64, apiKeyIDs []int64) ([]int64, error) {
	panic("unexpected VerifyOwnership call")
}

func (s *authRepoStub) CountByUserID(ctx context.Context, userID int64) (int64, error) {
	panic("unexpected CountByUserID call")
}

func (s *authRepoStub) ExistsByKey(ctx context.Context, key string) (bool, error) {
	panic("unexpected ExistsByKey call")
}

func (s *authRepoStub) ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]apikey.APIKey, *pagination.PaginationResult, error) {
	panic("unexpected ListByGroupID call")
}

func (s *authRepoStub) SearchAPIKeys(ctx context.Context, userID int64, keyword string, limit int) ([]apikey.APIKey, error) {
	panic("unexpected SearchAPIKeys call")
}

func (s *authRepoStub) ClearGroupIDByGroupID(ctx context.Context, groupID int64) (int64, error) {
	panic("unexpected ClearGroupIDByGroupID call")
}

func (s *authRepoStub) UpdateGroupIDByUserAndGroup(ctx context.Context, userID, oldGroupID, newGroupID int64) (int64, error) {
	panic("unexpected UpdateGroupIDByUserAndGroup call")
}

func (s *authRepoStub) CountByGroupID(ctx context.Context, groupID int64) (int64, error) {
	panic("unexpected CountByGroupID call")
}

func (s *authRepoStub) ListKeysByUserID(ctx context.Context, userID int64) ([]string, error) {
	if s.listKeysByUserID == nil {
		panic("unexpected ListKeysByUserID call")
	}
	return s.listKeysByUserID(ctx, userID)
}

func (s *authRepoStub) ListKeysByGroupID(ctx context.Context, groupID int64) ([]string, error) {
	if s.listKeysByGroupID == nil {
		panic("unexpected ListKeysByGroupID call")
	}
	return s.listKeysByGroupID(ctx, groupID)
}

func (s *authRepoStub) IncrementQuotaUsed(ctx context.Context, id int64, amount float64) (float64, error) {
	panic("unexpected IncrementQuotaUsed call")
}

func (s *authRepoStub) UpdateLastUsed(ctx context.Context, id int64, usedAt time.Time) error {
	panic("unexpected UpdateLastUsed call")
}

func (s *authRepoStub) IncrementRateLimitUsage(ctx context.Context, id int64, cost float64) error {
	panic("unexpected IncrementRateLimitUsage call")
}

func (s *authRepoStub) ResetRateLimitWindows(ctx context.Context, id int64) error {
	panic("unexpected ResetRateLimitWindows call")
}

func (s *authRepoStub) GetRateLimitData(ctx context.Context, id int64) (*apikey.APIKeyRateLimitData, error) {
	panic("unexpected GetRateLimitData call")
}

func (s *authCacheStub) GetCreateAttemptCount(ctx context.Context, userID int64) (int, error) {
	return 0, nil
}

func (s *authCacheStub) IncrementCreateAttemptCount(ctx context.Context, userID int64) error {
	return nil
}

func (s *authCacheStub) DeleteCreateAttemptCount(ctx context.Context, userID int64) error {
	return nil
}

func (s *authCacheStub) IncrementDailyUsage(ctx context.Context, apiKey string) error {
	return nil
}

func (s *authCacheStub) SetDailyUsageExpiry(ctx context.Context, apiKey string, ttl time.Duration) error {
	return nil
}

func (s *authCacheStub) GetAuthCache(ctx context.Context, key string) (*apikey.APIKeyAuthCacheEntry, error) {
	if s.getAuthCache == nil {
		return nil, errAuthCacheMiss
	}
	return s.getAuthCache(ctx, key)
}

func (s *authCacheStub) SetAuthCache(ctx context.Context, key string, entry *apikey.APIKeyAuthCacheEntry, ttl time.Duration) error {
	s.setAuthKeys = append(s.setAuthKeys, key)
	return nil
}

func (s *authCacheStub) DeleteAuthCache(ctx context.Context, key string) error {
	s.deleteAuthKeys = append(s.deleteAuthKeys, key)
	return nil
}

func (s *authCacheStub) PublishAuthCacheInvalidation(ctx context.Context, cacheKey string) error {
	return nil
}

func (s *authCacheStub) SubscribeAuthCacheInvalidation(ctx context.Context, handler func(cacheKey string)) error {
	return nil
}

func validTeamAPIKeyForLifecycleTest() *apikey.APIKey {
	teamID := int64(11)
	createdAt := time.Now()
	return &apikey.APIKey{
		ID:        31,
		UserID:    2,
		TeamID:    &teamID,
		Status:    apikey.StatusAPIKeyActive,
		CreatedAt: createdAt,
		Team:      &team.Team{ID: teamID, Status: team.TeamStatusActive},
		TeamMembership: &team.TeamMembership{
			TeamID:   teamID,
			UserID:   2,
			Role:     team.TeamRoleMember,
			JoinedAt: createdAt.Add(-time.Minute),
		},
		ActorUser: &identity.User{ID: 2, Status: billing.StatusActive},
		User:      &identity.User{ID: 1, Status: billing.StatusActive},
	}
}

func newAPIKeyTestService(d apiKeyTestDependencies) *apikey.APIKeyService {
	cfg := d.cfg
	if cfg == nil {
		cfg = &config.Config{}
	}
	s := testkit.NewService(d.apiKeyRepo, d.userRepo, d.groupRepo, d.userSubRepo, d.userGroupRateRepo, d.cache, cfg)
	if d.teamRepo != nil {
		s.SetTeamRepository(d.teamRepo)
	}
	if d.concurrencyService != nil {
		s.SetConcurrencyService(d.concurrencyService)
	}
	if d.rateLimitCacheInvalid != nil {
		s.SetRateLimitCacheInvalidator(d.rateLimitCacheInvalid)
	}
	return s
}
