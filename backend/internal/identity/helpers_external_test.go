package identity_test

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	identityprovider "github.com/TokenFlux/TokenRouter/internal/identity/provider"
	identitytestkit "github.com/TokenFlux/TokenRouter/internal/identity/testkit"
	"github.com/TokenFlux/TokenRouter/internal/notification"
	"github.com/TokenFlux/TokenRouter/internal/notification/smtp"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	settingscore "github.com/TokenFlux/TokenRouter/internal/settings"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// defaultSubscriptionAssignerStub records signup-subscription calls and returns a configured error.
type defaultSubscriptionAssignerStub struct {
	calls []billing.AssignSubscriptionInput
	err   error
}

type ensureEmailCall struct {
	userID int64
	email  string
}

type replaceEmailCall struct {
	userID   int64
	oldEmail string
	newEmail string
}

type emailSyncRepoStub struct {
	user         *identity.User
	nextID       int64
	updateCalls  int
	created      []*identity.User
	updated      []*identity.User
	ensureCalls  []ensureEmailCall
	replaceCalls []replaceEmailCall
	ensureErr    error
	replaceErr   error
}

type emailNormalizationRepoStub struct {
	user *identity.User

	existsByEmail           bool
	existsByEmailErr        error
	existsByNormalized      bool
	existsByNormalizedErr   error
	createErr               error
	getByIDErr              error
	updateErr               error
	normalizedUpdateErr     error
	existsByEmailCalls      []string
	existsByNormalizedCalls []string
	createCalls             []*identity.User
	updateCalls             []*identity.User
	normalizedUpdateCalls   []string
	normalizedUpdateUsers   []*identity.User
}

type userRepoStub struct {
	user             *identity.User
	getErr           error
	createErr        error
	deleteErr        error
	exists           bool
	existsErr        error
	nextID           int64
	created          []*identity.User
	updated          []*identity.User
	deletedIDs       []int64
	usersByEmail     map[string]*identity.User
	getByEmailErr    error
	domainCounts     map[string]int
	domainCountErr   error
	domainGuardCalls []string
}

type settingRepoStub struct {
	mu               sync.Mutex
	values           map[string]string
	err              error
	getValueCalls    int
	getMultipleCalls int
}

type emailCacheStub struct {
	data *identity.VerificationCodeData
	err  error
}

type refreshTokenCacheStub struct{}

type turnstileVerifierSpy struct {
	called    int
	lastToken string
	result    *identity.TurnstileVerifyResponse
	err       error
}

// authSettingsFixture shares one settings stub across its settings readers.
type authSettingsFixture struct {
	*identity.RuntimeSettings
	*identity.GrantSettings
	*identity.OAuthSettings
	*site.DisplaySettings
	promotion *promotion.RuntimeSettings
}

// captchaSettingsStore counts reads and can simulate failures.
type captchaSettingsStore struct {
	identity.RuntimeSettingsStore
	mu               sync.Mutex
	values           map[string]string
	err              error
	getValueCalls    int
	getMultipleCalls int
}

// captchaAuthSettings supplies runtime settings for CAPTCHA tests.
type captchaAuthSettings struct {
	identity.AuthSettings
	runtime *identity.RuntimeSettings
}

type authSourceDefaultsRepoStub struct {
	values  map[string]string
	updates map[string]string
}

type tencentCaptchaVerifierStub struct {
	response    *identity.TencentCaptchaVerifyResponse
	err         error
	calls       int
	proof       identity.TencentCaptchaProof
	remoteIP    string
	credentials identity.TencentCaptchaCredentials
}

func (s *defaultSubscriptionAssignerStub) AssignOrExtendSubscription(_ context.Context, input *billing.AssignSubscriptionInput) (*billing.UserSubscription, bool, error) {
	if input != nil {
		s.calls = append(s.calls, *input)
	}
	if s.err != nil {
		return nil, false, s.err
	}
	return &billing.UserSubscription{UserID: input.UserID, PlanID: input.PlanID}, false, nil
}

func (s *emailSyncRepoStub) Create(_ context.Context, user *identity.User) error {
	if s.nextID != 0 && user.ID == 0 {
		user.ID = s.nextID
	}
	s.created = append(s.created, user)
	s.user = user
	return nil
}

func (s *emailSyncRepoStub) CreateWithNormalizedEmailGuard(ctx context.Context, user *identity.User, normalizedEmail string) error {
	return s.Create(ctx, user)
}

func (s *emailSyncRepoStub) GetByID(_ context.Context, _ int64) (*identity.User, error) {
	if s.user == nil {
		return nil, identity.ErrUserNotFound
	}
	cloned := *s.user
	return &cloned, nil
}

