package apikey_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/apikey/testkit"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	"github.com/TokenFlux/TokenRouter/internal/team"
)

const apiKeyLimitUpperBound = apikey.KeyApiKeyLimitUpperBound

// billingModeSubscriptionRepoStub supplies subscription lookups for key funding configuration.
type billingModeSubscriptionRepoStub struct {
	billing.UserSubscriptionRepository
	subscriptions map[int64]*billing.UserSubscription
}

// billingModeUserRepoStub returns members or owners by ID to check payer selection for team keys.
type billingModeUserRepoStub struct {
	identity.UserRepository

	users     map[int64]*identity.User
	requested []int64
}

type authGroupRepoStub struct {
	groupsByPlatform map[string][]routing.Group
	groupsByID       map[int64]routing.Group
}

type authUserGroupRateRepoStub struct {
	overrides map[int64]*int
	calls     []int64
}

// apiKeyRepoStub supplies lookup results and records key writes for service tests.
type apiKeyRepoStub struct {
	apiKey              *apikey.APIKey // Record returned by key lookups
	getByIDErr          error          // Lookup error
	deleteErr           error          // Deletion error
	updateErr           error          // Update error
	deletedIDs          []int64        // IDs passed to deletion methods
	updatedKeys         []apikey.APIKey
	allowListByUserID   bool
	listByUserIDKeys    []apikey.APIKey
	listByUserIDErr     error
	listByUserIDCalls   []int64
	listByUserIDParams  []pagination.PaginationParams
	listByUserIDFilters []apikey.APIKeyListFilters
	updateLastUsed      func(ctx context.Context, id int64, usedAt time.Time) error
	touchedIDs          []int64
	touchedUsedAts      []time.Time
}

// apiKeyCacheStub records creation-counter and authentication-cache invalidation.
type apiKeyCacheStub struct {
	invalidated    []int64  // User IDs passed to DeleteCreateAttemptCount
	deleteAuthKeys []string // Cache keys passed to DeleteAuthCache
}

type apiKeyNameSanitizeRepoStub struct {
	apiKey  *apikey.APIKey
	created []*apikey.APIKey
	updated []*apikey.APIKey
}

type quotaStateRepoStub struct {
	quotaBaseAPIKeyRepoStub
	stateCalls int
	state      *apikey.APIKeyQuotaUsageState
	stateErr   error
}

type quotaStateCacheStub struct {
	deleteAuthKeys []string
}

type quotaBaseAPIKeyRepoStub struct {
	getByIDCalls int
}

type updateFieldsAPIKeyRepoStub struct {
	quotaBaseAPIKeyRepoStub
	key          *apikey.APIKey
	updateFields []apikey.APIKeyUpdateFields
}

// userRepoStub supplies user reads; calls to unconfigured methods fail.
type userRepoStub struct {
	identity.UserRepository
	user *identity.User
}

type fakeTeamRepository struct {
	team.TeamRepository
	teamContext *team.TeamContext
}

// stubConcurrencyCacheForTest supplies cached key request counts to the scheduler reader.
type stubConcurrencyCacheForTest struct {
	scheduler.ConcurrencyCache
	apiKeyConcurrency map[int64]int
}

func TestAPIKeyService_CreateBillingModeValidatesPreferredSubscriptionGroups(t *testing.T) {
	const userID int64 = 7
	allowedGroup := &routing.Group{ID: 11, Status: billing.StatusActive, IsExclusive: true}
	blockedGroup := &routing.Group{ID: 12, Status: billing.StatusActive, IsExclusive: true}
	preferredID := int64(101)
	repo := &apiKeyNameSanitizeRepoStub{}
	user := &identity.User{ID: userID, Status: billing.StatusActive, Role: identity.RoleUser, AllowedGroups: []int64{allowedGroup.ID, blockedGroup.ID}}
	svc := testkit.NewService(
		repo,
		&userRepoStub{user: user},
		&compositeGroupRepoStub{groups: map[int64]*routing.Group{allowedGroup.ID: allowedGroup, blockedGroup.ID: blockedGroup}},
		&billingModeSubscriptionRepoStub{subscriptions: map[int64]*billing.UserSubscription{
			preferredID: activeBillingModeSubscription(preferredID, userID, allowedGroup.ID),
		}},
		nil,
		nil,
		nil,
	)
	svc.Start()

	t.Run("指定订阅保留合法分组", func(t *testing.T) {
		customKey := "sk_billing_mode_allowed_group"
		created, err := svc.Create(context.Background(), userID, apikey.CreateAPIKeyRequest{
			Name:                    "subscription key",
			CustomKey:               &customKey,
			GroupID:                 &allowedGroup.ID,
			BillingMode:             apikey.APIKeyBillingModeSubscription,
			PreferredSubscriptionID: &preferredID,
		})

		require.NoError(t, err)
		require.Equal(t, apikey.APIKeyBillingModeSubscription, created.BillingMode)
		require.NotNil(t, created.PreferredSubscriptionID)
		require.Equal(t, preferredID, *created.PreferredSubscriptionID)
	})

	t.Run("指定订阅拒绝套餐外分组", func(t *testing.T) {
		customKey := "sk_billing_mode_blocked_group"
		_, err := svc.Create(context.Background(), userID, apikey.CreateAPIKeyRequest{
			Name:                    "blocked subscription key",
			CustomKey:               &customKey,
			GroupID:                 &blockedGroup.ID,
			BillingMode:             apikey.APIKeyBillingModeSubscription,
			PreferredSubscriptionID: &preferredID,
		})

		require.ErrorIs(t, err, apikey.ErrPreferredSubscriptionGroup)
	})

	t.Run("指定订阅拒绝无绑定分组", func(t *testing.T) {
		customKey := "sk_billing_mode_without_group"
		_, err := svc.Create(context.Background(), userID, apikey.CreateAPIKeyRequest{
			Name:                    "unbound subscription key",
			CustomKey:               &customKey,
			BillingMode:             apikey.APIKeyBillingModeSubscription,
			PreferredSubscriptionID: &preferredID,
		})

		require.Error(t, err)
		require.Contains(t, err.Error(), "GROUP_REQUIRED")
	})
}

func TestAPIKeyService_UpdateBillingModeRejectsRestrictedSubscriptionWithoutGroup(t *testing.T) {
	const userID int64 = 7
	preferredID := int64(101)
	apiKey := &apikey.APIKey{
		ID:     1,
		UserID: userID,
		Key:    "sk_billing_mode_without_group_update",
		Status: apikey.StatusAPIKeyActive,
		User:   &identity.User{ID: userID, Status: billing.StatusActive, Role: identity.RoleUser},
	}
	repo := &apiKeyNameSanitizeRepoStub{apiKey: apiKey}
	svc := testkit.NewService(
		repo,
		&userRepoStub{user: apiKey.User},
		nil,
		&billingModeSubscriptionRepoStub{subscriptions: map[int64]*billing.UserSubscription{
			preferredID: activeBillingModeSubscription(preferredID, userID, 11),
		}},
		nil,
		nil,
		nil,
	)
	svc.Start()
	billingMode := apikey.APIKeyBillingModeSubscription

	_, err := svc.Update(context.Background(), apiKey.ID, userID, apikey.UpdateAPIKeyRequest{
		BillingMode:             &billingMode,
		PreferredSubscriptionID: &preferredID,
	})

	require.ErrorIs(t, err, apikey.ErrPreferredSubscriptionGroup)
	require.Empty(t, repo.updated)
}

func TestAPIKeyService_UpdateBillingModeClearsPreferredSubscription(t *testing.T) {
	const userID int64 = 7
	preferredID := int64(101)
	groupID := int64(11)
	repo := &apiKeyNameSanitizeRepoStub{apiKey: &apikey.APIKey{
		ID:                      1,
		UserID:                  userID,
		Key:                     "sk_billing_mode_update",
		Status:                  apikey.StatusAPIKeyActive,
		GroupID:                 &groupID,
		Group:                   &routing.Group{ID: groupID, Status: billing.StatusActive, IsExclusive: true},
		User:                    &identity.User{ID: userID, Status: billing.StatusActive, Role: identity.RoleUser, AllowedGroups: []int64{groupID}},
		BillingMode:             apikey.APIKeyBillingModeSubscription,
		PreferredSubscriptionID: &preferredID,
	}}
	svc := testkit.NewService(
		repo,
		&userRepoStub{user: repo.apiKey.User},
		&compositeGroupRepoStub{groups: map[int64]*routing.Group{groupID: repo.apiKey.Group}},
		&billingModeSubscriptionRepoStub{subscriptions: map[int64]*billing.UserSubscription{
			preferredID: activeBillingModeSubscription(preferredID, userID, groupID),
		}},
		nil,
		nil,
		nil,
	)
	svc.Start()
	billingMode := apikey.APIKeyBillingModeBalance

	updated, err := svc.Update(context.Background(), repo.apiKey.ID, userID, apikey.UpdateAPIKeyRequest{BillingMode: &billingMode})

	require.NoError(t, err)
	require.Equal(t, apikey.APIKeyBillingModeBalance, updated.BillingMode)
	require.Nil(t, updated.PreferredSubscriptionID)
	require.Len(t, repo.updated, 1)
	require.Nil(t, repo.updated[0].PreferredSubscriptionID)
}

func TestAPIKeyService_ListBillingSubscriptionsUsesTeamOwner(t *testing.T) {
	const (
		memberID int64 = 7
		ownerID  int64 = 8
	)
	ownerSubscriptionID := int64(101)
	memberSubscriptionID := int64(102)
	svc := testkit.NewService(
		nil,
		nil,
		nil,
		&billingModeSubscriptionRepoStub{subscriptions: map[int64]*billing.UserSubscription{
			ownerSubscriptionID:  activeBillingModeSubscription(ownerSubscriptionID, ownerID),
			memberSubscriptionID: activeBillingModeSubscription(memberSubscriptionID, memberID),
		}},
		nil,
		nil,
		&config.Config{Team: config.TeamConfig{Enabled: true}},
	)
	svc.Start()
	svc.SetTeamRepository(&fakeTeamRepository{teamContext: &team.TeamContext{
		Team:       &team.Team{ID: 11, Status: team.TeamStatusActive},
		Membership: &team.TeamMembership{TeamID: 11, UserID: memberID, Role: team.TeamRoleMember},
		Owner:      &team.TeamMembership{TeamID: 11, UserID: ownerID, Role: team.TeamRoleOwner},
	}})

	options, err := svc.ListBillingSubscriptionsForScope(context.Background(), memberID, "team")

	require.NoError(t, err)
	require.Len(t, options, 1)
	require.Equal(t, ownerSubscriptionID, options[0].ID)
}

