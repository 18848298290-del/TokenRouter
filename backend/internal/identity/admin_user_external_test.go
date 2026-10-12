package identity_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	"github.com/TokenFlux/TokenRouter/internal/scheduler"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
)

type balanceUserRepoStub struct {
	*userRepoStub
	adjustErr error
	// changes records atomic balance updates in call order.
	changes []identity.BalanceChange
}

type balanceRedeemRepoStub struct {
	billing.RedeemCodeRepository
	created []*billing.RedeemCode
}

type authCacheInvalidatorStub struct {
	userIDs  []int64
	groupIDs []int64
	keys     []string
}

type adminRechargeAffiliateAccruerStub struct {
	calls  []adminRechargeAffiliateAccrual
	rebate float64
	err    error
}

// adminRechargeAffiliateAccrual records rebate accrual arguments.
type adminRechargeAffiliateAccrual struct {
	userID int64
	amount float64
}

// rechargeSettingsFixture reads the administrator-recharge rebate flag through promotion settings.
type rechargeSettingsFixture struct {
	identity.AdminUserSettings
	runtime *promotion.RuntimeSettings
}

type batchLimitsUserRepoStub struct {
	identity.UserRepository
	calls       int
	userIDs     []int64
	concurrency *int
	rpmLimit    *int
	affected    int
	err         error
}

// batchLimitsInvalidator records authentication cache invalidations after batch updates.
type batchLimitsInvalidator struct{ userIDs []int64 }

// adminCreationSettingsStore returns ErrSettingNotFound for missing setting keys.
type adminCreationSettingsStore struct{ authSourceDefaultsRepoStub }

// adminCreationSettings combines authentication and signup-grant settings.
type adminCreationSettings struct {
	*identity.RuntimeSettings
	*identity.GrantSettings
}

type userRepoStubForListUsers struct {
	userRepoStub
	users                 []identity.User
	err                   error
	listWithFiltersParams pagination.PaginationParams
	lastUsedByUserID      map[int64]*time.Time
	lastUsedErr           error
}

type userGroupRateRepoStubForListUsers struct {
	batchCalls int
	singleCall []int64

	batchErr  error
	batchData map[int64]map[int64]float64

	singleErr  map[int64]error
	singleData map[int64]map[int64]float64
}

// roleGuardUserRepoStub adds administrator counts to test the last-administrator check.
type roleGuardUserRepoStub struct {
	*rpmUserRepoStub
	adminTotal int64
	listCalls  int
}

type rpmStatusUserRepoStub struct {
	identity.UserRepository

	user *identity.User
}

type rpmStatusAPIKeyRepoStub struct {
	identity.AdminKeyReader
	keys []identity.AdminKeySummary
}

type rpmStatusGroupRepoStub struct {
	identity.AdminGroupReader

	groups map[int64]*identity.AdminGroup
}

type rpmStatusRateRepoStub struct {
	billing.UserGroupRateRepository
	overrides map[int64]*int
}

type rpmStatusCacheStub struct {
	scheduler.UserRPMCache
	userUsed  int
	groupUsed map[int64]int
}

// rpmUserRepoStub copies Update inputs so tests can inspect RPMLimit changes.
type rpmUserRepoStub struct {
	*userRepoStub
	lastUpdated *identity.User
	lastFields  identity.UserUpdateFields
}

func (s *balanceUserRepoStub) AdjustBalance(ctx context.Context, id int64, delta float64) (identity.BalanceChange, error) {
	return s.apply(func(current float64) float64 { return current + delta })
}

func (s *balanceUserRepoStub) SetBalance(ctx context.Context, id int64, value float64) (identity.BalanceChange, error) {
	return s.apply(func(float64) float64 { return value })
}

func (s *balanceUserRepoStub) apply(next func(current float64) float64) (identity.BalanceChange, error) {
	if s.adjustErr != nil {
		return identity.BalanceChange{}, s.adjustErr
	}
	if s.userRepoStub == nil || s.user == nil {
		return identity.BalanceChange{}, identity.ErrUserNotFound
	}
	change := identity.BalanceChange{Old: s.user.Balance}
	change.New = next(change.Old)
	if change.New < 0 {
		return change, identity.ErrBalanceNegative
	}
	s.user.Balance = change.New
	s.changes = append(s.changes, change)
	return change, nil
}

func (s *balanceRedeemRepoStub) Create(ctx context.Context, code *billing.RedeemCode) error {
	if code == nil {
		return nil
	}
	clone := *code
	s.created = append(s.created, &clone)
	return nil
}

func (s *adminRechargeAffiliateAccruerStub) AccrueInviteRebate(_ context.Context, userID int64, amount float64) (float64, error) {
	s.calls = append(s.calls, adminRechargeAffiliateAccrual{userID: userID, amount: amount})
	return s.rebate, s.err
}

func adminRechargeSettingService(enabled bool) identity.AdminUserSettings {
	values := map[string]string{}
	if enabled {
		values[promotion.SettingKeyAffiliateAdminRechargeEnabled] = "true"
	}
	return rechargeSettingsFixture{runtime: promotion.NewRuntimeSettings(&adminCreationSettingsStore{authSourceDefaultsRepoStub: authSourceDefaultsRepoStub{values: values}})}
}

func (s *authCacheInvalidatorStub) InvalidateAuthCacheByKey(ctx context.Context, key string) {
	s.keys = append(s.keys, key)
}

func (s *authCacheInvalidatorStub) InvalidateAuthCacheByUserID(ctx context.Context, userID int64) {
	s.userIDs = append(s.userIDs, userID)
}