func (s *emailSyncRepoStub) GetByEmail(_ context.Context, _ string) (*identity.User, error) {
	return nil, identity.ErrUserNotFound
}

func (s *emailSyncRepoStub) GetFirstAdmin(context.Context) (*identity.User, error) {
	return nil, fmt.Errorf("unexpected GetFirstAdmin call")
}

func (s *emailSyncRepoStub) Update(_ context.Context, user *identity.User, _ identity.UserUpdateFields) error {
	s.updateCalls++
	s.updated = append(s.updated, user)
	s.user = user
	return nil
}

func (s *emailSyncRepoStub) UpdateWithNormalizedEmailGuard(ctx context.Context, user *identity.User, normalizedEmail string, fields identity.UserUpdateFields) error {
	return s.Update(ctx, user, fields)
}

func (s *emailSyncRepoStub) Delete(context.Context, int64) error { return nil }

func (s *emailSyncRepoStub) GetUserAvatar(context.Context, int64) (*identity.UserAvatar, error) {
	return nil, fmt.Errorf("unexpected GetUserAvatar call")
}

func (s *emailSyncRepoStub) UpsertUserAvatar(context.Context, int64, identity.UpsertUserAvatarInput) (*identity.UserAvatar, error) {
	return nil, fmt.Errorf("unexpected UpsertUserAvatar call")
}

func (s *emailSyncRepoStub) DeleteUserAvatar(context.Context, int64) error {
	return fmt.Errorf("unexpected DeleteUserAvatar call")
}

func (s *emailSyncRepoStub) List(context.Context, pagination.PaginationParams) ([]identity.User, *pagination.PaginationResult, error) {
	return nil, nil, fmt.Errorf("unexpected List call")
}

func (s *emailSyncRepoStub) ListWithFilters(context.Context, pagination.PaginationParams, identity.UserListFilters) ([]identity.User, *pagination.PaginationResult, error) {
	return nil, nil, fmt.Errorf("unexpected ListWithFilters call")
}

func (s *emailSyncRepoStub) GetLatestUsedAtByUserIDs(context.Context, []int64) (map[int64]*time.Time, error) {
	return map[int64]*time.Time{}, nil
}

func (s *emailSyncRepoStub) GetLatestUsedAtByUserID(context.Context, int64) (*time.Time, error) {
	return nil, nil
}

func (s *emailSyncRepoStub) UpdateUserLastActiveAt(context.Context, int64, time.Time) error {
	return nil
}

func (s *emailSyncRepoStub) UpdateBalance(context.Context, int64, float64) error { return nil }

func (s *emailSyncRepoStub) AddBalance(context.Context, int64, float64) error { return nil }

func (s *emailSyncRepoStub) DeductBalance(context.Context, int64, float64) (float64, error) {
	return 0, nil
}

func (s *emailSyncRepoStub) AdjustBalance(ctx context.Context, id int64, delta float64) (identity.BalanceChange, error) {
	panic("unexpected AdjustBalance call")
}

func (s *emailSyncRepoStub) SetBalance(ctx context.Context, id int64, value float64) (identity.BalanceChange, error) {
	panic("unexpected SetBalance call")
}

func (s *emailSyncRepoStub) UpdateConcurrency(context.Context, int64, int) error { return nil }

func (s *emailSyncRepoStub) BatchSetConcurrency(context.Context, []int64, int) (int, error) {
	return 0, nil
}

func (s *emailSyncRepoStub) BatchAddConcurrency(context.Context, []int64, int) (int, error) {
	return 0, nil
}

func (s *emailSyncRepoStub) BatchUpdateLimits(context.Context, []int64, *int, *int) (int, error) {
	return 0, nil
}

func (s *emailSyncRepoStub) ExistsByEmail(context.Context, string) (bool, error) { return false, nil }

func (s *emailSyncRepoStub) ExistsByNormalizedEmail(context.Context, string) (bool, error) {
	return false, nil
}

func (s *emailSyncRepoStub) LockRegistrationEmail(context.Context, string) error { return nil }

func (s *emailSyncRepoStub) RemoveGroupFromAllowedGroups(context.Context, int64) (int64, error) {
	return 0, nil
}

func (s *emailSyncRepoStub) AddGroupToAllowedGroups(context.Context, int64, int64) error { return nil }

func (s *emailSyncRepoStub) RemoveGroupFromUserAllowedGroups(context.Context, int64, int64) error {
	return nil
}

func (s *emailSyncRepoStub) ListUserAuthIdentities(context.Context, int64) ([]identity.UserAuthIdentityRecord, error) {
	return nil, nil
}