func TestAPIKeyService_UpdateInactiveTeamKeyBillingModeUsesTeamOwner(t *testing.T) {
	const (
		memberID int64 = 7
		ownerID  int64 = 8
		teamID   int64 = 11
		groupID  int64 = 12
	)
	preferredID := int64(101)
	createdAt := time.Now()
	member := &identity.User{ID: memberID, Status: billing.StatusActive, Role: identity.RoleUser}
	owner := &identity.User{ID: ownerID, Status: billing.StatusActive, Role: identity.RoleUser, AllowedGroups: []int64{groupID}}
	group := &routing.Group{ID: groupID, Status: billing.StatusActive, IsExclusive: true}
	apiKey := &apikey.APIKey{
		ID:        1,
		UserID:    memberID,
		TeamID:    func() *int64 { value := teamID; return &value }(),
		Key:       "sk_inactive_team_billing_mode",
		Status:    apikey.StatusAPIKeyDisabled,
		CreatedAt: createdAt,
		GroupID:   &group.ID,
		Group:     group,
		User:      member,
	}
	keyRepo := &apiKeyNameSanitizeRepoStub{apiKey: apiKey}
	userRepo := &billingModeUserRepoStub{users: map[int64]*identity.User{memberID: member, ownerID: owner}}
	svc := testkit.NewService(
		keyRepo,
		userRepo,
		&compositeGroupRepoStub{groups: map[int64]*routing.Group{groupID: group}},
		&billingModeSubscriptionRepoStub{subscriptions: map[int64]*billing.UserSubscription{
			preferredID: activeBillingModeSubscription(preferredID, ownerID, groupID),
		}},
		nil,
		nil,
		&config.Config{Team: config.TeamConfig{Enabled: true}},
	)
	svc.Start()
	svc.SetTeamRepository(&fakeTeamRepository{teamContext: &team.TeamContext{
		Team:       &team.Team{ID: teamID, Status: team.TeamStatusActive},
		Membership: &team.TeamMembership{TeamID: teamID, UserID: memberID, Role: team.TeamRoleMember, JoinedAt: createdAt.Add(-time.Minute)},
		Owner:      &team.TeamMembership{TeamID: teamID, UserID: ownerID, Role: team.TeamRoleOwner},
	}})
	billingMode := apikey.APIKeyBillingModeSubscription

	updated, err := svc.Update(context.Background(), apiKey.ID, memberID, apikey.UpdateAPIKeyRequest{
		BillingMode:             &billingMode,
		PreferredSubscriptionID: &preferredID,
	})

	require.NoError(t, err)
	require.Equal(t, []int64{ownerID}, userRepo.requested)
	require.NotNil(t, updated.User)
	require.Equal(t, ownerID, updated.User.ID)
	require.Equal(t, apikey.APIKeyBillingModeSubscription, updated.BillingMode)
	require.Equal(t, preferredID, *updated.PreferredSubscriptionID)
}

// TestAPIKeyServiceManagedKeyHidden checks that user reads, updates, and deletes treat managed keys as missing.
func TestAPIKeyServiceManagedKeyHidden(t *testing.T) {
	ctx := context.Background()
	managedBy := creative.CreativeManagedBy
	managed := &apikey.APIKey{
		ID:        42,
		UserID:    7,
		Key:       "sk-hidden-managed-key",
		Name:      "creative-studio:12",
		Status:    billing.StatusActive,
		ManagedBy: &managedBy,
	}
	repo := &apiKeyRepoStub{apiKey: managed}
	svc := testkit.NewService(repo, nil, nil, nil, nil, nil, nil)
	svc.Start()

	// Reading a managed key returns no key and ErrAPIKeyNotFound.
	got, err := svc.GetByID(ctx, 42)
	require.Nil(t, got)
	require.Error(t, err)
	require.True(t, errors.Is(err, apikey.ErrAPIKeyNotFound), "managed key GetByID 必须返回 ErrAPIKeyNotFound")

	// Updating a managed key must also hide its existence.
	hackedName := "hacked"
	updated, err := svc.Update(ctx, 42, 7, apikey.UpdateAPIKeyRequest{Name: &hackedName})
	require.Nil(t, updated)
	require.True(t, errors.Is(err, apikey.ErrAPIKeyNotFound), "managed key Update 必须返回 ErrAPIKeyNotFound")

	// Reject deletion before calling DeleteWithAudit.
	err = svc.Delete(ctx, 42, 7)
	require.True(t, errors.Is(err, apikey.ErrAPIKeyNotFound), "managed key Delete 必须返回 ErrAPIKeyNotFound")
	require.Empty(t, repo.deletedIDs, "managed key 不得进入删除流程")
}

func TestAPIKeyService_GetByKey_UsesL2Cache(t *testing.T) {
	cache := &authCacheStub{}
	repo := &authRepoStub{
		getByKeyForAuth: func(ctx context.Context, key string) (*apikey.APIKey, error) {
			return nil, errors.New("unexpected repo call")
		},
	}
	cfg := &config.Config{
		APIKeyAuth: config.APIKeyAuthCacheConfig{
			L2TTLSeconds:       60,
			NegativeTTLSeconds: 30,
		},
	}
	svc := testkit.NewService(repo, nil, nil, nil, nil, cache, cfg)
	svc.Start()

	groupID := int64(9)
	cacheEntry := &apikey.APIKeyAuthCacheEntry{
		Snapshot: &apikey.APIKeyAuthSnapshot{
			Version:  apikey.KeyApiKeyAuthSnapshotVersion,
			APIKeyID: 1,
			UserID:   2,
			GroupID:  &groupID,
			Status:   billing.StatusActive,
			User: apikey.APIKeyAuthUserSnapshot{
				ID:          2,
				Status:      billing.StatusActive,
				Role:        identity.RoleUser,
				Balance:     10,
				Concurrency: 3,
			},
			Group: &apikey.APIKeyAuthGroupSnapshot{
				ID:   groupID,
				Name: "g",

				Status:              billing.StatusActive,
				RateMultiplier:      1,
				ModelRoutingEnabled: true,
				ModelRouting: map[string][]int64{
					"claude-opus-*": {1, 2},
				},
			},
		},
	}
	cache.getAuthCache = func(ctx context.Context, key string) (*apikey.APIKeyAuthCacheEntry, error) {
		return cacheEntry, nil
	}

	apiKey, err := svc.GetByKey(context.Background(), "k1")
	require.NoError(t, err)
	require.Equal(t, int64(1), apiKey.ID)
	require.Equal(t, int64(2), apiKey.User.ID)
	require.Equal(t, groupID, apiKey.Group.ID)
	require.True(t, apiKey.Group.ModelRoutingEnabled)
	require.Equal(t, map[string][]int64{"claude-opus-*": {1, 2}}, apiKey.Group.ModelRouting)
}

func TestAPIKeyService_GetByKey_KeepsDisabledGroupWithoutConfiguredFallbackFromRepo(t *testing.T) {
	disabledGroupID := int64(9)
	defaultGroupID := int64(10)
	defaultRPM := 77
	oldRPM := 3
	repo := &authRepoStub{
		getByKeyForAuth: func(ctx context.Context, key string) (*apikey.APIKey, error) {
			return &apikey.APIKey{
				ID:                           1,
				UserID:                       2,
				GroupID:                      &disabledGroupID,
				Key:                          key,
				Status:                       billing.StatusActive,
				FallbackWhenGroupUnavailable: true,
				User: &identity.User{
					ID:                   2,
					Status:               billing.StatusActive,
					Role:                 identity.RoleUser,
					Balance:              10,
					Concurrency:          3,
					UserGroupRPMOverride: &oldRPM,
				},
				Group: &routing.Group{
					ID:   disabledGroupID,
					Name: "openai-disabled",

					Status:         billing.StatusDisabled,
					Hydrated:       true,
					RateMultiplier: 9,
				},
			}, nil
		},
	}
	groupRepo := &authGroupRepoStub{
		groupsByPlatform: map[string][]routing.Group{
			capability.PlatformOpenAI: {
				{
					ID:   defaultGroupID,
					Name: "openai-default",

					Status:   billing.StatusActive,
					Hydrated: true,

					RateMultiplier: 1.5,
				},
			},
		},
	}
	rateRepo := &authUserGroupRateRepoStub{overrides: map[int64]*int{defaultGroupID: &defaultRPM}}
	svc := testkit.NewService(repo, nil, groupRepo, nil, rateRepo, nil, &config.Config{})
	svc.Start()

	apiKey, err := svc.GetByKey(context.Background(), "k-disabled")
	require.NoError(t, err)
	require.NotNil(t, apiKey.GroupID)
	require.Equal(t, disabledGroupID, *apiKey.GroupID)
	require.NotNil(t, apiKey.Group)
	require.Equal(t, disabledGroupID, apiKey.Group.ID)
	require.Equal(t, billing.StatusDisabled, apiKey.Group.Status)
	require.Nil(t, apiKey.User.UserGroupRPMOverride)
	require.Empty(t, rateRepo.calls)
}

func TestAPIKeyService_GetByKey_FallsBackDisabledBoundGroupToConfiguredGroup(t *testing.T) {
	disabledGroupID := int64(9)
	configuredFallbackID := int64(11)
	defaultGroupID := int64(10)
	repo := &authRepoStub{
		getByKeyForAuth: func(ctx context.Context, key string) (*apikey.APIKey, error) {
			return &apikey.APIKey{
				ID:                           1,
				UserID:                       2,
				GroupID:                      &disabledGroupID,
				Key:                          key,
				Status:                       billing.StatusActive,
				FallbackWhenGroupUnavailable: true,
				User: &identity.User{
					ID:          2,
					Status:      billing.StatusActive,
					Role:        identity.RoleUser,
					Balance:     10,
					Concurrency: 3,
				},
				Group: &routing.Group{
					ID:   disabledGroupID,
					Name: "openai-disabled",

					Status:                     billing.StatusDisabled,
					Hydrated:                   true,
					UnavailableFallbackGroupID: &configuredFallbackID,
				},
			}, nil
		},
	}
	groupRepo := &authGroupRepoStub{
		groupsByID: map[int64]routing.Group{
			configuredFallbackID: {
				ID:   configuredFallbackID,
				Name: "openai-configured-fallback",

				Status:         billing.StatusActive,
				Hydrated:       true,
				RateMultiplier: 1.2,
			},
		},
		groupsByPlatform: map[string][]routing.Group{
			capability.PlatformOpenAI: {
				{
					ID:   defaultGroupID,
					Name: "openai-default",

					Status:   billing.StatusActive,
					Hydrated: true,

					RateMultiplier: 1,
				},
			},
		},
	}
	svc := testkit.NewService(repo, nil, groupRepo, nil, nil, nil, &config.Config{})
	svc.Start()

	apiKey, err := svc.GetByKey(context.Background(), "k-disabled")
	require.NoError(t, err)
	require.NotNil(t, apiKey.GroupID)
	require.Equal(t, configuredFallbackID, *apiKey.GroupID)
	require.NotNil(t, apiKey.Group)
	require.Equal(t, configuredFallbackID, apiKey.Group.ID)
	require.Equal(t, "openai-configured-fallback", apiKey.Group.Name)
}