func (s *authCacheInvalidatorStub) InvalidateAuthCacheByGroupID(ctx context.Context, groupID int64) {
	s.groupIDs = append(s.groupIDs, groupID)
}

// TestAdminService_UpdateUserBalance_UsesAtomicPrimitives checks that adjustments use AdjustBalance or SetBalance.
// A read followed by a full-row write could overwrite concurrent charges; the fixture panics on Update.
func TestAdminService_UpdateUserBalance_UsesAtomicPrimitives(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		amount    float64
		want      identity.BalanceChange
	}{
		{name: "add", operation: "add", amount: 5, want: identity.BalanceChange{Old: 10, New: 15}},
		{name: "subtract", operation: "subtract", amount: 4, want: identity.BalanceChange{Old: 10, New: 6}},
		{name: "set", operation: "set", amount: 2, want: identity.BalanceChange{Old: 10, New: 2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &identity.User{ID: 7, Balance: 10}}}
			svc := newBalanceAdminForTest(repo, &balanceRedeemRepoStub{}, nil, nil, nil)

			user, err := svc.UpdateUserBalance(context.Background(), 7, tt.amount, tt.operation, "")
			require.NoError(t, err)
			require.Equal(t, []identity.BalanceChange{tt.want}, repo.changes)
			require.Equal(t, tt.want.New, user.Balance)
		})
	}
}

func TestAdminService_UpdateUserBalance_RejectsNegativeResult(t *testing.T) {
	repo := &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &identity.User{ID: 7, Balance: 3}}}
	svc := newBalanceAdminForTest(repo, &balanceRedeemRepoStub{}, nil, nil, nil)

	_, err := svc.UpdateUserBalance(context.Background(), 7, 4, "subtract", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "balance cannot be negative")
	require.Empty(t, repo.changes, "refused adjustment must not be applied")
	require.Equal(t, 3.0, repo.user.Balance)
}

func TestAdminService_UpdateUserBalance_RejectsUnknownOperation(t *testing.T) {
	repo := &balanceUserRepoStub{userRepoStub: &userRepoStub{user: &identity.User{ID: 7, Balance: 10}}}
	svc := newBalanceAdminForTest(repo, &balanceRedeemRepoStub{}, nil, nil, nil)

	_, err := svc.UpdateUserBalance(context.Background(), 7, 1, "multiply", "")
	require.Error(t, err)
	require.Empty(t, repo.changes)
}

func TestAdminService_UpdateUserBalance_InvalidatesAuthCache(t *testing.T) {
	baseRepo := &userRepoStub{user: &identity.User{ID: 7, Balance: 10}}
	repo := &balanceUserRepoStub{userRepoStub: baseRepo}
	redeemRepo := &balanceRedeemRepoStub{}
	invalidator := &authCacheInvalidatorStub{}
	svc := newBalanceAdminForTest(repo, redeemRepo, invalidator, nil, nil)

	_, err := svc.UpdateUserBalance(context.Background(), 7, 5, "add", "")
	require.NoError(t, err)
	require.Equal(t, []int64{7}, invalidator.userIDs)
	require.Len(t, redeemRepo.created, 1)
}

func TestAdminService_UpdateUserBalance_NoChangeNoInvalidate(t *testing.T) {
	baseRepo := &userRepoStub{user: &identity.User{ID: 7, Balance: 10}}
	repo := &balanceUserRepoStub{userRepoStub: baseRepo}
	redeemRepo := &balanceRedeemRepoStub{}
	invalidator := &authCacheInvalidatorStub{}
	svc := newBalanceAdminForTest(repo, redeemRepo, invalidator, nil, nil)

	_, err := svc.UpdateUserBalance(context.Background(), 7, 10, "set", "")
	require.NoError(t, err)
	require.Empty(t, invalidator.userIDs)
	require.Empty(t, redeemRepo.created)
}

func TestAdminService_UpdateUserBalance_AdminRechargeAffiliateRebate(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		operation string
		amount    float64
		wantCalls []adminRechargeAffiliateAccrual
	}{
		{
			name:      "disabled by default",
			operation: "add",
			amount:    5,
		},
		{
			name:      "enabled add",
			enabled:   true,
			operation: "add",
			amount:    0.1,
			wantCalls: []adminRechargeAffiliateAccrual{{userID: 7, amount: 0.1}},
		},
		{
			name:      "enabled set increase",
			enabled:   true,
			operation: "set",
			amount:    15,
		},
		{
			name:      "enabled subtract",
			enabled:   true,
			operation: "subtract",
			amount:    5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseRepo := &userRepoStub{user: &identity.User{ID: 7, Balance: 10}}
			repo := &balanceUserRepoStub{userRepoStub: baseRepo}
			redeemRepo := &balanceRedeemRepoStub{}
			affiliate := &adminRechargeAffiliateAccruerStub{}
			svc := newBalanceAdminForTest(repo, redeemRepo, nil, adminRechargeSettingService(tt.enabled), affiliate)

			_, err := svc.UpdateUserBalance(context.Background(), 7, tt.amount, tt.operation, "")
			require.NoError(t, err)
			require.Equal(t, tt.wantCalls, affiliate.calls)
		})
	}
}

func TestAdminService_UpdateUserBalance_AffiliateFailureDoesNotRollbackRecharge(t *testing.T) {
	baseRepo := &userRepoStub{user: &identity.User{ID: 7, Balance: 10}}
	repo := &balanceUserRepoStub{userRepoStub: baseRepo}
	redeemRepo := &balanceRedeemRepoStub{}
	affiliate := &adminRechargeAffiliateAccruerStub{err: errors.New("affiliate unavailable")}
	svc := newBalanceAdminForTest(repo, redeemRepo, nil, adminRechargeSettingService(true), affiliate)

	user, err := svc.UpdateUserBalance(context.Background(), 7, 5, "add", "")
	require.NoError(t, err)
	require.Equal(t, 15.0, user.Balance)
	require.Equal(t, []adminRechargeAffiliateAccrual{{userID: 7, amount: 5}}, affiliate.calls)
	require.Len(t, redeemRepo.created, 1)
}