func (s *emailSyncRepoStub) UnbindUserAuthProvider(context.Context, int64, string) error { return nil }

func (s *emailSyncRepoStub) UpdateTotpSecret(context.Context, int64, *string) error { return nil }

func (s *emailSyncRepoStub) EnableTotp(context.Context, int64) error { return nil }

func (s *emailSyncRepoStub) DisableTotp(context.Context, int64) error { return nil }

func (s *emailSyncRepoStub) GetByIDIncludeDeleted(ctx context.Context, id int64) (*identity.User, error) {
	return s.GetByID(ctx, id)
}

func (s *emailSyncRepoStub) EnsureEmailAuthIdentity(_ context.Context, userID int64, email string) error {
	s.ensureCalls = append(s.ensureCalls, ensureEmailCall{userID: userID, email: email})
	return s.ensureErr
}

func (s *emailSyncRepoStub) ReplaceEmailAuthIdentity(_ context.Context, userID int64, oldEmail, newEmail string) error {
	s.replaceCalls = append(s.replaceCalls, replaceEmailCall{
		userID:   userID,
		oldEmail: oldEmail,
		newEmail: newEmail,
	})
	return s.replaceErr
}

func cloneEmailNormalizationUser(u *identity.User) *identity.User {
	if u == nil {
		return nil
	}
	cloned := *u
	if u.AllowedGroups != nil {
		cloned.AllowedGroups = append([]int64(nil), u.AllowedGroups...)
	}
	if u.BalanceNotifyExtraEmails != nil {
		cloned.BalanceNotifyExtraEmails = append([]billing.NotifyEmailSummary(nil), u.BalanceNotifyExtraEmails...)
	}
	if u.GroupRates != nil {
		cloned.GroupRates = make(map[int64]float64, len(u.GroupRates))
		maps.Copy(cloned.GroupRates, u.GroupRates)
	}
	return &cloned
}

func (s *emailNormalizationRepoStub) Create(_ context.Context, user *identity.User) error {
	if s.createErr != nil {
		return s.createErr
	}
	cloned := cloneEmailNormalizationUser(user)
	s.createCalls = append(s.createCalls, cloned)
	s.user = cloneEmailNormalizationUser(cloned)
	return nil
}

func (s *emailNormalizationRepoStub) CreateWithNormalizedEmailGuard(ctx context.Context, user *identity.User, normalizedEmail string) error {
	return s.Create(ctx, user)
}

func (s *emailNormalizationRepoStub) GetByID(context.Context, int64) (*identity.User, error) {
	if s.getByIDErr != nil {
		return nil, s.getByIDErr
	}
	return cloneEmailNormalizationUser(s.user), nil
}

func (s *emailNormalizationRepoStub) GetByIDIncludeDeleted(ctx context.Context, id int64) (*identity.User, error) {
	return s.GetByID(ctx, id)
}

func (s *emailNormalizationRepoStub) GetByEmail(context.Context, string) (*identity.User, error) {
	return nil, identity.ErrUserNotFound
}

func (s *emailNormalizationRepoStub) GetFirstAdmin(context.Context) (*identity.User, error) {
	return nil, identity.ErrUserNotFound
}

func (s *emailNormalizationRepoStub) Update(_ context.Context, user *identity.User, _ identity.UserUpdateFields) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	cloned := cloneEmailNormalizationUser(user)
	s.updateCalls = append(s.updateCalls, cloned)
	s.user = cloneEmailNormalizationUser(cloned)
	return nil
}

func (s *emailNormalizationRepoStub) UpdateWithNormalizedEmailGuard(_ context.Context, user *identity.User, normalizedEmail string, _ identity.UserUpdateFields) error {
	if s.normalizedUpdateErr != nil {
		return s.normalizedUpdateErr
	}
	cloned := cloneEmailNormalizationUser(user)
	s.normalizedUpdateCalls = append(s.normalizedUpdateCalls, normalizedEmail)
	s.normalizedUpdateUsers = append(s.normalizedUpdateUsers, cloned)
	s.user = cloneEmailNormalizationUser(cloned)
	return nil
}

func (s *emailNormalizationRepoStub) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}

func (s *emailNormalizationRepoStub) GetUserAvatar(context.Context, int64) (*identity.UserAvatar, error) {
	panic("unexpected GetUserAvatar call")
}

func (s *emailNormalizationRepoStub) UpsertUserAvatar(context.Context, int64, identity.UpsertUserAvatarInput) (*identity.UserAvatar, error) {
	panic("unexpected UpsertUserAvatar call")
}

func (s *emailNormalizationRepoStub) DeleteUserAvatar(context.Context, int64) error {
	panic("unexpected DeleteUserAvatar call")
}