func TestAPIKeyService_GetByKey_UsesConfiguredFallbackWithoutPlatformConstraint(t *testing.T) {
	disabledGroupID := int64(9)
	configuredFallbackID := int64(11)
	defaultGroupID := int64(10)
	repo := &authRepoStub{
		getByKeyForAuth: func(ctx context.Context, key string) (*apikey.APIKey, error) {
			return &apikey.APIKey{
				ID:                           1,
				UserID:                       2,
				GroupID:                      &disabledGroupID,
				Key:                          key,
				Status:                       billing.StatusActive,
				FallbackWhenGroupUnavailable: true,
				User: &identity.User{
					ID:          2,
					Status:      billing.StatusActive,
					Role:        identity.RoleUser,
					Balance:     10,
					Concurrency: 3,
				},
				Group: &routing.Group{
					ID:   disabledGroupID,
					Name: "openai-disabled",

					Status:                     billing.StatusDisabled,
					Hydrated:                   true,
					UnavailableFallbackGroupID: &configuredFallbackID,
				},
			}, nil
		},
	}
	groupRepo := &authGroupRepoStub{
		groupsByID: map[int64]routing.Group{
			configuredFallbackID: {
				ID:   configuredFallbackID,
				Name: "mixed-fallback",

				Status:   billing.StatusActive,
				Hydrated: true,
			},
		},
		groupsByPlatform: map[string][]routing.Group{
			capability.PlatformOpenAI: {
				{
					ID:   defaultGroupID,
					Name: "openai-default",

					Status:   billing.StatusActive,
					Hydrated: true,
				},
			},
		},
	}
	svc := testkit.NewService(repo, nil, groupRepo, nil, nil, nil, &config.Config{})
	svc.Start()

	apiKey, err := svc.GetByKey(context.Background(), "k-disabled")
	require.NoError(t, err)
	require.NotNil(t, apiKey.GroupID)
	require.Equal(t, configuredFallbackID, *apiKey.GroupID)
	require.NotNil(t, apiKey.Group)
	require.Equal(t, configuredFallbackID, apiKey.Group.ID)
}

func TestAPIKeyService_GetByKey_KeepsDisabledGroupWithoutConfiguredFallbackFromAuthCache(t *testing.T) {
	cache := &authCacheStub{}
	repo := &authRepoStub{
		getByKeyForAuth: func(ctx context.Context, key string) (*apikey.APIKey, error) {
			return nil, errors.New("unexpected repo call")
		},
	}
	disabledGroupID := int64(9)
	defaultGroupID := int64(10)
	groupRepo := &authGroupRepoStub{
		groupsByPlatform: map[string][]routing.Group{
			capability.PlatformGemini: {
				{
					ID:   defaultGroupID,
					Name: "gemini-default",

					Status:   billing.StatusActive,
					Hydrated: true,

					RateMultiplier: 2,
				},
			},
		},
	}
	oldRPM := 3
	cache.getAuthCache = func(ctx context.Context, key string) (*apikey.APIKeyAuthCacheEntry, error) {
		return &apikey.APIKeyAuthCacheEntry{
			Snapshot: &apikey.APIKeyAuthSnapshot{
				Version:                      apikey.KeyApiKeyAuthSnapshotVersion,
				APIKeyID:                     1,
				UserID:                       2,
				GroupID:                      &disabledGroupID,
				Status:                       billing.StatusActive,
				FallbackWhenGroupUnavailable: true,
				User: apikey.APIKeyAuthUserSnapshot{
					ID:                   2,
					Status:               billing.StatusActive,
					Role:                 identity.RoleUser,
					Balance:              10,
					Concurrency:          3,
					UserGroupRPMOverride: &oldRPM,
				},
				Group: &apikey.APIKeyAuthGroupSnapshot{
					ID:   disabledGroupID,
					Name: "gemini-disabled",

					Status:         billing.StatusDisabled,
					RateMultiplier: 8,
				},
			},
		}, nil
	}
	cfg := &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L2TTLSeconds: 60}}
	svc := testkit.NewService(repo, nil, groupRepo, nil, nil, cache, cfg)
	svc.Start()

	apiKey, err := svc.GetByKey(context.Background(), "k-cached-disabled")
	require.NoError(t, err)
	require.NotNil(t, apiKey.GroupID)
	require.Equal(t, disabledGroupID, *apiKey.GroupID)
	require.NotNil(t, apiKey.Group)
	require.Equal(t, disabledGroupID, apiKey.Group.ID)
	require.NotNil(t, apiKey.User.UserGroupRPMOverride)
	require.Equal(t, oldRPM, *apiKey.User.UserGroupRPMOverride)
}

func TestAPIKeyService_GetByKey_DoesNotFallbackDeletedOrMissingBoundGroup(t *testing.T) {
	t.Run("deleted status", func(t *testing.T) {
		groupID := int64(9)
		repo := &authRepoStub{
			getByKeyForAuth: func(ctx context.Context, key string) (*apikey.APIKey, error) {
				return &apikey.APIKey{
					ID:      1,
					UserID:  2,
					GroupID: &groupID,
					Key:     key,
					Status:  billing.StatusActive,
					User: &identity.User{
						ID:          2,
						Status:      billing.StatusActive,
						Role:        identity.RoleUser,
						Balance:     10,
						Concurrency: 3,
					},
					Group: &routing.Group{
						ID:   groupID,
						Name: "deleted",

						Status:   "deleted",
						Hydrated: true,
					},
				}, nil
			},
		}
		groupRepo := &authGroupRepoStub{
			groupsByPlatform: map[string][]routing.Group{
				capability.PlatformOpenAI: {{ID: 10, Name: "openai-default", Status: billing.StatusActive, Hydrated: true}},
			},
		}
		ctx := apikey.WithInboundEndpoint(context.Background(), "/v1/images/generations")
		svc := testkit.NewService(repo, nil, groupRepo, nil, nil, nil, &config.Config{})
		svc.Start()

		apiKey, err := svc.GetByKey(ctx, "k-deleted")
		require.NoError(t, err)
		require.NotNil(t, apiKey.GroupID)
		require.Equal(t, groupID, *apiKey.GroupID)
		require.Equal(t, groupID, apiKey.Group.ID)
	})

	t.Run("missing edge", func(t *testing.T) {
		groupID := int64(9)
		repo := &authRepoStub{
			getByKeyForAuth: func(ctx context.Context, key string) (*apikey.APIKey, error) {
				return &apikey.APIKey{
					ID:      1,
					UserID:  2,
					GroupID: &groupID,
					Key:     key,
					Status:  billing.StatusActive,
					User: &identity.User{
						ID:          2,
						Status:      billing.StatusActive,
						Role:        identity.RoleUser,
						Balance:     10,
						Concurrency: 3,
					},
				}, nil
			},
		}
		groupRepo := &authGroupRepoStub{
			groupsByPlatform: map[string][]routing.Group{
				capability.PlatformAnthropic: {{ID: 10, Name: "default", Status: billing.StatusActive, Hydrated: true}},
			},
		}
		ctx := apikey.WithInboundEndpoint(context.Background(), "/v1/messages")
		svc := testkit.NewService(repo, nil, groupRepo, nil, nil, nil, &config.Config{})
		svc.Start()

		apiKey, err := svc.GetByKey(ctx, "k-missing-group")
		require.NoError(t, err)
		require.NotNil(t, apiKey.GroupID)
		require.Equal(t, groupID, *apiKey.GroupID)
		require.Nil(t, apiKey.Group)
	})
}

func TestAPIKeyService_GetByKey_NegativeCache(t *testing.T) {
	cache := &authCacheStub{}
	repo := &authRepoStub{
		getByKeyForAuth: func(ctx context.Context, key string) (*apikey.APIKey, error) {
			return nil, errors.New("unexpected repo call")
		},
	}
	cfg := &config.Config{
		APIKeyAuth: config.APIKeyAuthCacheConfig{
			L2TTLSeconds:       60,
			NegativeTTLSeconds: 30,
		},
	}
	svc := testkit.NewService(repo, nil, nil, nil, nil, cache, cfg)
	svc.Start()
	cache.getAuthCache = func(ctx context.Context, key string) (*apikey.APIKeyAuthCacheEntry, error) {
		return &apikey.APIKeyAuthCacheEntry{NotFound: true}, nil
	}

	_, err := svc.GetByKey(context.Background(), "missing")
	require.ErrorIs(t, err, apikey.ErrAPIKeyNotFound)
}

func TestAPIKeyService_GetByKey_CacheMissStoresL2(t *testing.T) {
	cache := &authCacheStub{}
	repo := &authRepoStub{
		getByKeyForAuth: func(ctx context.Context, key string) (*apikey.APIKey, error) {
			return &apikey.APIKey{
				ID:     5,
				UserID: 7,
				Status: billing.StatusActive,
				User: &identity.User{
					ID:          7,
					Status:      billing.StatusActive,
					Role:        identity.RoleUser,
					Balance:     12,
					Concurrency: 2,
				},
			}, nil
		},
	}
	cfg := &config.Config{
		APIKeyAuth: config.APIKeyAuthCacheConfig{
			L2TTLSeconds:       60,
			NegativeTTLSeconds: 30,
		},
	}
	svc := testkit.NewService(repo, nil, nil, nil, nil, cache, cfg)
	svc.Start()
	cache.getAuthCache = func(ctx context.Context, key string) (*apikey.APIKeyAuthCacheEntry, error) {
		return nil, errAuthCacheMiss
	}

	apiKey, err := svc.GetByKey(context.Background(), "k2")
	require.NoError(t, err)
	require.Equal(t, int64(5), apiKey.ID)
	require.Len(t, cache.setAuthKeys, 1)
}

func TestAPIKeyService_GetByKeyRejectsInvalidLengthBeforeCaches(t *testing.T) {
	var cacheCalls atomic.Int32
	cache := &authCacheStub{getAuthCache: func(context.Context, string) (*apikey.APIKeyAuthCacheEntry, error) {
		cacheCalls.Add(1)
		return nil, errAuthCacheMiss
	}}
	repo := &authRepoStub{getByKeyForAuth: func(context.Context, string) (*apikey.APIKey, error) {
		t.Fatal("invalid credential reached repository")
		return nil, nil
	}}
	svc := testkit.NewService(repo, nil, nil, nil, nil, cache, &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{L2TTLSeconds: 60}})
	svc.Start()

	for _, key := range []string{"", strings.Repeat("x", apikey.MaxAPIKeyCredentialBytes+1)} {
		_, err := svc.GetByKey(context.Background(), key)
		require.ErrorIs(t, err, apikey.ErrAPIKeyNotFound)
	}
	require.Zero(t, cacheCalls.Load())
}

func TestAPIKeyService_GetByKeyAllowsMaximumLength(t *testing.T) {
	key := strings.Repeat("x", apikey.MaxAPIKeyCredentialBytes)
	var repoCalls atomic.Int32
	repo := &authRepoStub{getByKeyForAuth: func(_ context.Context, got string) (*apikey.APIKey, error) {
		repoCalls.Add(1)
		require.Equal(t, key, got)
		return nil, apikey.ErrAPIKeyNotFound
	}}
	svc := testkit.NewService(repo, nil, nil, nil, nil, nil, &config.Config{})
	svc.Start()
	_, err := svc.GetByKey(context.Background(), key)
	require.ErrorIs(t, err, apikey.ErrAPIKeyNotFound)
	require.Equal(t, int32(1), repoCalls.Load())
}

func TestAPIKeyService_AuthLookupBulkheadRejectsExcessMisses(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	repo := &authRepoStub{getByKeyForAuth: func(context.Context, string) (*apikey.APIKey, error) {
		close(entered)
		<-release
		return nil, apikey.ErrAPIKeyNotFound
	}}
	svc := testkit.NewService(repo, nil, nil, nil, nil, nil, &config.Config{APIKeyAuth: config.APIKeyAuthCacheConfig{LookupConcurrency: 1}})
	svc.Start()

	done := make(chan error, 1)
	go func() {
		_, err := svc.GetByKey(context.Background(), "first")
		done <- err
	}()
	<-entered

	_, err := svc.GetByKey(context.Background(), "second")
	require.ErrorIs(t, err, apikey.ErrAPIKeyAuthOverloaded)
	metrics := svc.AuthLookupMetrics()
	require.Equal(t, uint64(2), metrics.Total)
	require.Equal(t, uint64(1), metrics.Rejected)
	require.Equal(t, int64(1), metrics.InFlight)
	require.Equal(t, 1, metrics.Capacity)

	close(release)
	require.ErrorIs(t, <-done, apikey.ErrAPIKeyNotFound)
}