// CreateUsage simulates a successful adjustment-history write.
func (*balanceRedeemRepoStub) CreateUsage(context.Context, *billing.RedeemCodeUsage) error {
	return nil
}

func (s rechargeSettingsFixture) IsAffiliateAdminRechargeEnabled(ctx context.Context) bool {
	return s.runtime.IsAffiliateAdminRechargeEnabled(ctx)
}

// newBalanceAdminForTest configures balance writes and adjustment-history transactions.
func newBalanceAdminForTest(users *balanceUserRepoStub, records *balanceRedeemRepoStub, invalidator *authCacheInvalidatorStub, settings identity.AdminUserSettings, affiliate *adminRechargeAffiliateAccruerStub) *identity.UserAdmin {
	d := identity.AdminDependencies{Users: users, Balances: users, Settings: settings, Records: billing.NewRedeemAdmin(records, billingpostgres.NewRedeemAdministrationMutations(nil), time.Now)}
	if invalidator != nil {
		d.Invalidator = invalidator
	}
	if affiliate != nil {
		d.Affiliates = affiliate
	}
	return identity.NewUserAdmin(d)
}

func (s *batchLimitsUserRepoStub) BatchUpdateLimits(_ context.Context, userIDs []int64, concurrency, rpmLimit *int) (int, error) {
	s.calls++
	s.userIDs = append([]int64(nil), userIDs...)
	s.concurrency = cloneBatchLimitValue(concurrency)
	s.rpmLimit = cloneBatchLimitValue(rpmLimit)
	return s.affected, s.err
}

func cloneBatchLimitValue(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func TestAdminServiceBatchUpdateLimitsPassesOnlyProvidedFields(t *testing.T) {
	concurrency := 0
	repo := &batchLimitsUserRepoStub{
		affected: 2,
	}
	invalidator := &batchLimitsInvalidator{}
	service := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Invalidator: invalidator})

	affected, err := service.BatchUpdateLimits(
		context.Background(),
		[]int64{3, 0, 3, 7, -1},
		&concurrency,
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, 2, affected)
	require.Equal(t, []int64{3, 7}, repo.userIDs)
	require.Equal(t, pointerToInt(0), repo.concurrency)
	require.Nil(t, repo.rpmLimit)
	require.Equal(t, []int64{3, 7}, invalidator.userIDs)
}

func TestAdminServiceBatchUpdateLimitsDoesNotInvalidateCacheOnRepositoryError(t *testing.T) {
	rpmLimit := 60
	repo := &batchLimitsUserRepoStub{
		err: errors.New("database unavailable"),
	}
	invalidator := &batchLimitsInvalidator{}
	service := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Invalidator: invalidator})

	affected, err := service.BatchUpdateLimits(context.Background(), []int64{1, 2}, nil, &rpmLimit)

	require.EqualError(t, err, "database unavailable")
	require.Zero(t, affected)
	require.Empty(t, invalidator.userIDs)
}

func TestAdminServiceBatchUpdateLimitsRequiresAField(t *testing.T) {
	repo := &batchLimitsUserRepoStub{}
	service := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Invalidator: &batchLimitsInvalidator{}})

	affected, err := service.BatchUpdateLimits(context.Background(), []int64{1}, nil, nil)

	require.Error(t, err)
	require.Zero(t, affected)
	require.Zero(t, repo.calls)
}

func pointerToInt(value int) *int {
	return &value
}

func (s *batchLimitsInvalidator) InvalidateAuthCacheByUserID(_ context.Context, id int64) {
	s.userIDs = append(s.userIDs, id)
}

func (s *batchLimitsInvalidator) InvalidateAuthCacheByKey(context.Context, string) {
	panic("unexpected key invalidation")
}

func TestAdminService_CreateUser_Success(t *testing.T) {
	repo := &userRepoStub{nextID: 10}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})
	balance := 12.5

	input := &identity.CreateUserInput{
		Email:                "user@test.com",
		Password:             "strong-pass",
		Username:             "tester",
		Notes:                "note",
		Balance:              &balance,
		Concurrency:          7,
		AllowedGroups:        []int64{3, 5},
		DisabledPublicGroups: []int64{8},
	}

	user, err := svc.CreateUser(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, int64(10), user.ID)
	require.Equal(t, input.Email, user.Email)
	require.Equal(t, input.Username, user.Username)
	require.Equal(t, input.Notes, user.Notes)
	require.Equal(t, balance, user.Balance)
	require.Equal(t, input.Concurrency, user.Concurrency)
	require.Equal(t, identity.DefaultUserAPIKeyLimit, user.APIKeyLimit)
	require.Equal(t, input.AllowedGroups, user.AllowedGroups)
	require.Equal(t, input.DisabledPublicGroups, user.DisabledPublicGroups)
	require.Equal(t, identity.RoleUser, user.Role)
	require.Equal(t, billing.StatusActive, user.Status)
	require.True(t, user.CheckPassword(input.Password))
	require.Len(t, repo.created, 1)
	require.Equal(t, user, repo.created[0])
}