func (s *emailNormalizationRepoStub) List(context.Context, pagination.PaginationParams) ([]identity.User, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

func (s *emailNormalizationRepoStub) ListWithFilters(context.Context, pagination.PaginationParams, identity.UserListFilters) ([]identity.User, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

func (s *emailNormalizationRepoStub) GetLatestUsedAtByUserIDs(context.Context, []int64) (map[int64]*time.Time, error) {
	return map[int64]*time.Time{}, nil
}

func (s *emailNormalizationRepoStub) GetLatestUsedAtByUserID(context.Context, int64) (*time.Time, error) {
	return nil, nil
}

func (s *emailNormalizationRepoStub) UpdateUserLastActiveAt(context.Context, int64, time.Time) error {
	return nil
}

func (s *emailNormalizationRepoStub) AddBalance(context.Context, int64, float64) error {
	panic("unexpected AddBalance call")
}

func (s *emailNormalizationRepoStub) UpdateBalance(context.Context, int64, float64) error {
	panic("unexpected UpdateBalance call")
}

func (s *emailNormalizationRepoStub) DeductBalance(context.Context, int64, float64) (float64, error) {
	panic("unexpected DeductBalance call")
}

func (s *emailNormalizationRepoStub) AdjustBalance(context.Context, int64, float64) (identity.BalanceChange, error) {
	panic("unexpected AdjustBalance call")
}

func (s *emailNormalizationRepoStub) SetBalance(context.Context, int64, float64) (identity.BalanceChange, error) {
	panic("unexpected SetBalance call")
}

func (s *emailNormalizationRepoStub) UpdateConcurrency(context.Context, int64, int) error {
	panic("unexpected UpdateConcurrency call")
}

func (s *emailNormalizationRepoStub) BatchSetConcurrency(context.Context, []int64, int) (int, error) {
	panic("unexpected BatchSetConcurrency call")
}

func (s *emailNormalizationRepoStub) BatchAddConcurrency(context.Context, []int64, int) (int, error) {
	panic("unexpected BatchAddConcurrency call")
}

func (s *emailNormalizationRepoStub) BatchUpdateLimits(context.Context, []int64, *int, *int) (int, error) {
	panic("unexpected BatchUpdateLimits call")
}

func (s *emailNormalizationRepoStub) ExistsByEmail(_ context.Context, email string) (bool, error) {
	s.existsByEmailCalls = append(s.existsByEmailCalls, email)
	if s.existsByEmailErr != nil {
		return false, s.existsByEmailErr
	}
	return s.existsByEmail, nil
}

func (s *emailNormalizationRepoStub) ExistsByNormalizedEmail(_ context.Context, normalizedEmail string) (bool, error) {
	s.existsByNormalizedCalls = append(s.existsByNormalizedCalls, normalizedEmail)
	if s.existsByNormalizedErr != nil {
		return false, s.existsByNormalizedErr
	}
	return s.existsByNormalized, nil
}

func (s *emailNormalizationRepoStub) LockRegistrationEmail(context.Context, string) error {
	panic("unexpected LockRegistrationEmail call")
}

func (s *emailNormalizationRepoStub) RemoveGroupFromAllowedGroups(context.Context, int64) (int64, error) {
	panic("unexpected RemoveGroupFromAllowedGroups call")
}

func (s *emailNormalizationRepoStub) AddGroupToAllowedGroups(context.Context, int64, int64) error {
	panic("unexpected AddGroupToAllowedGroups call")
}

func (s *emailNormalizationRepoStub) RemoveGroupFromUserAllowedGroups(context.Context, int64, int64) error {
	panic("unexpected RemoveGroupFromUserAllowedGroups call")
}

func (s *emailNormalizationRepoStub) ListUserAuthIdentities(context.Context, int64) ([]identity.UserAuthIdentityRecord, error) {
	return nil, nil
}

func (s *emailNormalizationRepoStub) UnbindUserAuthProvider(context.Context, int64, string) error {
	panic("unexpected UnbindUserAuthProvider call")
}

func (s *emailNormalizationRepoStub) UpdateTotpSecret(context.Context, int64, *string) error {
	panic("unexpected UpdateTotpSecret call")
}

func (s *emailNormalizationRepoStub) EnableTotp(context.Context, int64) error {
	panic("unexpected EnableTotp call")
}

func (s *emailNormalizationRepoStub) DisableTotp(context.Context, int64) error {
	panic("unexpected DisableTotp call")
}

func (s *userRepoStub) Create(ctx context.Context, user *identity.User) error {
	if s.createErr != nil {
		return s.createErr
	}
	if s.nextID != 0 && user.ID == 0 {
		user.ID = s.nextID
	}
	s.created = append(s.created, user)
	if s.usersByEmail == nil {
		s.usersByEmail = make(map[string]*identity.User)
	}
	s.usersByEmail[user.Email] = user
	s.user = user
	return nil
}

func (s *userRepoStub) CreateWithNormalizedEmailGuard(ctx context.Context, user *identity.User, normalizedEmail string) error {
	return s.Create(ctx, user)
}

func (s *userRepoStub) CountUsersByEmailDomain(_ context.Context, domain string) (int, error) {
	if s.domainCountErr != nil {
		return 0, s.domainCountErr
	}
	return s.domainCounts[domain], nil
}

func (s *userRepoStub) CreateWithRegistrationEmailGuards(ctx context.Context, user *identity.User, _ string, domain string) error {
	s.domainGuardCalls = append(s.domainGuardCalls, domain)
	if s.domainCounts[domain] > 0 {
		return identity.ErrEmailDomainRegistrationLimit
	}
	if err := s.Create(ctx, user); err != nil {
		return err
	}
	if s.domainCounts == nil {
		s.domainCounts = make(map[string]int)
	}
	s.domainCounts[domain]++
	return nil
}

func (s *userRepoStub) GetByID(ctx context.Context, id int64) (*identity.User, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.user == nil {
		return nil, identity.ErrUserNotFound
	}
	return s.user, nil
}

func (s *userRepoStub) GetByEmail(ctx context.Context, email string) (*identity.User, error) {
	if s.getByEmailErr != nil {
		return nil, s.getByEmailErr
	}
	if s.usersByEmail != nil {
		if user, ok := s.usersByEmail[email]; ok {
			return user, nil
		}
	}
	if s.user != nil && s.user.Email == email {
		return s.user, nil
	}
	return nil, identity.ErrUserNotFound
}

func (s *userRepoStub) GetFirstAdmin(ctx context.Context) (*identity.User, error) {
	panic("unexpected GetFirstAdmin call")
}

func (s *userRepoStub) Update(ctx context.Context, user *identity.User, fields identity.UserUpdateFields) error {
	s.updated = append(s.updated, user)
	if s.usersByEmail == nil {
		s.usersByEmail = make(map[string]*identity.User)
	}
	s.usersByEmail[user.Email] = user
	s.user = user
	return nil
}

func (s *userRepoStub) UpdateWithNormalizedEmailGuard(ctx context.Context, user *identity.User, normalizedEmail string, fields identity.UserUpdateFields) error {
	return s.Update(ctx, user, fields)
}

func (s *userRepoStub) Delete(ctx context.Context, id int64) error {
	s.deletedIDs = append(s.deletedIDs, id)
	return s.deleteErr
}

func (s *userRepoStub) GetUserAvatar(ctx context.Context, userID int64) (*identity.UserAvatar, error) {
	panic("unexpected GetUserAvatar call")
}

func (s *userRepoStub) UpsertUserAvatar(ctx context.Context, userID int64, input identity.UpsertUserAvatarInput) (*identity.UserAvatar, error) {
	panic("unexpected UpsertUserAvatar call")
}

func (s *userRepoStub) DeleteUserAvatar(ctx context.Context, userID int64) error {
	panic("unexpected DeleteUserAvatar call")
}

func (s *userRepoStub) List(ctx context.Context, params pagination.PaginationParams) ([]identity.User, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

func (s *userRepoStub) ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters identity.UserListFilters) ([]identity.User, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

func (s *userRepoStub) GetLatestUsedAtByUserIDs(ctx context.Context, userIDs []int64) (map[int64]*time.Time, error) {
	panic("unexpected GetLatestUsedAtByUserIDs call")
}

func (s *userRepoStub) GetLatestUsedAtByUserID(ctx context.Context, userID int64) (*time.Time, error) {
	panic("unexpected GetLatestUsedAtByUserID call")
}

func (s *userRepoStub) UpdateUserLastActiveAt(ctx context.Context, userID int64, activeAt time.Time) error {
	panic("unexpected UpdateUserLastActiveAt call")
}

func (s *userRepoStub) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	panic("unexpected UpdateBalance call")
}

func (s *userRepoStub) AddBalance(ctx context.Context, id int64, amount float64) error {
	if s.user != nil && s.user.ID == id {
		s.user.Balance += amount
	}
	for _, user := range s.created {
		if user != nil && user.ID == id {
			user.Balance += amount
		}
	}
	return nil
}

func (s *userRepoStub) DeductBalance(ctx context.Context, id int64, amount float64) (float64, error) {
	panic("unexpected DeductBalance call")
}

func (s *userRepoStub) AdjustBalance(ctx context.Context, id int64, delta float64) (identity.BalanceChange, error) {
	panic("unexpected AdjustBalance call")
}

func (s *userRepoStub) SetBalance(ctx context.Context, id int64, value float64) (identity.BalanceChange, error) {
	panic("unexpected SetBalance call")
}

func (s *userRepoStub) UpdateConcurrency(ctx context.Context, id int64, amount int) error {
	panic("unexpected UpdateConcurrency call")
}

func (s *userRepoStub) BatchSetConcurrency(ctx context.Context, userIDs []int64, value int) (int, error) {
	panic("unexpected BatchSetConcurrency call")
}

func (s *userRepoStub) BatchAddConcurrency(ctx context.Context, userIDs []int64, delta int) (int, error) {
	panic("unexpected BatchAddConcurrency call")
}

func (s *userRepoStub) BatchUpdateLimits(context.Context, []int64, *int, *int) (int, error) {
	panic("unexpected BatchUpdateLimits call")
}

func (s *userRepoStub) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	if s.existsErr != nil {
		return false, s.existsErr
	}
	return s.exists, nil
}

func (s *userRepoStub) ExistsByNormalizedEmail(ctx context.Context, normalizedEmail string) (bool, error) {
	return s.ExistsByEmail(ctx, normalizedEmail)
}

func (s *userRepoStub) LockRegistrationEmail(ctx context.Context, normalizedEmail string) error {
	return nil
}

func (s *userRepoStub) RemoveGroupFromAllowedGroups(ctx context.Context, groupID int64) (int64, error) {
	panic("unexpected RemoveGroupFromAllowedGroups call")
}

func (s *userRepoStub) RemoveGroupFromUserAllowedGroups(ctx context.Context, userID int64, groupID int64) error {
	panic("unexpected RemoveGroupFromUserAllowedGroups call")
}

func (s *userRepoStub) AddGroupToAllowedGroups(ctx context.Context, userID int64, groupID int64) error {
	panic("unexpected AddGroupToAllowedGroups call")
}

func (s *userRepoStub) ListUserAuthIdentities(ctx context.Context, userID int64) ([]identity.UserAuthIdentityRecord, error) {
	panic("unexpected ListUserAuthIdentities call")
}

func (s *userRepoStub) UnbindUserAuthProvider(context.Context, int64, string) error {
	panic("unexpected UnbindUserAuthProvider call")
}

func (s *userRepoStub) UpdateTotpSecret(ctx context.Context, userID int64, encryptedSecret *string) error {
	panic("unexpected UpdateTotpSecret call")
}

func (s *userRepoStub) EnableTotp(ctx context.Context, userID int64) error {
	panic("unexpected EnableTotp call")
}

func (s *userRepoStub) DisableTotp(ctx context.Context, userID int64) error {
	panic("unexpected DisableTotp call")
}

func (s *userRepoStub) GetByIDIncludeDeleted(ctx context.Context, id int64) (*identity.User, error) {
	return s.GetByID(ctx, id)
}

func newOAuthEmailFlowAuthService(
	userRepo identity.UserRepository,
	redeemRepo billing.RedeemCodeRepository,
	refreshTokenCache identity.RefreshTokenCache,
	settings map[string]string,
	emailCache identity.EmailCache,
) *identity.AuthService {
	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:                   "test-secret",
			ExpireHour:               1,
			AccessTokenExpireMinutes: 60,
			RefreshTokenExpireDays:   7,
		},
		Default: config.DefaultConfig{
			UserBalance:     3.5,
			UserConcurrency: 2,
		},
	}

	settingService := newAuthSettingsFixture(&settingRepoStub{values: settings}, cfg)
	emailService := identity.NewEmailChallenges(emailCache, notification.NewMailer(&settingRepoStub{values: settings}, smtp.New()))

	return identitytestkit.Auth(
		nil, &identity.AuthDependencies{Users: userRepo, Redeem: redeemRepo, RefreshTokens: refreshTokenCache, Options: identitytestkit.AuthOptions(cfg), Settings: authSettingsPort(settingService), Email: identitytestkit.Email(emailService)}, // Authentication dependencies.
	)
}