func TestAPIKeyService_GetByKey_SingleflightCollapses(t *testing.T) {
	var calls int32
	cache := &authCacheStub{}
	repo := &authRepoStub{
		getByKeyForAuth: func(ctx context.Context, key string) (*apikey.APIKey, error) {
			atomic.AddInt32(&calls, 1)
			time.Sleep(50 * time.Millisecond)
			return &apikey.APIKey{
				ID:     11,
				UserID: 2,
				Status: billing.StatusActive,
				User: &identity.User{
					ID:          2,
					Status:      billing.StatusActive,
					Role:        identity.RoleUser,
					Balance:     1,
					Concurrency: 1,
				},
			}, nil
		},
	}
	cfg := &config.Config{
		APIKeyAuth: config.APIKeyAuthCacheConfig{
			Singleflight: true,
		},
	}
	svc := testkit.NewService(repo, nil, nil, nil, nil, cache, cfg)
	svc.Start()

	start := make(chan struct{})
	wg := sync.WaitGroup{}
	errs := make([]error, 5)
	for i := range 5 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, err := svc.GetByKey(context.Background(), "k1")
			errs[idx] = err
		}(i)
	}
	close(start)
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

// TestAPIKeyServiceZeroValueLookup checks that an unconfigured service returns a missing-key error.
func TestAPIKeyServiceZeroValueLookup(t *testing.T) {
	var svc apikey.APIKeyService
	key, err := svc.GetByKey(context.Background(), "sk-zero-value")
	require.Nil(t, key)
	require.ErrorIs(t, err, apikey.ErrAPIKeyNotFound)
	require.Equal(t, "get api key: "+apikey.ErrAPIKeyNotFound.Error(), err.Error())
}

// TestApiKeyService_Delete_OwnerMismatch rejects a different user before deletion or cache invalidation.
func TestApiKeyService_Delete_OwnerMismatch(t *testing.T) {
	repo := &apiKeyRepoStub{
		apiKey: &apikey.APIKey{ID: 10, UserID: 1, Key: "k"},
	}
	cache := &apiKeyCacheStub{}
	svc := newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo, cache: cache})

	err := svc.Delete(context.Background(), 10, 2) // Key 10 belongs to user 1; user 2 attempts deletion.
	require.ErrorIs(t, err, identity.ErrInsufficientPerms)
	require.Empty(t, repo.deletedIDs)   // No deletion attempted
	require.Empty(t, cache.invalidated) // No creation-counter invalidation
	require.Empty(t, cache.deleteAuthKeys)
}

// TestApiKeyService_Delete_NotFound propagates the lookup error before deletion or cache invalidation.
func TestApiKeyService_Delete_NotFound(t *testing.T) {
	repo := &apiKeyRepoStub{getByIDErr: apikey.ErrAPIKeyNotFound}
	cache := &apiKeyCacheStub{}
	svc := newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo, cache: cache})

	err := svc.Delete(context.Background(), 99, 1)
	require.ErrorIs(t, err, apikey.ErrAPIKeyNotFound)
	require.Empty(t, repo.deletedIDs)
	require.Empty(t, cache.invalidated)
	require.Empty(t, cache.deleteAuthKeys)
}

func TestAPIKeyService_List_FillsCurrentConcurrency(t *testing.T) {
	repo := &apiKeyRepoStub{
		allowListByUserID: true,
		listByUserIDKeys: []apikey.APIKey{
			{ID: 10, UserID: 7, Key: "sk-10", Name: "key-10"},
			{ID: 11, UserID: 7, Key: "sk-11", Name: "key-11"},
		},
	}
	concurrency := scheduler.NewConcurrencyService(&stubConcurrencyCacheForTest{
		apiKeyConcurrency: map[int64]int{10: 2, 11: 0},
	})
	svc := newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo, concurrencyService: concurrency})

	keys, _, err := svc.List(context.Background(), 7, pagination.PaginationParams{Page: 1, PageSize: 20}, apikey.APIKeyListFilters{})
	require.NoError(t, err)
	require.Len(t, keys, 2)
	require.Equal(t, 2, keys[0].CurrentConcurrency)
	require.Equal(t, 0, keys[1].CurrentConcurrency)
}

func TestAPIKeyService_GetByID_FillsCurrentConcurrency(t *testing.T) {
	repo := &apiKeyRepoStub{
		apiKey: &apikey.APIKey{ID: 10, UserID: 7, Key: "sk-10", Name: "key-10"},
	}
	concurrency := scheduler.NewConcurrencyService(&stubConcurrencyCacheForTest{
		apiKeyConcurrency: map[int64]int{10: 4},
	})
	svc := newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo, concurrencyService: concurrency})

	key, err := svc.GetByID(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 4, key.CurrentConcurrency)
}

// TestApiKeyService_Delete_DeleteFails reports the persistence failure and leaves caches intact.
func TestApiKeyService_Delete_DeleteFails(t *testing.T) {
	repo := &apiKeyRepoStub{
		apiKey:    &apikey.APIKey{ID: 42, UserID: 3, Key: "k"},
		deleteErr: errors.New("delete failed"),
	}
	cache := &apiKeyCacheStub{}
	svc := newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo, cache: cache})

	err := svc.Delete(context.Background(), 3, 3) // The fixture supplies user 3 as owner.
	require.Error(t, err)
	require.ErrorContains(t, err, "delete api key")
	require.Equal(t, []int64{3}, repo.deletedIDs) // DeleteWithAudit was attempted
	require.Empty(t, cache.invalidated)           // Creation-counter cache remains intact
	require.Empty(t, cache.deleteAuthKeys)        // Authentication cache remains intact
}

func TestAPIKeyService_Create_EscapesNameBeforePersist(t *testing.T) {
	repo := &apiKeyNameSanitizeRepoStub{}
	svc := newAPIKeyTestService(apiKeyTestDependencies{
		apiKeyRepo: repo,
		groupRepo:  &compositeGroupRepoStub{groups: map[int64]*routing.Group{1: {ID: 1, Status: billing.StatusActive, Hydrated: true}}},
		userRepo:   &userRepoStub{user: &identity.User{ID: 7, Status: billing.StatusActive, Role: identity.RoleUser}},
	})
	customKey := "sk_valid_xss_key_1"

	created, err := svc.Create(context.Background(), 7, apikey.CreateAPIKeyRequest{
		GroupID:   sanitizeFixtureGroupID(),
		Name:      `<img src=x onerror=alert(1)>`,
		CustomKey: &customKey,
	})

	require.NoError(t, err)
	require.Equal(t, "&lt;img src=x onerror=alert(1)&gt;", created.Name)
	require.Len(t, repo.created, 1)
	require.Equal(t, "&lt;img src=x onerror=alert(1)&gt;", repo.created[0].Name)
}

func TestAPIKeyService_Create_DefaultsConfiguredGroupFallbackEnabled(t *testing.T) {
	repo := &apiKeyNameSanitizeRepoStub{}
	svc := newAPIKeyTestService(apiKeyTestDependencies{
		apiKeyRepo: repo,
		groupRepo:  &compositeGroupRepoStub{groups: map[int64]*routing.Group{1: {ID: 1, Status: billing.StatusActive, Hydrated: true}}},
		userRepo:   &userRepoStub{user: &identity.User{ID: 7, Status: billing.StatusActive, Role: identity.RoleUser}},
	})
	customKey := "sk_valid_default_fallback"

	created, err := svc.Create(context.Background(), 7, apikey.CreateAPIKeyRequest{
		GroupID:   sanitizeFixtureGroupID(),
		Name:      "default fallback",
		CustomKey: &customKey,
	})

	require.NoError(t, err)
	require.True(t, created.FallbackWhenGroupUnavailable)
	require.Len(t, repo.created, 1)
	require.True(t, repo.created[0].FallbackWhenGroupUnavailable)
	require.Equal(t, apikey.APIKeyFastModePolicyFollowRequest, created.FastModePolicy)
}

func TestAPIKeyService_CreateRejectsInvalidFastModePolicy(t *testing.T) {
	svc := newAPIKeyTestService(apiKeyTestDependencies{})

	_, err := svc.Create(context.Background(), 7, apikey.CreateAPIKeyRequest{GroupID: sanitizeFixtureGroupID(), FastModePolicy: "invalid"})
	require.ErrorIs(t, err, apikey.ErrInvalidAPIKeyFastModePolicy)
}

func TestAPIKeyService_Create_AllowsDisablingGroupFallback(t *testing.T) {
	repo := &apiKeyNameSanitizeRepoStub{}
	svc := newAPIKeyTestService(apiKeyTestDependencies{
		apiKeyRepo: repo,
		groupRepo:  &compositeGroupRepoStub{groups: map[int64]*routing.Group{1: {ID: 1, Status: billing.StatusActive, Hydrated: true}}},
		userRepo:   &userRepoStub{user: &identity.User{ID: 7, Status: billing.StatusActive, Role: identity.RoleUser}},
	})
	customKey := "sk_valid_disabled_fallback"
	fallback := false

	created, err := svc.Create(context.Background(), 7, apikey.CreateAPIKeyRequest{
		GroupID:                      sanitizeFixtureGroupID(),
		Name:                         "disabled fallback",
		CustomKey:                    &customKey,
		FallbackWhenGroupUnavailable: &fallback,
	})

	require.NoError(t, err)
	require.False(t, created.FallbackWhenGroupUnavailable)
	require.Len(t, repo.created, 1)
	require.False(t, repo.created[0].FallbackWhenGroupUnavailable)
}

func TestAPIKeyService_Update_EscapesNameBeforePersist(t *testing.T) {
	repo := &apiKeyNameSanitizeRepoStub{
		apiKey: &apikey.APIKey{ID: 11, UserID: 7, Key: "sk_existing_key_01", Name: "old", Status: billing.StatusActive},
	}
	svc := newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo})
	name := `<script>alert("x")</script>`

	updated, err := svc.Update(context.Background(), 11, 7, apikey.UpdateAPIKeyRequest{Name: &name})

	require.NoError(t, err)
	require.Equal(t, "&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;", updated.Name)
	require.Len(t, repo.updated, 1)
	require.Equal(t, "&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;", repo.updated[0].Name)
}

func TestAPIKeyService_UpdatePersistsFastModePolicy(t *testing.T) {
	repo := &apiKeyNameSanitizeRepoStub{
		apiKey: &apikey.APIKey{ID: 11, UserID: 7, Key: "sk_existing_key_02", Name: "old", Status: billing.StatusActive, FastModePolicy: apikey.APIKeyFastModePolicyFollowRequest},
	}
	svc := newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo})
	policy := apikey.APIKeyFastModePolicyForceOff

	updated, err := svc.Update(context.Background(), 11, 7, apikey.UpdateAPIKeyRequest{FastModePolicy: &policy})
	require.NoError(t, err)
	require.Equal(t, apikey.APIKeyFastModePolicyForceOff, updated.FastModePolicy)
	require.Equal(t, apikey.APIKeyFastModePolicyForceOff, repo.updated[0].FastModePolicy)
}