func TestAdminService_CreateUser_APIKeyLimitDefaultsAndExplicitZero(t *testing.T) {
	repo := &userRepoStub{nextID: 13}
	settingService := newAdminCreationSettings(&adminCreationSettingsStore{authSourceDefaultsRepoStub: authSourceDefaultsRepoStub{values: map[string]string{
		identity.SettingKeyDefaultUserAPIKeyLimit: "45",
	}}}, identity.GrantSettingsOptions{})
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Settings: settingService})

	inherited, err := svc.CreateUser(context.Background(), &identity.CreateUserInput{
		Email:    "inherited-limit@test.com",
		Password: "strong-pass",
	})
	require.NoError(t, err)
	require.Equal(t, 45, inherited.APIKeyLimit)

	unlimited := 0
	explicit, err := svc.CreateUser(context.Background(), &identity.CreateUserInput{
		Email:       "unlimited-limit@test.com",
		Password:    "strong-pass",
		APIKeyLimit: &unlimited,
	})
	require.NoError(t, err)
	require.Equal(t, 0, explicit.APIKeyLimit)
}

func TestAdminService_CreateUser_RejectsNegativeAPIKeyLimit(t *testing.T) {
	repo := &userRepoStub{}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})
	negative := -1

	_, err := svc.CreateUser(context.Background(), &identity.CreateUserInput{
		Email:       "negative-limit@test.com",
		Password:    "strong-pass",
		APIKeyLimit: &negative,
	})

	require.ErrorIs(t, err, identity.ErrUserAPIKeyLimitInvalid)
	require.Empty(t, repo.created)
}

func TestAdminService_CreateUser_RejectsAPIKeyLimitAboveDatabaseRange(t *testing.T) {
	repo := &userRepoStub{}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})
	tooHigh := identity.MaxUserAPIKeyLimit + 1

	_, err := svc.CreateUser(context.Background(), &identity.CreateUserInput{
		Email:       "too-high-limit@test.com",
		Password:    "strong-pass",
		APIKeyLimit: &tooHigh,
	})

	require.ErrorIs(t, err, identity.ErrUserAPIKeyLimitInvalid)
	require.Empty(t, repo.created)
}

func TestAdminService_CreateUser_UsesDefaultBalanceWhenBalanceOmitted(t *testing.T) {
	repo := &userRepoStub{nextID: 11}
	defaults := identity.GrantSettingsOptions{DefaultBalance: 0}
	settingService := newAdminCreationSettings(&adminCreationSettingsStore{authSourceDefaultsRepoStub: authSourceDefaultsRepoStub{values: map[string]string{
		billing.SettingKeyDefaultBalance: "0.02",
	}}}, defaults)
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Settings: settingService})

	user, err := svc.CreateUser(context.Background(), &identity.CreateUserInput{
		Email:    "default-balance@test.com",
		Password: "strong-pass",
	})

	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, 0.02, user.Balance)
	require.Len(t, repo.created, 1)
	require.Equal(t, 0.02, repo.created[0].Balance)
}

func TestAdminService_CreateUser_ExplicitZeroBalanceOverridesDefault(t *testing.T) {
	repo := &userRepoStub{nextID: 12}
	defaults := identity.GrantSettingsOptions{DefaultBalance: 0}
	settingService := newAdminCreationSettings(&adminCreationSettingsStore{authSourceDefaultsRepoStub: authSourceDefaultsRepoStub{values: map[string]string{
		billing.SettingKeyDefaultBalance: "0.02",
	}}}, defaults)
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Settings: settingService})
	balance := 0.0

	user, err := svc.CreateUser(context.Background(), &identity.CreateUserInput{
		Email:    "zero-balance@test.com",
		Password: "strong-pass",
		Balance:  &balance,
	})

	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, 0.0, user.Balance)
	require.Len(t, repo.created, 1)
	require.Equal(t, 0.0, repo.created[0].Balance)
}

func TestAdminService_CreateUser_EmailExists(t *testing.T) {
	repo := &userRepoStub{createErr: identity.ErrEmailExists}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})

	_, err := svc.CreateUser(context.Background(), &identity.CreateUserInput{
		Email:    "dup@test.com",
		Password: "password",
	})
	require.ErrorIs(t, err, identity.ErrEmailExists)
	require.Empty(t, repo.created)
}

func TestAdminService_CreateUser_CreateError(t *testing.T) {
	createErr := errors.New("db down")
	repo := &userRepoStub{createErr: createErr}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})

	_, err := svc.CreateUser(context.Background(), &identity.CreateUserInput{
		Email:    "user@test.com",
		Password: "password",
	})
	require.ErrorIs(t, err, createErr)
	require.Empty(t, repo.created)
}

func TestAdminService_CreateUser_AssignsDefaultSubscriptions(t *testing.T) {
	repo := &userRepoStub{nextID: 21}
	assigner := &defaultSubscriptionAssignerStub{}
	defaults := identity.GrantSettingsOptions{DefaultBalance: 0, DefaultConcurrency: 1}
	settingService := newAdminCreationSettings(&adminCreationSettingsStore{authSourceDefaultsRepoStub: authSourceDefaultsRepoStub{values: map[string]string{
		billing.SettingKeyDefaultSubscriptions: `[{"plan_id":5}]`,
	}}}, defaults)
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Settings: settingService, Subscriptions: assigner})

	_, err := svc.CreateUser(context.Background(), &identity.CreateUserInput{
		Email:    "new-user@test.com",
		Password: "password",
	})
	require.NoError(t, err)
	require.Len(t, assigner.calls, 1)
	require.Equal(t, int64(21), assigner.calls[0].UserID)
	require.Equal(t, int64(5), assigner.calls[0].PlanID)
}

func (s *adminCreationSettingsStore) GetValue(_ context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", settingscore.ErrSettingNotFound
	}
	return value, nil
}