func (s *settingRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

func (s *settingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getValueCalls++
	if s.err != nil {
		return "", s.err
	}
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", settingscore.ErrSettingNotFound
}

func (s *settingRepoStub) Set(ctx context.Context, key, value string) error {
	panic("unexpected Set call")
}

func (s *settingRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getMultipleCalls++
	if s.err != nil {
		return nil, s.err
	}
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if v, ok := s.values[key]; ok {
			result[key] = v
		}
	}
	return result, nil
}

func (s *settingRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *settingRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *settingRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

func (s *refreshTokenCacheStub) StoreRefreshToken(context.Context, string, *identity.RefreshTokenData, time.Duration) error {
	return nil
}

func (s *refreshTokenCacheStub) GetRefreshToken(context.Context, string) (*identity.RefreshTokenData, error) {
	return nil, identity.ErrRefreshTokenNotFound
}

func (s *refreshTokenCacheStub) DeleteRefreshToken(context.Context, string) error {
	return nil
}

func (s *refreshTokenCacheStub) DeleteUserRefreshTokens(context.Context, int64) error {
	return nil
}

func (s *refreshTokenCacheStub) DeleteTokenFamily(context.Context, string) error {
	return nil
}

func (s *refreshTokenCacheStub) AddToUserTokenSet(context.Context, int64, string, time.Duration) error {
	return nil
}

func (s *refreshTokenCacheStub) AddToFamilyTokenSet(context.Context, string, string, time.Duration) error {
	return nil
}

func (s *refreshTokenCacheStub) GetUserTokenHashes(context.Context, int64) ([]string, error) {
	return nil, nil
}

func (s *refreshTokenCacheStub) GetFamilyTokenHashes(context.Context, string) ([]string, error) {
	return nil, nil
}

func (s *refreshTokenCacheStub) IsTokenInFamily(context.Context, string, string) (bool, error) {
	return false, nil
}

func (s *emailCacheStub) GetVerificationCode(ctx context.Context, email string) (*identity.VerificationCodeData, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.data, nil
}

func (s *emailCacheStub) SetVerificationCode(ctx context.Context, email string, data *identity.VerificationCodeData, ttl time.Duration) error {
	return nil
}

func (s *emailCacheStub) DeleteVerificationCode(ctx context.Context, email string) error {
	return nil
}

func (s *emailCacheStub) GetNotifyVerifyCode(ctx context.Context, email string) (*identity.VerificationCodeData, error) {
	return nil, nil
}

func (s *emailCacheStub) SetNotifyVerifyCode(ctx context.Context, email string, data *identity.VerificationCodeData, ttl time.Duration) error {
	return nil
}

func (s *emailCacheStub) DeleteNotifyVerifyCode(ctx context.Context, email string) error {
	return nil
}

func (s *emailCacheStub) GetPasswordResetToken(ctx context.Context, email string) (*identity.PasswordResetTokenData, error) {
	return nil, nil
}

func (s *emailCacheStub) SetPasswordResetToken(ctx context.Context, email string, data *identity.PasswordResetTokenData, ttl time.Duration) error {
	return nil
}

func (s *emailCacheStub) DeletePasswordResetToken(ctx context.Context, email string) error {
	return nil
}

func (s *emailCacheStub) IsPasswordResetEmailInCooldown(ctx context.Context, email string) bool {
	return false
}

func (s *emailCacheStub) SetPasswordResetEmailCooldown(ctx context.Context, email string, ttl time.Duration) error {
	return nil
}

func (s *emailCacheStub) GetNotifyCodeUserRate(ctx context.Context, userID int64) (int64, error) {
	return 0, nil
}

func (s *emailCacheStub) IncrNotifyCodeUserRate(ctx context.Context, userID int64, window time.Duration) (int64, error) {
	return 0, nil
}

func newAuthService(repo *userRepoStub, settings map[string]string, emailCache identity.EmailCache) *identity.AuthService {
	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:     "test-secret",
			ExpireHour: 1,
		},
		Default: config.DefaultConfig{
			UserBalance:     3.5,
			UserConcurrency: 2,
		},
	}

	var settingService *authSettingsFixture
	if settings != nil {
		settingService = newAuthSettingsFixture(&settingRepoStub{values: settings}, cfg)
	}

	var emailService *identity.EmailChallenges
	if emailCache != nil {
		emailService = identity.NewEmailChallenges(emailCache, notification.NewMailer(&settingRepoStub{values: settings}, smtp.New()))
	}

	return identitytestkit.Auth(
		nil, &identity.AuthDependencies{Users: repo, Options: identitytestkit.AuthOptions(cfg), Settings: authSettingsPort(settingService), Email: identitytestkit.Email(emailService)},
	)
}