// TestAPIKeyService_UpdatePreservesOmittedIPRestrictions checks that omitted rule fields retain their values.
func TestAPIKeyService_UpdatePreservesOmittedIPRestrictions(t *testing.T) {
	repo := &apiKeyNameSanitizeRepoStub{
		apiKey: &apikey.APIKey{
			ID:          11,
			UserID:      7,
			Key:         "sk_existing_ip_key_01",
			Status:      billing.StatusActive,
			IPWhitelist: []string{"192.0.2.10"},
			IPBlacklist: []string{"198.51.100.0/24"},
		},
	}
	svc := newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo})

	updated, err := svc.Update(context.Background(), 11, 7, apikey.UpdateAPIKeyRequest{})

	require.NoError(t, err)
	require.Equal(t, []string{"192.0.2.10"}, updated.IPWhitelist)
	require.Equal(t, []string{"198.51.100.0/24"}, updated.IPBlacklist)
}

// TestAPIKeyService_UpdateClearsExplicitEmptyIPRestriction clears an empty rule set while retaining the omitted set.
func TestAPIKeyService_UpdateClearsExplicitEmptyIPRestriction(t *testing.T) {
	repo := &apiKeyNameSanitizeRepoStub{
		apiKey: &apikey.APIKey{
			ID:          11,
			UserID:      7,
			Key:         "sk_existing_ip_key_02",
			Status:      billing.StatusActive,
			IPWhitelist: []string{"192.0.2.10"},
			IPBlacklist: []string{"198.51.100.0/24"},
		},
	}
	svc := newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo})
	emptyWhitelist := []string{}

	updated, err := svc.Update(context.Background(), 11, 7, apikey.UpdateAPIKeyRequest{IPWhitelist: &emptyWhitelist})

	require.NoError(t, err)
	require.Empty(t, updated.IPWhitelist)
	require.Equal(t, []string{"198.51.100.0/24"}, updated.IPBlacklist)
}

// TestAPIKeyService_UpdateRejectsInvalidIPRestriction checks pattern validation during updates.
func TestAPIKeyService_UpdateRejectsInvalidIPRestriction(t *testing.T) {
	repo := &apiKeyNameSanitizeRepoStub{
		apiKey: &apikey.APIKey{ID: 11, UserID: 7, Key: "sk_existing_ip_key_03", Status: billing.StatusActive},
	}
	svc := newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo})
	invalidBlacklist := []string{"not-an-ip"}

	_, err := svc.Update(context.Background(), 11, 7, apikey.UpdateAPIKeyRequest{IPBlacklist: &invalidBlacklist})

	require.ErrorIs(t, err, apikey.ErrInvalidIPPattern)
	require.Empty(t, repo.updated)
}

func TestAPIKeyService_UpdateQuotaUsed_UsesAtomicStatePath(t *testing.T) {
	repo := &quotaStateRepoStub{
		state: &apikey.APIKeyQuotaUsageState{
			QuotaUsed: 12,
			Quota:     10,
			Key:       "sk-test-quota",
			Status:    apikey.StatusAPIKeyQuotaExhausted,
		},
	}
	cache := &quotaStateCacheStub{}
	svc := newAPIKeyTestService(apiKeyTestDependencies{
		apiKeyRepo: repo,
		cache:      cache,
	})

	err := svc.UpdateQuotaUsed(context.Background(), 101, 2)
	require.NoError(t, err)
	require.Equal(t, 1, repo.stateCalls)
	require.Equal(t, 0, repo.getByIDCalls, "fast path should not re-read API key by id")
	require.Equal(t, []string{svc.KeyAuthCacheKey("sk-test-quota")}, cache.deleteAuthKeys)
}

func TestAPIKeyService_Update_ReactivatesQuotaExhaustedWhenQuotaUnlimited(t *testing.T) {
	repo := &apiKeyRepoStub{
		apiKey: &apikey.APIKey{
			ID:        10,
			UserID:    7,
			Key:       "sk-test-unlimited",
			Status:    apikey.StatusAPIKeyQuotaExhausted,
			Quota:     10,
			QuotaUsed: 12,
		},
	}
	svc := newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo})
	quota := 0.0

	updated, err := svc.Update(context.Background(), 10, 7, apikey.UpdateAPIKeyRequest{Quota: &quota})

	require.NoError(t, err)
	require.Equal(t, billing.StatusActive, updated.Status)
	require.Equal(t, 0.0, updated.Quota)
	require.Len(t, repo.updatedKeys, 1)
	require.Equal(t, billing.StatusActive, repo.updatedKeys[0].Status)
	require.Equal(t, 0.0, repo.updatedKeys[0].Quota)
}

func TestAPIKeyUpdate_OnlyDeclaresRequestedColumns(t *testing.T) {
	name := "renamed"
	zero := 0
	quota := 500.0
	rateLimit := 42.0
	whitelist := []string{"10.0.0.1"}
	fastModePolicy := apikey.APIKeyFastModePolicyForceOff
	fallbackToDefaultGroup := false
	modelMapping := map[string]string{"review": "gpt-5.6-luna"}

	tests := []struct {
		name string
		req  apikey.UpdateAPIKeyRequest
		want apikey.APIKeyUpdateFields
	}{
		{
			name: "clear concurrency limit",
			req:  apikey.UpdateAPIKeyRequest{ConcurrencyLimit: &zero},
			want: apikey.APIKeyUpdateFields{ConcurrencyLimit: true},
		},
		{
			name: "clear rpm limit",
			req:  apikey.UpdateAPIKeyRequest{RPMLimit: &zero},
			want: apikey.APIKeyUpdateFields{RPMLimit: true},
		},
		{
			name: "model mapping only",
			req:  apikey.UpdateAPIKeyRequest{ModelMapping: &modelMapping},
			want: apikey.APIKeyUpdateFields{ModelMapping: true},
		},
		{
			name: "name only",
			req:  apikey.UpdateAPIKeyRequest{Name: &name},
			want: apikey.APIKeyUpdateFields{Name: true},
		},
		{
			name: "quota only",
			req:  apikey.UpdateAPIKeyRequest{Quota: &quota},
			want: apikey.APIKeyUpdateFields{Quota: true},
		},
		{
			name: "rate limit threshold only",
			req:  apikey.UpdateAPIKeyRequest{RateLimit5h: &rateLimit},
			want: apikey.APIKeyUpdateFields{RateLimits: true},
		},
		{
			name: "ip whitelist only",
			req:  apikey.UpdateAPIKeyRequest{IPWhitelist: &whitelist},
			want: apikey.APIKeyUpdateFields{IPRules: true},
		},
		{
			name: "fork fast mode policy only",
			req:  apikey.UpdateAPIKeyRequest{FastModePolicy: &fastModePolicy},
			want: apikey.APIKeyUpdateFields{FastModePolicy: true},
		},
		{
			name: "fork group fallback policy only",
			req:  apikey.UpdateAPIKeyRequest{FallbackWhenGroupUnavailable: &fallbackToDefaultGroup},
			want: apikey.APIKeyUpdateFields{FallbackWhenGroupUnavailable: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newUpdateFieldsAPIKeyService(&apikey.APIKey{
				ID:        1,
				UserID:    7,
				Key:       "sk-test",
				Name:      "before",
				Status:    billing.StatusActive,
				Quota:     100,
				QuotaUsed: 30,
				Usage5h:   12,
			})

			_, err := svc.Update(context.Background(), 1, 7, tt.req)
			require.NoError(t, err)
			require.Equal(t, []apikey.APIKeyUpdateFields{tt.want}, repo.updateFields)
		})
	}
}

func TestAPIKeyUpdateClearsModelMappingWithEmptyObject(t *testing.T) {
	emptyMapping := map[string]string{}
	svc, repo := newUpdateFieldsAPIKeyService(&apikey.APIKey{
		ID: 1, UserID: 7, Key: "sk-test", Status: billing.StatusActive,
		ModelMapping: map[string]string{"review": "gpt-5.6-luna"},
	})

	updated, err := svc.Update(context.Background(), 1, 7, apikey.UpdateAPIKeyRequest{ModelMapping: &emptyMapping})
	require.NoError(t, err)
	require.Empty(t, updated.ModelMapping)
	require.Equal(t, []apikey.APIKeyUpdateFields{{ModelMapping: true}}, repo.updateFields)
}

// TestAPIKeyUpdate_DeclaresUsageColumnsOnExplicitReset checks that resets select the corresponding usage columns.
func TestAPIKeyUpdate_DeclaresUsageColumnsOnExplicitReset(t *testing.T) {
	reset := true
	svc, repo := newUpdateFieldsAPIKeyService(&apikey.APIKey{
		ID: 1, UserID: 7, Key: "sk-test", Status: billing.StatusActive, Quota: 100, QuotaUsed: 30, Usage5h: 12,
	})

	_, err := svc.Update(context.Background(), 1, 7, apikey.UpdateAPIKeyRequest{
		ResetQuota:          &reset,
		ResetRateLimitUsage: &reset,
	})
	require.NoError(t, err)
	require.Equal(t, []apikey.APIKeyUpdateFields{{QuotaUsed: true, RateLimitUsage: true}}, repo.updateFields)
}

// TestAPIKeyUpdate_DeclaresStatusWhenReactivated checks that increasing quota selects the reactivated status for persistence.
func TestAPIKeyUpdate_DeclaresStatusWhenReactivated(t *testing.T) {
	quota := 500.0
	svc, repo := newUpdateFieldsAPIKeyService(&apikey.APIKey{
		ID: 1, UserID: 7, Key: "sk-test", Status: apikey.StatusAPIKeyQuotaExhausted, Quota: 100, QuotaUsed: 100,
	})

	_, err := svc.Update(context.Background(), 1, 7, apikey.UpdateAPIKeyRequest{Quota: &quota})
	require.NoError(t, err)
	require.Equal(t, []apikey.APIKeyUpdateFields{{Quota: true, Status: true}}, repo.updateFields)
}

// TestUpdateQuotaUsed_ExhaustedMarkOnlyDeclaresStatus checks that exhaustion writes status alone.
// A full snapshot write would overwrite the atomic quota increment.
func TestUpdateQuotaUsed_ExhaustedMarkOnlyDeclaresStatus(t *testing.T) {
	repo := &updateFieldsAPIKeyRepoStub{key: &apikey.APIKey{
		ID: 1, UserID: 7, Key: "sk-test", Status: billing.StatusActive, Quota: 10, QuotaUsed: 10,
	}}
	svc := newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo})

	require.NoError(t, svc.UpdateQuotaUsed(context.Background(), 1, 5))
	require.Equal(t, []apikey.APIKeyUpdateFields{{Status: true}}, repo.updateFields)
}

func TestValidateAPIKeyLimit(t *testing.T) {
	t.Parallel()

	for _, value := range []float64{0, 1, math.Nextafter(apiKeyLimitUpperBound, 0)} {
		require.NoError(t, apikey.ValidateAPIKeyLimit("quota", value))
	}

	for _, value := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1), apiKeyLimitUpperBound, 1e100} {
		err := apikey.ValidateAPIKeyLimit("rate_limit_7d", value)
		require.ErrorIs(t, err, apikey.ErrAPIKeyLimitInvalid)
		require.Equal(t, "API_KEY_LIMIT_INVALID", apperror.Reason(err))
	}
}