func newAdminCreationSettings(repo *adminCreationSettingsStore, opts identity.GrantSettingsOptions) *adminCreationSettings {
	return &adminCreationSettings{identity.NewRuntimeSettings(repo, settingscore.ErrSettingNotFound), identity.NewGrantSettings(repo, opts)}
}

func (*adminCreationSettings) IsAffiliateAdminRechargeEnabled(context.Context) bool {
	panic("unexpected recharge policy read")
}

func TestAdminService_CreateUser_DoesNotReturnPartialSuccessFromEmailIdentityResync(t *testing.T) {
	repo := &emailSyncRepoStub{
		nextID:    55,
		ensureErr: fmt.Errorf("unexpected email resync"),
	}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})

	user, err := svc.CreateUser(context.Background(), &identity.CreateUserInput{
		Email:    "admin-created@example.com",
		Password: "strong-pass",
	})
	require.NoError(t, err)
	require.NotNil(t, user)
	require.Equal(t, int64(55), user.ID)
	require.Empty(t, repo.ensureCalls)
	require.Empty(t, repo.replaceCalls)
}

func TestAdminService_UpdateUser_DoesNotReturnPartialSuccessFromEmailIdentityResync(t *testing.T) {
	repo := &emailSyncRepoStub{
		user: &identity.User{
			ID:          91,
			Email:       "before@example.com",
			Role:        identity.RoleUser,
			Status:      billing.StatusActive,
			Concurrency: 3,
		},
		replaceErr: fmt.Errorf("unexpected email resync"),
	}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})

	updated, err := svc.UpdateUser(context.Background(), 91, &identity.UpdateUserInput{
		Email: "after@example.com",
	})
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, "after@example.com", updated.Email)
	require.Empty(t, repo.replaceCalls)
	require.Empty(t, repo.ensureCalls)
}

func TestAdminService_GetUserIncludeDeleted(t *testing.T) {
	ts := time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC)
	repo := &userRepoStub{user: &identity.User{ID: 7, Email: "del@test.com", DeletedAt: &ts}}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})

	got, err := svc.GetUserIncludeDeleted(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(7), got.ID)
	require.NotNil(t, got.DeletedAt)
}

func (s *userRepoStubForListUsers) ListWithFilters(_ context.Context, params pagination.PaginationParams, _ identity.UserListFilters) ([]identity.User, *pagination.PaginationResult, error) {
	s.listWithFiltersParams = params
	if s.err != nil {
		return nil, nil, s.err
	}
	out := make([]identity.User, len(s.users))
	copy(out, s.users)
	return out, &pagination.PaginationResult{
		Total:    int64(len(out)),
		Page:     params.Page,
		PageSize: params.PageSize,
	}, nil
}

func (s *userRepoStubForListUsers) GetLatestUsedAtByUserIDs(_ context.Context, userIDs []int64) (map[int64]*time.Time, error) {
	if s.lastUsedErr != nil {
		return nil, s.lastUsedErr
	}
	result := make(map[int64]*time.Time, len(userIDs))
	for _, userID := range userIDs {
		if ts, ok := s.lastUsedByUserID[userID]; ok {
			result[userID] = ts
		}
	}
	return result, nil
}

func (s *userRepoStubForListUsers) GetLatestUsedAtByUserID(_ context.Context, userID int64) (*time.Time, error) {
	if s.lastUsedErr != nil {
		return nil, s.lastUsedErr
	}
	return s.lastUsedByUserID[userID], nil
}

func (s *userGroupRateRepoStubForListUsers) GetByUserIDs(_ context.Context, _ []int64) (map[int64]map[int64]float64, error) {
	s.batchCalls++
	if s.batchErr != nil {
		return nil, s.batchErr
	}
	return s.batchData, nil
}

func (s *userGroupRateRepoStubForListUsers) GetByUserID(_ context.Context, userID int64) (map[int64]float64, error) {
	s.singleCall = append(s.singleCall, userID)
	if err, ok := s.singleErr[userID]; ok {
		return nil, err
	}
	if rates, ok := s.singleData[userID]; ok {
		return rates, nil
	}
	return map[int64]float64{}, nil
}

func (s *userGroupRateRepoStubForListUsers) GetByUserAndGroup(_ context.Context, userID, groupID int64) (*float64, error) {
	panic("unexpected GetByUserAndGroup call")
}

func (s *userGroupRateRepoStubForListUsers) GetRPMOverrideByUserAndGroup(_ context.Context, _, _ int64) (*int, error) {
	panic("unexpected GetRPMOverrideByUserAndGroup call")
}

func (s *userGroupRateRepoStubForListUsers) SyncUserGroupRates(_ context.Context, userID int64, rates map[int64]*float64) error {
	panic("unexpected SyncUserGroupRates call")
}

func (s *userGroupRateRepoStubForListUsers) GetByGroupID(_ context.Context, _ int64) ([]billing.UserGroupRateEntry, error) {
	panic("unexpected GetByGroupID call")
}

func (s *userGroupRateRepoStubForListUsers) SyncGroupRateMultipliers(_ context.Context, _ int64, _ []billing.GroupRateMultiplierInput) error {
	panic("unexpected SyncGroupRateMultipliers call")
}

func (s *userGroupRateRepoStubForListUsers) SyncGroupRPMOverrides(_ context.Context, _ int64, _ []billing.GroupRPMOverrideInput) error {
	panic("unexpected SyncGroupRPMOverrides call")
}

func (s *userGroupRateRepoStubForListUsers) ClearGroupRPMOverrides(_ context.Context, _ int64) error {
	panic("unexpected ClearGroupRPMOverrides call")
}

func (s *userGroupRateRepoStubForListUsers) DeleteByGroupID(_ context.Context, _ int64) error {
	panic("unexpected DeleteByGroupID call")
}