// ConsumeRefreshToken reports that the credential was not found.
func (s *refreshTokenCacheStub) ConsumeRefreshToken(context.Context, string) (bool, error) {
	return false, nil
}

func (s *turnstileVerifierSpy) VerifyToken(_ context.Context, _ string, token, _ string) (*identity.TurnstileVerifyResponse, error) {
	s.called++
	s.lastToken = token
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &identity.TurnstileVerifyResponse{Success: true}, nil
}

func newAuthSettingsFixture(repo settingscore.Repository, cfg *config.Config) *authSettingsFixture {
	var defaults *identity.OAuthSettingsDefaults
	grants := identity.GrantSettingsOptions{}
	if cfg != nil {
		defaults = &identity.OAuthSettingsDefaults{LinuxDo: cfg.LinuxDo, DingTalk: cfg.DingTalk, OIDC: cfg.OIDC, WeChat: cfg.WeChat, GitHubOAuth: cfg.GitHubOAuth, GoogleOAuth: cfg.GoogleOAuth}
		grants.DefaultBalance = cfg.Default.UserBalance
		grants.DefaultConcurrency = cfg.Default.UserConcurrency
	}
	return &authSettingsFixture{
		RuntimeSettings: identity.NewRuntimeSettings(repo, settingscore.ErrSettingNotFound),
		GrantSettings:   identity.NewGrantSettings(repo, grants),
		OAuthSettings:   identity.NewOAuthSettings(repo, defaults, identityprovider.ResolveSettingsOIDCMetadata),
		DisplaySettings: site.NewDisplaySettings(repo, nil),
		promotion:       promotion.NewRuntimeSettings(repo),
	}
}