func TestValidateCreateAPIKeyRequestNumericLimits(t *testing.T) {
	t.Parallel()

	positiveExpiry := 1
	require.NoError(t, apikey.KeyValidateCreateAPIKeyRequest(apikey.CreateAPIKeyRequest{
		Quota:         math.Nextafter(apiKeyLimitUpperBound, 0),
		RateLimit5h:   10,
		RateLimit1d:   20,
		RateLimit7d:   30,
		ExpiresInDays: &positiveExpiry,
	}))
	require.NoError(t, apikey.KeyValidateCreateAPIKeyRequest(apikey.CreateAPIKeyRequest{}))

	zeroExpiry, negativeExpiry := 0, -1
	invalid := []apikey.CreateAPIKeyRequest{
		{Quota: -1},
		{Quota: math.NaN()},
		{Quota: apiKeyLimitUpperBound},
		{RateLimit5h: math.Inf(1)},
		{RateLimit1d: -1},
		{RateLimit7d: 1e100},
		{ExpiresInDays: &zeroExpiry},
		{ExpiresInDays: &negativeExpiry},
	}
	for _, request := range invalid {
		require.Error(t, apikey.KeyValidateCreateAPIKeyRequest(request))
	}
}

func TestValidateUpdateAPIKeyRequestNumericLimits(t *testing.T) {
	t.Parallel()

	zero := 0.0
	largeValid := math.Nextafter(apiKeyLimitUpperBound, 0)
	require.NoError(t, apikey.KeyValidateUpdateAPIKeyRequest(apikey.UpdateAPIKeyRequest{
		Quota:       &zero,
		RateLimit7d: &largeValid,
		RateLimit1d: nil,
		RateLimit5h: nil,
	}))

	negative, nan, inf, tooLarge := -1.0, math.NaN(), math.Inf(1), float64(apiKeyLimitUpperBound)
	invalid := []apikey.UpdateAPIKeyRequest{
		{Quota: &negative},
		{RateLimit5h: &nan},
		{RateLimit1d: &inf},
		{RateLimit7d: &tooLarge},
	}
	for _, request := range invalid {
		require.ErrorIs(t, apikey.KeyValidateUpdateAPIKeyRequest(request), apikey.ErrAPIKeyLimitInvalid)
	}
}

func TestAPIKeyServiceRejectsInvalidLimitsBeforeRepositoryAccess(t *testing.T) {
	t.Parallel()

	service := newAPIKeyTestService(apiKeyTestDependencies{})
	// A nil context makes accidental repository access fail before input validation.
	var requestContext context.Context
	_, createErr := service.Create(requestContext, 1, apikey.CreateAPIKeyRequest{GroupID: sanitizeFixtureGroupID(), Quota: -1})
	require.ErrorIs(t, createErr, apikey.ErrAPIKeyLimitInvalid)

	invalid := math.Inf(1)
	_, updateErr := service.Update(requestContext, 1, 1, apikey.UpdateAPIKeyRequest{RateLimit5h: &invalid})
	require.ErrorIs(t, updateErr, apikey.ErrAPIKeyLimitInvalid)
}

func TestValidateTeamKeyLifecycle(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*apikey.APIKey)
		cfg    *config.Config
		want   error
	}{
		{name: "valid", cfg: &config.Config{Team: config.TeamConfig{Enabled: true}}},
		{name: "feature_disabled", cfg: &config.Config{}, want: team.ErrTeamFeatureDisabled},
		{name: "membership_missing", cfg: &config.Config{Team: config.TeamConfig{Enabled: true}}, mutate: func(key *apikey.APIKey) { key.TeamMembership = nil }, want: team.ErrTeamMembershipRequired},
		{name: "membership_rejoined_after_key", cfg: &config.Config{Team: config.TeamConfig{Enabled: true}}, mutate: func(key *apikey.APIKey) { key.TeamMembership.JoinedAt = key.CreatedAt.Add(time.Second) }, want: team.ErrTeamMembershipRequired},
		{name: "team_suspended", cfg: &config.Config{Team: config.TeamConfig{Enabled: true}}, mutate: func(key *apikey.APIKey) { key.Team.Status = team.TeamStatusSuspended }, want: team.ErrTeamSuspended},
		{name: "actor_inactive", cfg: &config.Config{Team: config.TeamConfig{Enabled: true}}, mutate: func(key *apikey.APIKey) { key.ActorUser.Status = billing.StatusDisabled }, want: apikey.ErrTeamActorInactive},
		{name: "owner_inactive", cfg: &config.Config{Team: config.TeamConfig{Enabled: true}}, mutate: func(key *apikey.APIKey) { key.User.Status = billing.StatusDisabled }, want: apikey.ErrTeamBillingOwnerInactive},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := validTeamAPIKeyForLifecycleTest()
			if test.mutate != nil {
				test.mutate(key)
			}
			err := newAPIKeyTestService(apiKeyTestDependencies{cfg: test.cfg}).ValidateTeamKeyLifecycle(key)
			if test.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, test.want)
		})
	}
}

// TestAPIKeyCreateRequiresExplicitPositiveGroup rejects ordinary keys without a valid group before repository access.
func TestAPIKeyCreateRequiresExplicitPositiveGroup(t *testing.T) {
	zero, negative := int64(0), int64(-1)
	for _, groupID := range []*int64{nil, &zero, &negative} {
		service := newAPIKeyTestService(apiKeyTestDependencies{})
		_, err := service.Create(context.Background(), 7, apikey.CreateAPIKeyRequest{Name: "missing group", GroupID: groupID})
		require.Error(t, err)
		require.Contains(t, err.Error(), "GROUP_REQUIRED")
	}
}

// TestAPIKeyLoadingPreservesUnboundLegacyKey checks that loading a legacy unbound key does not assign a group.
func TestAPIKeyLoadingPreservesUnboundLegacyKey(t *testing.T) {
	repo := &authRepoStub{getByKeyForAuth: func(_ context.Context, key string) (*apikey.APIKey, error) {
		return &apikey.APIKey{ID: 19, UserID: 7, Key: key, Status: billing.StatusActive, User: &identity.User{ID: 7, Status: billing.StatusActive, Role: identity.RoleUser}}, nil
	}}
	service := newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo})
	loaded, err := service.GetByKey(context.Background(), "unchanged-legacy-key")
	require.NoError(t, err)
	require.Equal(t, "unchanged-legacy-key", loaded.Key)
	require.Nil(t, loaded.GroupID)
	require.Nil(t, loaded.Group)
}

func (s *billingModeSubscriptionRepoStub) GetByID(_ context.Context, id int64) (*billing.UserSubscription, error) {
	subscription := s.subscriptions[id]
	if subscription == nil {
		return nil, billing.ErrSubscriptionNotFound
	}
	copySubscription := *subscription
	return &copySubscription, nil
}

func (s *billingModeSubscriptionRepoStub) ListActiveByUserID(_ context.Context, userID int64) ([]billing.UserSubscription, error) {
	result := make([]billing.UserSubscription, 0)
	for _, subscription := range s.subscriptions {
		if subscription.UserID == userID {
			result = append(result, *subscription)
		}
	}
	return result, nil
}

func (s *billingModeUserRepoStub) GetByID(_ context.Context, id int64) (*identity.User, error) {
	s.requested = append(s.requested, id)
	user := s.users[id]
	if user == nil {
		return nil, identity.ErrUserNotFound
	}
	copyUser := *user
	return &copyUser, nil
}

func activeBillingModeSubscription(id, userID int64, groupIDs ...int64) *billing.UserSubscription {
	now := time.Now()
	return &billing.UserSubscription{
		ID:        id,
		UserID:    userID,
		PlanID:    id,
		Status:    billing.SubscriptionStatusActive,
		StartsAt:  now.Add(-time.Hour),
		ExpiresAt: now.Add(time.Hour),
		Plan: &billing.SubscriptionPlan{
			ID:       id,
			Name:     "restricted plan",
			GroupIDs: append([]int64(nil), groupIDs...),
		},
	}
}

func (s *authGroupRepoStub) Create(ctx context.Context, group *routing.Group) error {
	panic("unexpected Create call")
}

func (s *authGroupRepoStub) GetByID(ctx context.Context, id int64) (*routing.Group, error) {
	panic("unexpected GetByID call")
}

func (s *authGroupRepoStub) GetByIDLite(ctx context.Context, id int64) (*routing.Group, error) {
	if s.groupsByID == nil {
		panic("unexpected GetByIDLite call")
	}
	group, ok := s.groupsByID[id]
	if !ok {
		return nil, routing.ErrGroupNotFound
	}
	return &group, nil
}

func (s *authGroupRepoStub) Update(ctx context.Context, group *routing.Group) error {
	panic("unexpected Update call")
}

func (s *authGroupRepoStub) Delete(ctx context.Context, id int64) error {
	panic("unexpected Delete call")
}

func (s *authGroupRepoStub) DeleteCascade(ctx context.Context, id int64) ([]int64, error) {
	panic("unexpected DeleteCascade call")
}

func (s *authGroupRepoStub) List(ctx context.Context, params pagination.PaginationParams) ([]routing.Group, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

func (s *authGroupRepoStub) ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, status, search string, isExclusive *bool) ([]routing.Group, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

func (s *authGroupRepoStub) ListActive(ctx context.Context) ([]routing.Group, error) {
	panic("unexpected ListActive call")
}

func (s *authGroupRepoStub) ListActiveByPlatform(ctx context.Context, platform string) ([]routing.Group, error) {
	return s.ListActiveByPlatformLite(ctx, platform)
}

func (s *authGroupRepoStub) ListActiveByPlatformLite(ctx context.Context, platform string) ([]routing.Group, error) {
	groups := s.groupsByPlatform[platform]
	out := make([]routing.Group, len(groups))
	copy(out, groups)
	return out, nil
}

func (s *authGroupRepoStub) ExistsByName(ctx context.Context, name string) (bool, error) {
	panic("unexpected ExistsByName call")
}

func (s *authGroupRepoStub) GetProviderCount(ctx context.Context, groupID int64) (int64, int64, error) {
	panic("unexpected GetProviderCount call")
}

func (s *authGroupRepoStub) DeleteProviderGroupsByGroupID(ctx context.Context, groupID int64) (int64, error) {
	panic("unexpected DeleteProviderGroupsByGroupID call")
}

func (s *authGroupRepoStub) GetProviderIDsByGroupIDs(ctx context.Context, groupIDs []int64) ([]int64, error) {
	panic("unexpected GetProviderIDsByGroupIDs call")
}

func (s *authGroupRepoStub) BindProvidersToGroup(ctx context.Context, groupID int64, providerIDs []int64) error {
	panic("unexpected BindProvidersToGroup call")
}

func (s *authGroupRepoStub) UpdateSortOrders(ctx context.Context, updates []routing.GroupSortOrderUpdate) error {
	panic("unexpected UpdateSortOrders call")
}

func (s *authUserGroupRateRepoStub) GetByUserID(ctx context.Context, userID int64) (map[int64]float64, error) {
	panic("unexpected GetByUserID call")
}

func (s *authUserGroupRateRepoStub) GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error) {
	panic("unexpected GetByUserAndGroup call")
}

func (s *authUserGroupRateRepoStub) GetRPMOverrideByUserAndGroup(ctx context.Context, userID, groupID int64) (*int, error) {
	s.calls = append(s.calls, groupID)
	return s.overrides[groupID], nil
}