func (s *userGroupRateRepoStubForListUsers) DeleteByUserID(_ context.Context, userID int64) error {
	panic("unexpected DeleteByUserID call")
}

func TestAdminService_ListUsers_BatchRateFallbackToSingle(t *testing.T) {
	userRepo := &userRepoStubForListUsers{
		users: []identity.User{
			{ID: 101, Username: "u1"},
			{ID: 202, Username: "u2"},
		},
	}
	rateRepo := &userGroupRateRepoStubForListUsers{
		batchErr: errors.New("batch unavailable"),
		singleData: map[int64]map[int64]float64{
			101: {11: 1.1},
			202: {22: 2.2},
		},
	}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: userRepo, Rates: rateRepo})

	users, total, err := svc.ListUsers(context.Background(), 1, 20, identity.UserListFilters{}, "", "")
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, users, 2)
	require.Equal(t, 1, rateRepo.batchCalls)
	require.ElementsMatch(t, []int64{101, 202}, rateRepo.singleCall)
	require.Equal(t, 1.1, users[0].GroupRates[11])
	require.Equal(t, 2.2, users[1].GroupRates[22])
}

func TestAdminService_ListUsers_PassesSortParams(t *testing.T) {
	userRepo := &userRepoStubForListUsers{
		users: []identity.User{{ID: 1, Email: "a@example.com"}},
	}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: userRepo})

	_, _, err := svc.ListUsers(context.Background(), 2, 50, identity.UserListFilters{}, "email", "ASC")
	require.NoError(t, err)
	require.Equal(t, pagination.PaginationParams{
		Page:      2,
		PageSize:  50,
		SortBy:    "email",
		SortOrder: "ASC",
	}, userRepo.listWithFiltersParams)
}

func TestAdminService_ListUsers_PopulatesLastUsedAt(t *testing.T) {
	lastUsed := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	userRepo := &userRepoStubForListUsers{
		users: []identity.User{{ID: 101, Email: "u@example.com"}},
		lastUsedByUserID: map[int64]*time.Time{
			101: &lastUsed,
		},
	}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: userRepo})

	users, total, err := svc.ListUsers(context.Background(), 1, 20, identity.UserListFilters{}, "", "")
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, users, 1)
	require.NotNil(t, users[0].LastUsedAt)
	require.WithinDuration(t, lastUsed, *users[0].LastUsedAt, time.Second)
}

func TestAdminService_UpdateUser_UsesNormalizedEmailGuardWhenEnabled(t *testing.T) {
	repo := &emailNormalizationRepoStub{
		user: &identity.User{
			ID:       11,
			Email:    "old@example.com",
			Role:     identity.RoleUser,
			Status:   billing.StatusActive,
			Username: "tester",
		},
	}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Settings: newAdminCreationSettings(&adminCreationSettingsStore{authSourceDefaultsRepoStub: authSourceDefaultsRepoStub{values: map[string]string{identity.SettingKeyRegistrationEmailNormalization: "true"}}}, identity.GrantSettingsOptions{})})

	updated, err := svc.UpdateUser(context.Background(), 11, &identity.UpdateUserInput{
		Email: "Y.o.u.r.N.a.m.e+alias@googlemail.com.",
	})
	require.NoError(t, err)
	require.Equal(t, "Y.o.u.r.N.a.m.e+alias@googlemail.com.", updated.Email)
	require.Empty(t, repo.existsByEmailCalls)
	require.Equal(t, []string{"yourname@gmail.com"}, repo.normalizedUpdateCalls)
	require.Len(t, repo.normalizedUpdateUsers, 1)
	require.Equal(t, "Y.o.u.r.N.a.m.e+alias@googlemail.com.", repo.normalizedUpdateUsers[0].Email)
}

func TestAdminService_CreateUser_WithAdminRole(t *testing.T) {
	repo := &userRepoStub{nextID: 30}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})

	user, err := svc.CreateUser(context.Background(), &identity.CreateUserInput{
		Email:    "admin@test.com",
		Password: "strong-pass",
		Role:     identity.RoleAdmin,
	})
	require.NoError(t, err)
	require.Equal(t, identity.RoleAdmin, user.Role)
}

func TestAdminService_CreateUser_DefaultsToUserRole(t *testing.T) {
	repo := &userRepoStub{nextID: 31}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})

	user, err := svc.CreateUser(context.Background(), &identity.CreateUserInput{
		Email:    "plain@test.com",
		Password: "strong-pass",
	})
	require.NoError(t, err)
	require.Equal(t, identity.RoleUser, user.Role)
}

func TestAdminService_CreateUser_InvalidRoleRejected(t *testing.T) {
	repo := &userRepoStub{nextID: 32}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})

	_, err := svc.CreateUser(context.Background(), &identity.CreateUserInput{
		Email:    "bad@test.com",
		Password: "strong-pass",
		Role:     "superuser",
	})
	require.Error(t, err)
	require.Empty(t, repo.created, "非法角色不应写入用户")
}

func TestAdminService_UpdateUser_PromoteToAdmin(t *testing.T) {
	base := &userRepoStub{user: &identity.User{ID: 42, Email: "u@example.com", Role: identity.RoleUser}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	invalidator := &batchLimitsInvalidator{}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Invalidator: invalidator})

	updated, err := svc.UpdateUser(context.Background(), 42, &identity.UpdateUserInput{Role: identity.RoleAdmin})
	require.NoError(t, err)
	require.Equal(t, identity.RoleAdmin, updated.Role)
	require.Equal(t, []int64{42}, invalidator.userIDs, "角色变更应失效认证缓存")
}