func (s *authSettingsFixture) GetDingTalkConnectOAuthConfig(ctx context.Context) (identity.DingTalkRegistrationPolicy, error) {
	value, err := s.OAuthSettings.GetDingTalkConnectOAuthConfig(ctx)
	return identity.DingTalkRegistrationPolicy{Enabled: value.Enabled, BypassRegistration: value.BypassRegistration, CorpRestrictionPolicy: value.CorpRestrictionPolicy}, err
}

func (s *authSettingsFixture) IsInvitationCodeEnabled(ctx context.Context) bool {
	return s.promotion.IsInvitationCodeEnabled(ctx)
}

func (s *authSettingsFixture) IsPromoCodeEnabled(ctx context.Context) bool {
	return s.promotion.IsPromoCodeEnabled(ctx)
}

// authSettingsPort returns a nil interface for a nil fixture.
func authSettingsPort(value *authSettingsFixture) identity.AuthSettings {
	if value == nil {
		return nil
	}
	return value
}

// rebuildSessionForTest rebuilds the session service from the fixture options and cache.
func rebuildSessionForTest(service *identity.AuthService) {
	service.SessionService = identity.NewSessionService(service.Options.JWT, service.Users, service.RefreshTokens, service.Settings, service.Observer.Log)
}

func (s *captchaSettingsStore) GetValue(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getValueCalls++
	if s.err != nil {
		return "", s.err
	}
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", settingscore.ErrSettingNotFound
}