func (s *authUserGroupRateRepoStub) GetByGroupID(ctx context.Context, groupID int64) ([]billing.UserGroupRateEntry, error) {
	panic("unexpected GetByGroupID call")
}

func (s *authUserGroupRateRepoStub) SyncUserGroupRates(ctx context.Context, userID int64, rates map[int64]*float64) error {
	panic("unexpected SyncUserGroupRates call")
}

func (s *authUserGroupRateRepoStub) SyncGroupRateMultipliers(ctx context.Context, groupID int64, entries []billing.GroupRateMultiplierInput) error {
	panic("unexpected SyncGroupRateMultipliers call")
}

func (s *authUserGroupRateRepoStub) SyncGroupRPMOverrides(ctx context.Context, groupID int64, entries []billing.GroupRPMOverrideInput) error {
	panic("unexpected SyncGroupRPMOverrides call")
}

func (s *authUserGroupRateRepoStub) ClearGroupRPMOverrides(ctx context.Context, groupID int64) error {
	panic("unexpected ClearGroupRPMOverrides call")
}

func (s *authUserGroupRateRepoStub) DeleteByGroupID(ctx context.Context, groupID int64) error {
	panic("unexpected DeleteByGroupID call")
}

func (s *authUserGroupRateRepoStub) DeleteByUserID(ctx context.Context, userID int64) error {
	panic("unexpected DeleteByUserID call")
}

func (s *apiKeyRepoStub) Create(ctx context.Context, key *apikey.APIKey) error {
	panic("unexpected Create call")
}

func (s *apiKeyRepoStub) GetByID(ctx context.Context, id int64) (*apikey.APIKey, error) {
	if s.getByIDErr != nil {
		return nil, s.getByIDErr
	}
	if s.apiKey != nil {
		clone := *s.apiKey
		return &clone, nil
	}
	panic("unexpected GetByID call")
}

func (s *apiKeyRepoStub) GetKeyAndOwnerID(ctx context.Context, id int64) (string, int64, error) {
	if s.getByIDErr != nil {
		return "", 0, s.getByIDErr
	}
	if s.apiKey != nil {
		return s.apiKey.Key, s.apiKey.UserID, nil
	}
	return "", 0, apikey.ErrAPIKeyNotFound
}

func (s *apiKeyRepoStub) GetByKey(ctx context.Context, key string) (*apikey.APIKey, error) {
	panic("unexpected GetByKey call")
}

func (s *apiKeyRepoStub) GetByKeyForAuth(ctx context.Context, key string) (*apikey.APIKey, error) {
	panic("unexpected GetByKeyForAuth call")
}

func (s *apiKeyRepoStub) RotateCredential(context.Context, *apikey.APIKey, string) error {
	panic("unexpected RotateCredential call")
}

func (s *apiKeyRepoStub) Update(ctx context.Context, key *apikey.APIKey, _ apikey.APIKeyUpdateFields) error {
	if key != nil {
		s.updatedKeys = append(s.updatedKeys, *key)
	}
	return s.updateErr
}

// Delete records the requested key ID and returns the configured error.
func (s *apiKeyRepoStub) Delete(ctx context.Context, id int64) error {
	s.deletedIDs = append(s.deletedIDs, id)
	return s.deleteErr
}

// DeleteWithAudit records the requested key ID for service assertions.
func (s *apiKeyRepoStub) DeleteWithAudit(ctx context.Context, id int64) error {
	s.deletedIDs = append(s.deletedIDs, id)
	return s.deleteErr
}

func (s *apiKeyRepoStub) ListByUserID(ctx context.Context, userID int64, params pagination.PaginationParams, filters apikey.APIKeyListFilters) ([]apikey.APIKey, *pagination.PaginationResult, error) {
	if !s.allowListByUserID {
		panic("unexpected ListByUserID call")
	}
	s.listByUserIDCalls = append(s.listByUserIDCalls, userID)
	s.listByUserIDParams = append(s.listByUserIDParams, params)
	s.listByUserIDFilters = append(s.listByUserIDFilters, filters)
	if s.listByUserIDErr != nil {
		return nil, nil, s.listByUserIDErr
	}
	keys := append([]apikey.APIKey(nil), s.listByUserIDKeys...)
	return keys, &pagination.PaginationResult{
		Total:    int64(len(keys)),
		Page:     params.Page,
		PageSize: params.PageSize,
		Pages:    1,
	}, nil
}

func (s *apiKeyRepoStub) VerifyOwnership(ctx context.Context, userID int64, apiKeyIDs []int64) ([]int64, error) {
	panic("unexpected VerifyOwnership call")
}

func (s *apiKeyRepoStub) CountByUserID(ctx context.Context, userID int64) (int64, error) {
	panic("unexpected CountByUserID call")
}

func (s *apiKeyRepoStub) ExistsByKey(ctx context.Context, key string) (bool, error) {
	panic("unexpected ExistsByKey call")
}

func (s *apiKeyRepoStub) ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]apikey.APIKey, *pagination.PaginationResult, error) {
	panic("unexpected ListByGroupID call")
}

func (s *apiKeyRepoStub) SearchAPIKeys(ctx context.Context, userID int64, keyword string, limit int) ([]apikey.APIKey, error) {
	panic("unexpected SearchAPIKeys call")
}

func (s *apiKeyRepoStub) ClearGroupIDByGroupID(ctx context.Context, groupID int64) (int64, error) {
	panic("unexpected ClearGroupIDByGroupID call")
}

func (s *apiKeyRepoStub) UpdateGroupIDByUserAndGroup(ctx context.Context, userID, oldGroupID, newGroupID int64) (int64, error) {
	panic("unexpected UpdateGroupIDByUserAndGroup call")
}

func (s *apiKeyRepoStub) CountByGroupID(ctx context.Context, groupID int64) (int64, error) {
	panic("unexpected CountByGroupID call")
}

func (s *apiKeyRepoStub) ListKeysByUserID(ctx context.Context, userID int64) ([]string, error) {
	panic("unexpected ListKeysByUserID call")
}

func (s *apiKeyRepoStub) ListKeysByGroupID(ctx context.Context, groupID int64) ([]string, error) {
	panic("unexpected ListKeysByGroupID call")
}

func (s *apiKeyRepoStub) IncrementQuotaUsed(ctx context.Context, id int64, amount float64) (float64, error) {
	panic("unexpected IncrementQuotaUsed call")
}

func (s *apiKeyRepoStub) UpdateLastUsed(ctx context.Context, id int64, usedAt time.Time) error {
	s.touchedIDs = append(s.touchedIDs, id)
	s.touchedUsedAts = append(s.touchedUsedAts, usedAt)
	if s.updateLastUsed != nil {
		return s.updateLastUsed(ctx, id, usedAt)
	}
	return nil
}

func (s *apiKeyRepoStub) IncrementRateLimitUsage(ctx context.Context, id int64, cost float64) error {
	panic("unexpected IncrementRateLimitUsage call")
}

func (s *apiKeyRepoStub) ResetRateLimitWindows(ctx context.Context, id int64) error {
	panic("unexpected ResetRateLimitWindows call")
}

func (s *apiKeyRepoStub) GetRateLimitData(ctx context.Context, id int64) (*apikey.APIKeyRateLimitData, error) {
	panic("unexpected GetRateLimitData call")
}

// GetCreateAttemptCount returns zero so these tests are not creation-rate-limited.
func (s *apiKeyCacheStub) GetCreateAttemptCount(ctx context.Context, userID int64) (int, error) {
	return 0, nil
}

// IncrementCreateAttemptCount is a no-op in this fixture.
func (s *apiKeyCacheStub) IncrementCreateAttemptCount(ctx context.Context, userID int64) error {
	return nil
}

// DeleteCreateAttemptCount records the user whose creation counter was cleared.
func (s *apiKeyCacheStub) DeleteCreateAttemptCount(ctx context.Context, userID int64) error {
	s.invalidated = append(s.invalidated, userID)
	return nil
}

// IncrementDailyUsage is a no-op in this fixture.
func (s *apiKeyCacheStub) IncrementDailyUsage(ctx context.Context, apiKey string) error {
	return nil
}

// SetDailyUsageExpiry is a no-op in this fixture.
func (s *apiKeyCacheStub) SetDailyUsageExpiry(ctx context.Context, apiKey string, ttl time.Duration) error {
	return nil
}

func (s *apiKeyCacheStub) GetAuthCache(ctx context.Context, key string) (*apikey.APIKeyAuthCacheEntry, error) {
	return nil, nil
}

func (s *apiKeyCacheStub) SetAuthCache(ctx context.Context, key string, entry *apikey.APIKeyAuthCacheEntry, ttl time.Duration) error {
	return nil
}

func (s *apiKeyCacheStub) DeleteAuthCache(ctx context.Context, key string) error {
	s.deleteAuthKeys = append(s.deleteAuthKeys, key)
	return nil
}

func (s *apiKeyCacheStub) PublishAuthCacheInvalidation(ctx context.Context, cacheKey string) error {
	return nil
}

func (s *apiKeyCacheStub) SubscribeAuthCacheInvalidation(ctx context.Context, handler func(cacheKey string)) error {
	return nil
}

func (s *apiKeyNameSanitizeRepoStub) Create(ctx context.Context, key *apikey.APIKey) error {
	clone := *key
	if clone.ID == 0 {
		clone.ID = int64(len(s.created) + 1)
	}
	s.created = append(s.created, &clone)
	*key = clone
	return nil
}

func (s *apiKeyNameSanitizeRepoStub) GetByID(ctx context.Context, id int64) (*apikey.APIKey, error) {
	if s.apiKey == nil || s.apiKey.ID != id {
		return nil, apikey.ErrAPIKeyNotFound
	}
	clone := *s.apiKey
	return &clone, nil
}

func (s *apiKeyNameSanitizeRepoStub) GetKeyAndOwnerID(ctx context.Context, id int64) (string, int64, error) {
	panic("unexpected GetKeyAndOwnerID call")
}

func (s *apiKeyNameSanitizeRepoStub) GetByKey(ctx context.Context, key string) (*apikey.APIKey, error) {
	panic("unexpected GetByKey call")
}

func (s *apiKeyNameSanitizeRepoStub) GetByKeyForAuth(ctx context.Context, key string) (*apikey.APIKey, error) {
	panic("unexpected GetByKeyForAuth call")
}

func (s *apiKeyNameSanitizeRepoStub) RotateCredential(context.Context, *apikey.APIKey, string) error {
	panic("unexpected RotateCredential call")
}

func (s *apiKeyNameSanitizeRepoStub) Update(ctx context.Context, key *apikey.APIKey, _ apikey.APIKeyUpdateFields) error {
	clone := *key
	s.updated = append(s.updated, &clone)
	s.apiKey = &clone
	return nil
}

func (s *apiKeyNameSanitizeRepoStub) Delete(ctx context.Context, id int64) error {
	panic("unexpected Delete call")
}

func (s *apiKeyNameSanitizeRepoStub) DeleteWithAudit(ctx context.Context, id int64) error {
	panic("unexpected DeleteWithAudit call")
}

func (s *apiKeyNameSanitizeRepoStub) ListByUserID(ctx context.Context, userID int64, params pagination.PaginationParams, filters apikey.APIKeyListFilters) ([]apikey.APIKey, *pagination.PaginationResult, error) {
	panic("unexpected ListByUserID call")
}