func TestAdminService_UpdateUser_RoleOmittedKeepsExisting(t *testing.T) {
	base := &userRepoStub{user: &identity.User{ID: 42, Email: "u@example.com", Role: identity.RoleAdmin}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})

	newName := "renamed"
	updated, err := svc.UpdateUser(context.Background(), 42, &identity.UpdateUserInput{Username: &newName})
	require.NoError(t, err)
	require.Equal(t, identity.RoleAdmin, updated.Role, "未提供 role 时不应改变现有角色")
}

func TestAdminService_UpdateUser_InvalidRoleRejected(t *testing.T) {
	base := &userRepoStub{user: &identity.User{ID: 42, Email: "u@example.com", Role: identity.RoleUser}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})

	_, err := svc.UpdateUser(context.Background(), 42, &identity.UpdateUserInput{Role: "root"})
	require.Error(t, err)
	require.Nil(t, repo.lastUpdated, "非法角色不应触发持久化")
}

func (s *roleGuardUserRepoStub) ListWithFilters(_ context.Context, _ pagination.PaginationParams, _ identity.UserListFilters) ([]identity.User, *pagination.PaginationResult, error) {
	s.listCalls++
	return nil, &pagination.PaginationResult{Total: s.adminTotal}, nil
}

func TestAdminService_UpdateUser_DemoteLastAdminRejected(t *testing.T) {
	base := &userRepoStub{user: &identity.User{ID: 42, Email: "a@example.com", Role: identity.RoleAdmin}}
	repo := &roleGuardUserRepoStub{rpmUserRepoStub: &rpmUserRepoStub{userRepoStub: base}, adminTotal: 1}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})

	_, err := svc.UpdateUser(context.Background(), 42, &identity.UpdateUserInput{Role: identity.RoleUser})
	require.Error(t, err)
	require.Contains(t, err.Error(), "last admin")
	require.Nil(t, repo.lastUpdated, "最后一个管理员不应被降级持久化")
	require.Equal(t, 1, repo.listCalls, "降级路径应触发管理员计数")
}

func TestAdminService_UpdateUser_DemoteAdminAllowedWhenOthersExist(t *testing.T) {
	base := &userRepoStub{user: &identity.User{ID: 42, Email: "a@example.com", Role: identity.RoleAdmin}}
	repo := &roleGuardUserRepoStub{rpmUserRepoStub: &rpmUserRepoStub{userRepoStub: base}, adminTotal: 2}
	invalidator := &batchLimitsInvalidator{}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Invalidator: invalidator})

	updated, err := svc.UpdateUser(context.Background(), 42, &identity.UpdateUserInput{Role: identity.RoleUser})
	require.NoError(t, err)
	require.Equal(t, identity.RoleUser, updated.Role)
	require.NotNil(t, repo.lastUpdated)
	require.Equal(t, identity.RoleUser, repo.lastUpdated.Role, "存在其他管理员时允许降级")
}

func TestAdminService_UpdateUser_PromoteDoesNotCountAdmins(t *testing.T) {
	base := &userRepoStub{user: &identity.User{ID: 42, Email: "u@example.com", Role: identity.RoleUser}}
	repo := &roleGuardUserRepoStub{rpmUserRepoStub: &rpmUserRepoStub{userRepoStub: base}, adminTotal: 1}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Invalidator: &batchLimitsInvalidator{}})

	updated, err := svc.UpdateUser(context.Background(), 42, &identity.UpdateUserInput{Role: identity.RoleAdmin})
	require.NoError(t, err)
	require.Equal(t, identity.RoleAdmin, updated.Role)
	require.Equal(t, 0, repo.listCalls, "升级路径不应触发管理员计数")
}

func (s *rpmStatusUserRepoStub) GetByID(_ context.Context, _ int64) (*identity.User, error) {
	return s.user, nil
}

func (s *rpmStatusAPIKeyRepoStub) List(_ context.Context, _ int64, _, _ int, _, _ string) ([]identity.AdminKeySummary, int64, error) {
	return s.keys, int64(len(s.keys)), nil
}

func (s *rpmStatusGroupRepoStub) GetByIDLite(_ context.Context, id int64) (*identity.AdminGroup, error) {
	return s.groups[id], nil
}

func (s *rpmStatusRateRepoStub) GetRPMOverrideByUserAndGroup(_ context.Context, _, groupID int64) (*int, error) {
	return s.overrides[groupID], nil
}

func (s *rpmStatusCacheStub) IncrementUserGroupRPM(context.Context, int64, int64) (int, error) {
	return 0, nil
}

func (s *rpmStatusCacheStub) IncrementUserRPM(context.Context, int64) (int, error) {
	return 0, nil
}

func (s *rpmStatusCacheStub) GetUserGroupRPM(_ context.Context, _, groupID int64) (int, error) {
	return s.groupUsed[groupID], nil
}

func (s *rpmStatusCacheStub) GetUserRPM(context.Context, int64) (int, error) {
	return s.userUsed, nil
}