func (s *captchaSettingsStore) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getMultipleCalls++
	if s.err != nil {
		return nil, s.err
	}
	result := make(map[string]string, len(keys))
	for _, k := range keys {
		if v, ok := s.values[k]; ok {
			result[k] = v
		}
	}
	return result, nil
}

func (s captchaAuthSettings) GetCaptchaProviderConfig(ctx context.Context) (identity.CaptchaProviderConfig, error) {
	return s.runtime.GetCaptchaProviderConfig(ctx)
}

func (s captchaAuthSettings) IsEmailVerifyEnabled(ctx context.Context) bool {
	return s.runtime.IsEmailVerifyEnabled(ctx)
}

func captchaAuthOptions(required bool) *identity.AuthOptions {
	options := &identity.AuthOptions{}
	options.Server.Mode = "release"
	options.Turnstile.Required = required
	return options
}

func (s *authSourceDefaultsRepoStub) Get(ctx context.Context, key string) (*settingscore.Setting, error) {
	panic("unexpected Get call")
}

func (s *authSourceDefaultsRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	panic("unexpected GetValue call")
}

func (s *authSourceDefaultsRepoStub) Set(ctx context.Context, key, value string) error {
	panic("unexpected Set call")
}

func (s *authSourceDefaultsRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (s *authSourceDefaultsRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	s.updates = make(map[string]string, len(settings))
	for key, value := range settings {
		s.updates[key] = value
		if s.values == nil {
			s.values = map[string]string{}
		}
		s.values[key] = value
	}
	return nil
}

func (s *authSourceDefaultsRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *authSourceDefaultsRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

func (s *tencentCaptchaVerifierStub) VerifyTicket(_ context.Context, credentials identity.TencentCaptchaCredentials, proof identity.TencentCaptchaProof, remoteIP string) (*identity.TencentCaptchaVerifyResponse, error) {
	s.calls++
	s.credentials = credentials
	s.proof = proof
	s.remoteIP = remoteIP
	return s.response, s.err
}