func (s *apiKeyNameSanitizeRepoStub) VerifyOwnership(ctx context.Context, userID int64, apiKeyIDs []int64) ([]int64, error) {
	panic("unexpected VerifyOwnership call")
}

func (s *apiKeyNameSanitizeRepoStub) CountByUserID(ctx context.Context, userID int64) (int64, error) {
	panic("unexpected CountByUserID call")
}

func (s *apiKeyNameSanitizeRepoStub) ExistsByKey(ctx context.Context, key string) (bool, error) {
	return false, nil
}

func (s *apiKeyNameSanitizeRepoStub) ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]apikey.APIKey, *pagination.PaginationResult, error) {
	panic("unexpected ListByGroupID call")
}

func (s *apiKeyNameSanitizeRepoStub) SearchAPIKeys(ctx context.Context, userID int64, keyword string, limit int) ([]apikey.APIKey, error) {
	panic("unexpected SearchAPIKeys call")
}

func (s *apiKeyNameSanitizeRepoStub) ClearGroupIDByGroupID(ctx context.Context, groupID int64) (int64, error) {
	panic("unexpected ClearGroupIDByGroupID call")
}

func (s *apiKeyNameSanitizeRepoStub) UpdateGroupIDByUserAndGroup(ctx context.Context, userID, oldGroupID, newGroupID int64) (int64, error) {
	panic("unexpected UpdateGroupIDByUserAndGroup call")
}

func (s *apiKeyNameSanitizeRepoStub) CountByGroupID(ctx context.Context, groupID int64) (int64, error) {
	panic("unexpected CountByGroupID call")
}

func (s *apiKeyNameSanitizeRepoStub) ListKeysByUserID(ctx context.Context, userID int64) ([]string, error) {
	panic("unexpected ListKeysByUserID call")
}

func (s *apiKeyNameSanitizeRepoStub) ListKeysByGroupID(ctx context.Context, groupID int64) ([]string, error) {
	panic("unexpected ListKeysByGroupID call")
}

func (s *apiKeyNameSanitizeRepoStub) IncrementQuotaUsed(ctx context.Context, id int64, amount float64) (float64, error) {
	panic("unexpected IncrementQuotaUsed call")
}

func (s *apiKeyNameSanitizeRepoStub) UpdateLastUsed(ctx context.Context, id int64, usedAt time.Time) error {
	panic("unexpected UpdateLastUsed call")
}

func (s *apiKeyNameSanitizeRepoStub) IncrementRateLimitUsage(ctx context.Context, id int64, cost float64) error {
	panic("unexpected IncrementRateLimitUsage call")
}

func (s *apiKeyNameSanitizeRepoStub) ResetRateLimitWindows(ctx context.Context, id int64) error {
	panic("unexpected ResetRateLimitWindows call")
}

func (s *apiKeyNameSanitizeRepoStub) GetRateLimitData(ctx context.Context, id int64) (*apikey.APIKeyRateLimitData, error) {
	panic("unexpected GetRateLimitData call")
}

// sanitizeFixtureGroupID selects an ordinary group for name and configuration tests.
func sanitizeFixtureGroupID() *int64 { id := int64(1); return &id }

func (s *quotaStateRepoStub) IncrementQuotaUsedAndGetState(ctx context.Context, id int64, amount float64) (*apikey.APIKeyQuotaUsageState, error) {
	s.stateCalls++
	if s.stateErr != nil {
		return nil, s.stateErr
	}
	if s.state == nil {
		return nil, nil
	}
	out := *s.state
	return &out, nil
}

func (s *quotaStateCacheStub) GetCreateAttemptCount(context.Context, int64) (int, error) {
	return 0, nil
}

func (s *quotaStateCacheStub) IncrementCreateAttemptCount(context.Context, int64) error {
	return nil
}

func (s *quotaStateCacheStub) DeleteCreateAttemptCount(context.Context, int64) error {
	return nil
}

func (s *quotaStateCacheStub) IncrementDailyUsage(context.Context, string) error {
	return nil
}

func (s *quotaStateCacheStub) SetDailyUsageExpiry(context.Context, string, time.Duration) error {
	return nil
}

func (s *quotaStateCacheStub) GetAuthCache(context.Context, string) (*apikey.APIKeyAuthCacheEntry, error) {
	return nil, nil
}

func (s *quotaStateCacheStub) SetAuthCache(context.Context, string, *apikey.APIKeyAuthCacheEntry, time.Duration) error {
	return nil
}

func (s *quotaStateCacheStub) DeleteAuthCache(_ context.Context, key string) error {
	s.deleteAuthKeys = append(s.deleteAuthKeys, key)
	return nil
}

func (s *quotaStateCacheStub) PublishAuthCacheInvalidation(context.Context, string) error {
	return nil
}

func (s *quotaStateCacheStub) SubscribeAuthCacheInvalidation(context.Context, func(string)) error {
	return nil
}

func (s *quotaBaseAPIKeyRepoStub) Create(context.Context, *apikey.APIKey) error {
	panic("unexpected Create call")
}

func (s *quotaBaseAPIKeyRepoStub) GetByID(context.Context, int64) (*apikey.APIKey, error) {
	s.getByIDCalls++
	return nil, nil
}

func (s *quotaBaseAPIKeyRepoStub) GetKeyAndOwnerID(context.Context, int64) (string, int64, error) {
	panic("unexpected GetKeyAndOwnerID call")
}

func (s *quotaBaseAPIKeyRepoStub) GetByKey(context.Context, string) (*apikey.APIKey, error) {
	panic("unexpected GetByKey call")
}

func (s *quotaBaseAPIKeyRepoStub) GetByKeyForAuth(context.Context, string) (*apikey.APIKey, error) {
	panic("unexpected GetByKeyForAuth call")
}

func (s *quotaBaseAPIKeyRepoStub) RotateCredential(context.Context, *apikey.APIKey, string) error {
	panic("unexpected RotateCredential call")
}

func (s *quotaBaseAPIKeyRepoStub) Update(context.Context, *apikey.APIKey, apikey.APIKeyUpdateFields) error {
	panic("unexpected Update call")
}

func (s *quotaBaseAPIKeyRepoStub) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}

func (s *quotaBaseAPIKeyRepoStub) DeleteWithAudit(context.Context, int64) error {
	panic("unexpected DeleteWithAudit call")
}

func (s *quotaBaseAPIKeyRepoStub) ListByUserID(context.Context, int64, pagination.PaginationParams, apikey.APIKeyListFilters) ([]apikey.APIKey, *pagination.PaginationResult, error) {
	panic("unexpected ListByUserID call")
}

func (s *quotaBaseAPIKeyRepoStub) VerifyOwnership(context.Context, int64, []int64) ([]int64, error) {
	panic("unexpected VerifyOwnership call")
}

func (s *quotaBaseAPIKeyRepoStub) CountByUserID(context.Context, int64) (int64, error) {
	panic("unexpected CountByUserID call")
}

func (s *quotaBaseAPIKeyRepoStub) ExistsByKey(context.Context, string) (bool, error) {
	panic("unexpected ExistsByKey call")
}

func (s *quotaBaseAPIKeyRepoStub) ListByGroupID(context.Context, int64, pagination.PaginationParams) ([]apikey.APIKey, *pagination.PaginationResult, error) {
	panic("unexpected ListByGroupID call")
}

func (s *quotaBaseAPIKeyRepoStub) SearchAPIKeys(context.Context, int64, string, int) ([]apikey.APIKey, error) {
	panic("unexpected SearchAPIKeys call")
}

func (s *quotaBaseAPIKeyRepoStub) ClearGroupIDByGroupID(context.Context, int64) (int64, error) {
	panic("unexpected ClearGroupIDByGroupID call")
}

func (s *quotaBaseAPIKeyRepoStub) UpdateGroupIDByUserAndGroup(context.Context, int64, int64, int64) (int64, error) {
	panic("unexpected UpdateGroupIDByUserAndGroup call")
}

func (s *quotaBaseAPIKeyRepoStub) CountByGroupID(context.Context, int64) (int64, error) {
	panic("unexpected CountByGroupID call")
}

func (s *quotaBaseAPIKeyRepoStub) ListKeysByUserID(context.Context, int64) ([]string, error) {
	panic("unexpected ListKeysByUserID call")
}

func (s *quotaBaseAPIKeyRepoStub) ListKeysByGroupID(context.Context, int64) ([]string, error) {
	panic("unexpected ListKeysByGroupID call")
}

func (s *quotaBaseAPIKeyRepoStub) IncrementQuotaUsed(context.Context, int64, float64) (float64, error) {
	panic("unexpected IncrementQuotaUsed call")
}

func (s *quotaBaseAPIKeyRepoStub) UpdateLastUsed(context.Context, int64, time.Time) error {
	panic("unexpected UpdateLastUsed call")
}

func (s *quotaBaseAPIKeyRepoStub) IncrementRateLimitUsage(context.Context, int64, float64) error {
	panic("unexpected IncrementRateLimitUsage call")
}

func (s *quotaBaseAPIKeyRepoStub) ResetRateLimitWindows(context.Context, int64) error {
	panic("unexpected ResetRateLimitWindows call")
}

func (s *quotaBaseAPIKeyRepoStub) GetRateLimitData(context.Context, int64) (*apikey.APIKeyRateLimitData, error) {
	panic("unexpected GetRateLimitData call")
}

// IncrementQuotaUsed simulates the billing increment by changing quota_used alone.
func (s *updateFieldsAPIKeyRepoStub) IncrementQuotaUsed(_ context.Context, _ int64, amount float64) (float64, error) {
	s.key.QuotaUsed += amount
	return s.key.QuotaUsed, nil
}

func (s *updateFieldsAPIKeyRepoStub) GetByID(context.Context, int64) (*apikey.APIKey, error) {
	clone := *s.key
	return &clone, nil
}

func (s *updateFieldsAPIKeyRepoStub) Update(_ context.Context, _ *apikey.APIKey, fields apikey.APIKeyUpdateFields) error {
	s.updateFields = append(s.updateFields, fields)
	return nil
}

func newUpdateFieldsAPIKeyService(key *apikey.APIKey) (*apikey.APIKeyService, *updateFieldsAPIKeyRepoStub) {
	repo := &updateFieldsAPIKeyRepoStub{key: key}
	return newAPIKeyTestService(apiKeyTestDependencies{apiKeyRepo: repo}), repo
}

func (s *userRepoStub) GetByID(context.Context, int64) (*identity.User, error) {
	if s.user == nil {
		return nil, identity.ErrUserNotFound
	}
	return s.user, nil
}

func (r *fakeTeamRepository) GetContextByUserID(context.Context, int64) (*team.TeamContext, error) {
	return r.teamContext, nil
}

func (r *fakeTeamRepository) GetContextByTeamID(context.Context, int64) (*team.TeamContext, error) {
	return r.teamContext, nil
}

func (c *stubConcurrencyCacheForTest) TrackAPIKeySlot(context.Context, int64, string) error {
	return nil
}

func (c *stubConcurrencyCacheForTest) ReleaseAPIKeySlot(context.Context, int64, string) error {
	return nil
}

func (c *stubConcurrencyCacheForTest) GetAPIKeyConcurrencyBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	result := make(map[int64]int, len(ids))
	for _, id := range ids {
		result[id] = c.apiKeyConcurrency[id]
	}
	return result, nil
}