func TestAdminService_GetUserRPMStatus_AggregatesUserAndGroupLimits(t *testing.T) {
	groupOneID := int64(1)
	groupTwoID := int64(2)
	override := 7
	svc := identity.NewUserAdmin(identity.AdminDependencies{
		Users: &rpmStatusUserRepoStub{user: &identity.User{
			ID:       42,
			RPMLimit: 20,
		}},
		Keys: &rpmStatusAPIKeyRepoStub{keys: []identity.AdminKeySummary{
			{ID: 100, GroupID: &groupTwoID},
			{ID: 101, GroupID: &groupOneID},
			{ID: 102, GroupID: &groupTwoID},
			{ID: 103},
		}},
		Groups: &rpmStatusGroupRepoStub{groups: map[int64]*identity.AdminGroup{
			groupOneID: {ID: groupOneID, Name: "group-one", RPMLimit: 10},
			groupTwoID: {ID: groupTwoID, Name: "group-two", RPMLimit: 60},
		}},
		Rates: &rpmStatusRateRepoStub{overrides: map[int64]*int{
			groupTwoID: &override,
		}},
		RPM: &rpmStatusCacheStub{
			userUsed: 5,
			groupUsed: map[int64]int{
				groupOneID: 3,
				groupTwoID: 4,
			},
		},
	})

	status, err := svc.GetUserRPMStatus(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, &identity.UserRPMStatus{
		UserRPMUsed:  5,
		UserRPMLimit: 20,
		PerGroup: []identity.UserGroupRPMStatus{
			{GroupID: groupOneID, GroupName: "group-one", Used: 3, Limit: 10, Source: "group"},
			{GroupID: groupTwoID, GroupName: "group-two", Used: 4, Limit: 7, Source: "override"},
		},
	}, status)
}

func (s *rpmUserRepoStub) Update(_ context.Context, user *identity.User, fields identity.UserUpdateFields) error {
	if user == nil {
		return nil
	}
	clone := *user
	s.lastUpdated = &clone
	s.lastFields = fields
	if s.userRepoStub != nil {
		s.user = &clone
	}
	return nil
}

func TestAdminService_UpdateUser_InvalidatesAuthCacheOnRPMLimitChange(t *testing.T) {
	base := &userRepoStub{user: &identity.User{ID: 42, Email: "u@example.com", RPMLimit: 10}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	invalidator := &batchLimitsInvalidator{}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Invalidator: invalidator})

	newRPM := 60
	updated, err := svc.UpdateUser(context.Background(), 42, &identity.UpdateUserInput{
		RPMLimit: &newRPM,
	})
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 60, updated.RPMLimit)
	require.Equal(t, identity.UserUpdateFields{RPMLimit: true}, repo.lastFields)
	require.Equal(t, []int64{42}, invalidator.userIDs, "仅修改 RPMLimit 也应失效 API Key 认证缓存")
}

func TestAdminService_UpdateUser_NoInvalidateWhenRPMLimitUnchanged(t *testing.T) {
	base := &userRepoStub{user: &identity.User{ID: 42, Email: "u@example.com", RPMLimit: 10, Username: "old"}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	invalidator := &batchLimitsInvalidator{}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Invalidator: invalidator})

	newName := "new"
	sameRPM := 10
	_, err := svc.UpdateUser(context.Background(), 42, &identity.UpdateUserInput{
		Username: &newName,
		RPMLimit: &sameRPM,
	})
	require.NoError(t, err)
	require.Empty(t, invalidator.userIDs, "只改 username 不应触发认证缓存失效")
}

func TestAdminService_UpdateUser_InvalidatesAuthCacheOnDisabledPublicGroupsChange(t *testing.T) {
	base := &userRepoStub{user: &identity.User{ID: 42, Email: "u@example.com", DisabledPublicGroups: []int64{1}}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	invalidator := &batchLimitsInvalidator{}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo, Invalidator: invalidator})

	disabled := []int64{1, 3}
	updated, err := svc.UpdateUser(context.Background(), 42, &identity.UpdateUserInput{
		DisabledPublicGroups: &disabled,
	})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 3}, updated.DisabledPublicGroups)
	require.Equal(t, identity.UserUpdateFields{DisabledPublicGroups: true}, repo.lastFields)
	require.Equal(t, []int64{42}, invalidator.userIDs)
}

func TestAdminService_UpdateUser_SavesExplicitZeroAPIKeyLimit(t *testing.T) {
	base := &userRepoStub{user: &identity.User{ID: 42, Email: "u@example.com", APIKeyLimit: 100}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})
	limit := 0

	updated, err := svc.UpdateUser(context.Background(), 42, &identity.UpdateUserInput{APIKeyLimit: &limit})

	require.NoError(t, err)
	require.Equal(t, 0, updated.APIKeyLimit)
	require.Equal(t, 0, repo.lastUpdated.APIKeyLimit)
	require.Equal(t, identity.UserUpdateFields{APIKeyLimit: true}, repo.lastFields)
}

func TestAdminService_UpdateUser_RejectsNegativeAPIKeyLimit(t *testing.T) {
	base := &userRepoStub{user: &identity.User{ID: 42, Email: "u@example.com", APIKeyLimit: 100}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})
	limit := -1

	_, err := svc.UpdateUser(context.Background(), 42, &identity.UpdateUserInput{APIKeyLimit: &limit})

	require.ErrorIs(t, err, identity.ErrUserAPIKeyLimitInvalid)
	require.Nil(t, repo.lastUpdated)
	require.Equal(t, 100, base.user.APIKeyLimit)
}

func TestAdminService_UpdateUser_RejectsAPIKeyLimitAboveDatabaseRange(t *testing.T) {
	base := &userRepoStub{user: &identity.User{ID: 42, Email: "u@example.com", APIKeyLimit: 100}}
	repo := &rpmUserRepoStub{userRepoStub: base}
	svc := identity.NewUserAdmin(identity.AdminDependencies{Users: repo})
	limit := identity.MaxUserAPIKeyLimit + 1

	_, err := svc.UpdateUser(context.Background(), 42, &identity.UpdateUserInput{APIKeyLimit: &limit})

	require.ErrorIs(t, err, identity.ErrUserAPIKeyLimitInvalid)
	require.Nil(t, repo.lastUpdated)
	require.Equal(t, 100, base.user.APIKeyLimit)
}
