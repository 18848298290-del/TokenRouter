package identity_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

type mockUserRepo struct {
	updateBalanceErr        error
	updateBalanceFn         func(ctx context.Context, id int64, amount float64) error
	deductBalanceFn         func(ctx context.Context, id int64, amount float64) error
	deductBalanceResultFn   func(ctx context.Context, id int64, amount float64) (float64, error)
	getByIDUser             *identity.User
	getByIDErr              error
	identities              []identity.UserAuthIdentityRecord
	unbindIdentityErr       error
	unboundProviders        []string
	updateLastActiveErr     error
	updateLastActiveUserIDs []int64
	updateLastActiveAt      []time.Time
	updateFn                func(ctx context.Context, user *identity.User) error
	updateCalls             int
	updateFields            []identity.UserUpdateFields
	upsertAvatarFn          func(ctx context.Context, userID int64, input identity.UpsertUserAvatarInput) (*identity.UserAvatar, error)
	upsertAvatarArgs        []identity.UpsertUserAvatarInput
	deleteAvatarFn          func(ctx context.Context, userID int64) error
	deleteAvatarIDs         []int64
	getAvatarFn             func(ctx context.Context, userID int64) (*identity.UserAvatar, error)
	txCalls                 int
}

type mockUserRepoTxKey struct{}

type mockUserRepoTxState struct {
	getByIDUser      *identity.User
	upsertAvatarArgs []identity.UpsertUserAvatarInput
	deleteAvatarIDs  []int64
}

type mockUserSettingRepo struct {
	values map[string]string
}

type mockAuthCacheInvalidator struct {
	invalidatedUserIDs []int64
	mu                 sync.Mutex
}

type mockBillingCache struct {
	invalidateErr       error
	invalidateCallCount atomic.Int64
	invalidatedUserIDs  []int64
	mu                  sync.Mutex
}

func TestUserService_UpdateProfile_RejectsEmailWhenNormalizationEnabled(t *testing.T) {
	repo := &emailNormalizationRepoStub{
		user: &identity.User{
			ID:          7,
			Email:       "old@example.com",
			Username:    "old-name",
			Concurrency: 2,
		},
	}
	svc := identity.NewUserService(repo, &settingRepoStub{values: map[string]string{
		identity.SettingKeyRegistrationEmailNormalization: "true",
	}}, nil, nil, runProfileBackground)
	newEmail := "Y.o.u.r.N.a.m.e+promo@example.com"
	newUsername := "new-name"

	_, err := svc.UpdateProfile(context.Background(), 7, identity.UpdateProfileRequest{
		Email:    &newEmail,
		Username: &newUsername,
	})
	require.ErrorIs(t, err, identity.ErrProfileEmailChangeForbidden)
	require.Empty(t, repo.existsByEmailCalls)
	require.Empty(t, repo.normalizedUpdateCalls)
	require.Empty(t, repo.normalizedUpdateUsers)
	require.Empty(t, repo.updateCalls)
	require.Equal(t, "old@example.com", repo.user.Email)
	require.Equal(t, "old-name", repo.user.Username)
}

func TestUserService_UpdateProfile_RejectsEmailWhenNormalizationDisabled(t *testing.T) {
	repo := &emailNormalizationRepoStub{
		user: &identity.User{
			ID:    8,
			Email: "old@example.com",
		},
		existsByEmail: true,
	}
	svc := identity.NewUserService(repo, &settingRepoStub{values: map[string]string{}}, nil, nil, runProfileBackground)
	newEmail := "duplicate@example.com"

	_, err := svc.UpdateProfile(context.Background(), 8, identity.UpdateProfileRequest{Email: &newEmail})
	require.ErrorIs(t, err, identity.ErrProfileEmailChangeForbidden)
	require.Empty(t, repo.existsByEmailCalls)
	require.Empty(t, repo.normalizedUpdateCalls)
	require.Empty(t, repo.updateCalls)
}

func TestUpdateProfile_RejectsEmailBeforeEmailIdentityResync(t *testing.T) {
	repo := &emailSyncRepoStub{
		user: &identity.User{
			ID:          19,
			Email:       "profile-before@example.com",
			Username:    "tester",
			Concurrency: 2,
		},
		replaceErr: context.DeadlineExceeded,
	}
	svc := identity.NewUserService(repo, nil, nil, nil, nil)

	newEmail := "profile-after@example.com"
	_, err := svc.UpdateProfile(context.Background(), 19, identity.UpdateProfileRequest{
		Email: &newEmail,
	})
	require.ErrorIs(t, err, identity.ErrProfileEmailChangeForbidden)
	require.Equal(t, 0, repo.updateCalls)
	require.Empty(t, repo.replaceCalls)
	require.Empty(t, repo.ensureCalls)
	require.Equal(t, "profile-before@example.com", repo.user.Email)
}

// runProfileBackground starts cache invalidation asynchronously; tests wait for observed calls.
func runProfileBackground(_ string, task func()) bool {
	go task()
	return true
}

func boolPtr(value bool) *bool { return &value }

func float64Ptr(value float64) *float64 { return &value }

func mockUserRepoStateFromContext(ctx context.Context) *mockUserRepoTxState {
	state, _ := ctx.Value(mockUserRepoTxKey{}).(*mockUserRepoTxState)
	return state
}

func (m *mockUserRepo) currentUser(ctx context.Context) *identity.User {
	if state := mockUserRepoStateFromContext(ctx); state != nil {
		return state.getByIDUser
	}
	return m.getByIDUser
}

func (m *mockUserRepo) setCurrentUser(ctx context.Context, user *identity.User) {
	if state := mockUserRepoStateFromContext(ctx); state != nil {
		state.getByIDUser = user
		return
	}
	m.getByIDUser = user
}

func (m *mockUserRepo) Create(context.Context, *identity.User) error { return nil }

func (m *mockUserRepo) CreateWithNormalizedEmailGuard(ctx context.Context, user *identity.User, _ string) error {
	return m.Create(ctx, user)
}

func (m *mockUserRepo) GetByID(ctx context.Context, id int64) (*identity.User, error) {
	if m.getByIDErr != nil {
		return nil, m.getByIDErr
	}
	user := m.currentUser(ctx)
	if user == nil {
		return &identity.User{ID: id}, nil
	}
	cloned := *user
	return &cloned, nil
}

func (m *mockUserRepo) GetByEmail(context.Context, string) (*identity.User, error) {
	return &identity.User{}, nil
}

func (m *mockUserRepo) GetFirstAdmin(context.Context) (*identity.User, error) {
	return &identity.User{}, nil
}

func (m *mockUserRepo) Update(ctx context.Context, user *identity.User, fields identity.UserUpdateFields) error {
	m.updateCalls++
	m.updateFields = append(m.updateFields, fields)
	if m.updateFn != nil {
		return m.updateFn(ctx, user)
	}
	cloned := *user
	m.setCurrentUser(ctx, &cloned)
	return nil
}

func (m *mockUserRepo) UpdateWithNormalizedEmailGuard(ctx context.Context, user *identity.User, _ string, fields identity.UserUpdateFields) error {
	return m.Update(ctx, user, fields)
}

func (m *mockUserRepo) Delete(context.Context, int64) error { return nil }

func (m *mockUserRepo) GetUserAvatar(ctx context.Context, userID int64) (*identity.UserAvatar, error) {
	if m.getAvatarFn != nil {
		return m.getAvatarFn(ctx, userID)
	}
	return nil, nil
}

func (m *mockUserRepo) UpsertUserAvatar(ctx context.Context, userID int64, input identity.UpsertUserAvatarInput) (*identity.UserAvatar, error) {
	if m.upsertAvatarFn != nil {
		return m.upsertAvatarFn(ctx, userID, input)
	}
	if state := mockUserRepoStateFromContext(ctx); state != nil {
		state.upsertAvatarArgs = append(state.upsertAvatarArgs, input)
	} else {
		m.upsertAvatarArgs = append(m.upsertAvatarArgs, input)
	}
	if user := m.currentUser(ctx); user != nil {
		cloned := *user
		cloned.AvatarURL = input.URL
		cloned.AvatarSource = input.StorageProvider
		cloned.AvatarMIME = input.ContentType
		cloned.AvatarByteSize = input.ByteSize
		cloned.AvatarSHA256 = input.SHA256
		m.setCurrentUser(ctx, &cloned)
	}
	return &identity.UserAvatar{
		StorageProvider: input.StorageProvider,
		StorageKey:      input.StorageKey,
		URL:             input.URL,
		ContentType:     input.ContentType,
		ByteSize:        input.ByteSize,
		SHA256:          input.SHA256,
	}, nil
}

func (m *mockUserRepo) DeleteUserAvatar(ctx context.Context, userID int64) error {
	if m.deleteAvatarFn != nil {
		return m.deleteAvatarFn(ctx, userID)
	}
	if state := mockUserRepoStateFromContext(ctx); state != nil {
		state.deleteAvatarIDs = append(state.deleteAvatarIDs, userID)
	} else {
		m.deleteAvatarIDs = append(m.deleteAvatarIDs, userID)
	}
	if user := m.currentUser(ctx); user != nil {
		cloned := *user
		cloned.AvatarURL = ""
		cloned.AvatarSource = ""
		cloned.AvatarMIME = ""
		cloned.AvatarByteSize = 0
		cloned.AvatarSHA256 = ""
		m.setCurrentUser(ctx, &cloned)
	}
	return nil
}

func (m *mockUserRepo) List(context.Context, pagination.PaginationParams) ([]identity.User, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (m *mockUserRepo) ListWithFilters(context.Context, pagination.PaginationParams, identity.UserListFilters) ([]identity.User, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (m *mockUserRepo) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	if m.updateBalanceFn != nil {
		return m.updateBalanceFn(ctx, id, amount)
	}
	return m.updateBalanceErr
}

func (m *mockUserRepo) AddBalance(context.Context, int64, float64) error { return nil }

func (m *mockUserRepo) DeductBalance(ctx context.Context, id int64, amount float64) (float64, error) {
	if m.deductBalanceResultFn != nil {
		return m.deductBalanceResultFn(ctx, id, amount)
	}
	if m.deductBalanceFn != nil {
		return 0, m.deductBalanceFn(ctx, id, amount)
	}
	return amount, nil
}

func (m *mockUserRepo) AdjustBalance(ctx context.Context, id int64, delta float64) (identity.BalanceChange, error) {
	panic("unexpected AdjustBalance call")
}

func (m *mockUserRepo) SetBalance(ctx context.Context, id int64, value float64) (identity.BalanceChange, error) {
	panic("unexpected SetBalance call")
}

func (m *mockUserRepo) UpdateConcurrency(context.Context, int64, int) error { return nil }

func (m *mockUserRepo) BatchSetConcurrency(context.Context, []int64, int) (int, error) {
	return 0, nil
}

func (m *mockUserRepo) BatchAddConcurrency(context.Context, []int64, int) (int, error) {
	return 0, nil
}

func (m *mockUserRepo) ExistsByEmail(context.Context, string) (bool, error) { return false, nil }

func (m *mockUserRepo) ExistsByNormalizedEmail(context.Context, string) (bool, error) {
	return false, nil
}

func (m *mockUserRepo) LockRegistrationEmail(context.Context, string) error { return nil }

func (m *mockUserRepo) RemoveGroupFromAllowedGroups(context.Context, int64) (int64, error) {
	return 0, nil
}

func (m *mockUserRepo) BatchUpdateLimits(context.Context, []int64, *int, *int) (int, error) {
	return 0, nil
}

func (m *mockUserRepo) AddGroupToAllowedGroups(context.Context, int64, int64) error { return nil }

func (m *mockUserRepo) ListUserAuthIdentities(context.Context, int64) ([]identity.UserAuthIdentityRecord, error) {
	out := make([]identity.UserAuthIdentityRecord, len(m.identities))
	copy(out, m.identities)
	return out, nil
}

func (m *mockUserRepo) GetLatestUsedAtByUserIDs(context.Context, []int64) (map[int64]*time.Time, error) {
	return map[int64]*time.Time{}, nil
}

func (m *mockUserRepo) GetLatestUsedAtByUserID(context.Context, int64) (*time.Time, error) {
	return nil, nil
}

func (m *mockUserRepo) UpdateUserLastActiveAt(_ context.Context, userID int64, activeAt time.Time) error {
	if m.updateLastActiveErr != nil {
		return m.updateLastActiveErr
	}
	m.updateLastActiveUserIDs = append(m.updateLastActiveUserIDs, userID)
	m.updateLastActiveAt = append(m.updateLastActiveAt, activeAt)
	return nil
}

func (m *mockUserRepo) UpdateTotpSecret(context.Context, int64, *string) error { return nil }

func (m *mockUserRepo) EnableTotp(context.Context, int64) error { return nil }

func (m *mockUserRepo) DisableTotp(context.Context, int64) error { return nil }

func (m *mockUserRepo) RemoveGroupFromUserAllowedGroups(context.Context, int64, int64) error {
	return nil
}

func (m *mockUserRepo) UnbindUserAuthProvider(_ context.Context, _ int64, provider string) error {
	if m.unbindIdentityErr != nil {
		return m.unbindIdentityErr
	}
	m.unboundProviders = append(m.unboundProviders, provider)
	filtered := m.identities[:0]
	for _, identity := range m.identities {
		if identity.ProviderType == provider {
			continue
		}
		filtered = append(filtered, identity)
	}
	m.identities = append([]identity.UserAuthIdentityRecord(nil), filtered...)
	return nil
}

func (m *mockUserRepo) GetByIDIncludeDeleted(ctx context.Context, id int64) (*identity.User, error) {
	return m.GetByID(ctx, id)
}

func (m *mockUserRepo) WithUserProfileIdentityTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	m.txCalls++
	txState := &mockUserRepoTxState{
		upsertAvatarArgs: append([]identity.UpsertUserAvatarInput(nil), m.upsertAvatarArgs...),
		deleteAvatarIDs:  append([]int64(nil), m.deleteAvatarIDs...),
	}
	if m.getByIDUser != nil {
		userCopy := *m.getByIDUser
		txState.getByIDUser = &userCopy
	}
	err := fn(context.WithValue(ctx, mockUserRepoTxKey{}, txState))
	if err != nil {
		return err
	}
	m.getByIDUser = txState.getByIDUser
	m.upsertAvatarArgs = txState.upsertAvatarArgs
	m.deleteAvatarIDs = txState.deleteAvatarIDs
	return nil
}

func (s *mockUserSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if s != nil && s.values != nil {
		if value, ok := s.values[key]; ok {
			return value, nil
		}
	}
	return "", errors.New("setting not found")
}

func (s *mockUserSettingRepo) Set(_ context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

func (s *mockUserSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if s != nil && s.values != nil {
			if value, ok := s.values[key]; ok {
				out[key] = value
			}
		}
	}
	return out, nil
}

func (s *mockUserSettingRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	for key, value := range settings {
		if err := s.Set(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

func (s *mockUserSettingRepo) GetAll(context.Context) (map[string]string, error) {
	out := map[string]string{}
	if s == nil || s.values == nil {
		return out, nil
	}
	maps.Copy(out, s.values)
	return out, nil
}

func (s *mockUserSettingRepo) Delete(_ context.Context, key string) error {
	delete(s.values, key)
	return nil
}

func (m *mockAuthCacheInvalidator) InvalidateAuthCacheByKey(context.Context, string) {}

func (m *mockAuthCacheInvalidator) InvalidateAuthCacheByGroupID(context.Context, int64) {}

func (m *mockAuthCacheInvalidator) InvalidateAuthCacheByUserID(_ context.Context, userID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidatedUserIDs = append(m.invalidatedUserIDs, userID)
}

func (m *mockBillingCache) GetUserBalance(context.Context, int64) (float64, error) { return 0, nil }

func (m *mockBillingCache) SetUserBalance(context.Context, int64, float64) error { return nil }

func (m *mockBillingCache) DeductUserBalance(context.Context, int64, float64) error { return nil }

func (m *mockBillingCache) InvalidateUserBalance(_ context.Context, userID int64) error {
	m.invalidateCallCount.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidatedUserIDs = append(m.invalidatedUserIDs, userID)
	return m.invalidateErr
}

func TestUpdateBalance_Success(t *testing.T) {
	repo := &mockUserRepo{}
	cache := &mockBillingCache{}
	svc := identity.NewUserService(repo, nil, nil, cache, runProfileBackground)

	err := svc.UpdateBalance(context.Background(), 42, 100.0)
	require.NoError(t, err)

	// Wait for asynchronous cache invalidation.
	require.Eventually(t, func() bool {
		return cache.invalidateCallCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond, "应异步调用 InvalidateUserBalance")

	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Equal(t, []int64{42}, cache.invalidatedUserIDs, "应对 userID=42 失效缓存")
}

func TestGetProfileIdentitySummaries_AllowsUnbindWhenAnotherLoginMethodRemains(t *testing.T) {
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:    7,
			Email: "alice@example.com",
		},
		identities: []identity.UserAuthIdentityRecord{
			{
				ProviderType:    "email",
				ProviderKey:     "email",
				ProviderSubject: "alice@example.com",
			},
			{
				ProviderType:    "linuxdo",
				ProviderKey:     "linuxdo",
				ProviderSubject: "linuxdo-subject-123456",
				Metadata: map[string]any{
					"username": "linuxdo-handle",
				},
			},
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	summaries, err := svc.GetProfileIdentitySummaries(context.Background(), 7, repo.getByIDUser)

	require.NoError(t, err)
	require.True(t, summaries.LinuxDo.Bound)
	require.True(t, summaries.LinuxDo.CanUnbind)
	require.Equal(t, "linuxdo-handle", summaries.LinuxDo.DisplayName)
	require.NotEmpty(t, summaries.LinuxDo.SubjectHint)
}

func TestUnbindUserAuthProviderRejectsLastRemainingLoginMethod(t *testing.T) {
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:    9,
			Email: "only-user@linuxdo-connect.invalid",
		},
		identities: []identity.UserAuthIdentityRecord{
			{
				ProviderType:    "linuxdo",
				ProviderKey:     "linuxdo",
				ProviderSubject: "linuxdo-only-subject",
			},
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	_, err := svc.UnbindUserAuthProvider(context.Background(), 9, "linuxdo")

	require.ErrorIs(t, err, identity.ErrIdentityUnbindLastMethod)
	require.Empty(t, repo.unboundProviders)
}

func TestGetProfileIdentitySummaries_DoesNotTreatOAuthOnlyCompatEmailAsAlternativeLoginMethod(t *testing.T) {
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:           10,
			Email:        "oauth-only@example.com",
			SignupSource: "oidc",
		},
		identities: []identity.UserAuthIdentityRecord{
			{
				ProviderType:    "oidc",
				ProviderKey:     "https://issuer.example.com",
				ProviderSubject: "oidc-only-subject",
			},
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	summaries, err := svc.GetProfileIdentitySummaries(context.Background(), 10, repo.getByIDUser)

	require.NoError(t, err)
	require.False(t, summaries.OIDC.CanUnbind)

	_, err = svc.UnbindUserAuthProvider(context.Background(), 10, "oidc")
	require.ErrorIs(t, err, identity.ErrIdentityUnbindLastMethod)
	require.Empty(t, repo.unboundProviders)
}

func TestGetProfileIdentitySummaries_DoesNotTreatCompatBackfilledEmailIdentityAsAlternativeLoginMethod(t *testing.T) {
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:           11,
			Email:        "oauth-only@example.com",
			SignupSource: "wechat",
		},
		identities: []identity.UserAuthIdentityRecord{
			{
				ProviderType:    "email",
				ProviderKey:     "email",
				ProviderSubject: "oauth-only@example.com",
				Metadata: map[string]any{
					"backfill_source": "users.email",
					"migration":       "109_auth_identity_compat_backfill",
				},
			},
			{
				ProviderType:    "wechat",
				ProviderKey:     "wechat",
				ProviderSubject: "wechat-only-subject",
			},
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	summaries, err := svc.GetProfileIdentitySummaries(context.Background(), 11, repo.getByIDUser)

	require.NoError(t, err)
	require.True(t, summaries.Email.Bound)
	require.False(t, summaries.WeChat.CanUnbind)

	_, err = svc.UnbindUserAuthProvider(context.Background(), 11, "wechat")
	require.ErrorIs(t, err, identity.ErrIdentityUnbindLastMethod)
	require.Empty(t, repo.unboundProviders)
}

func TestUnbindUserAuthProviderRemovesProviderAndReturnsUpdatedProfile(t *testing.T) {
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:    12,
			Email: "alice@example.com",
		},
		identities: []identity.UserAuthIdentityRecord{
			{
				ProviderType:    "email",
				ProviderKey:     "email",
				ProviderSubject: "alice@example.com",
			},
			{
				ProviderType:    "linuxdo",
				ProviderKey:     "linuxdo",
				ProviderSubject: "linuxdo-subject-12",
			},
		},
	}
	invalidator := &mockAuthCacheInvalidator{}
	svc := identity.NewUserService(repo, nil, invalidator, nil, runProfileBackground)

	user, err := svc.UnbindUserAuthProvider(context.Background(), 12, "linuxdo")

	require.NoError(t, err)
	require.Equal(t, []string{"linuxdo"}, repo.unboundProviders)
	require.Equal(t, int64(12), user.ID)
	require.Equal(t, []int64{12}, invalidator.invalidatedUserIDs)

	summaries, err := svc.GetProfileIdentitySummaries(context.Background(), 12, user)
	require.NoError(t, err)
	require.False(t, summaries.LinuxDo.Bound)
	require.True(t, summaries.LinuxDo.CanBind)
}

func TestGetProfileIdentitySummaries_HidesBindActionWhenProviderExplicitlyDisabled(t *testing.T) {
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:    15,
			Email: "alice@example.com",
		},
		identities: []identity.UserAuthIdentityRecord{
			{
				ProviderType:    "email",
				ProviderKey:     "email",
				ProviderSubject: "alice@example.com",
			},
		},
	}
	settingRepo := &mockUserSettingRepo{
		values: map[string]string{
			identity.SettingKeyLinuxDoConnectEnabled: "false",
		},
	}
	svc := identity.NewUserService(repo, settingRepo, nil, nil, runProfileBackground)

	summaries, err := svc.GetProfileIdentitySummaries(context.Background(), 15, repo.getByIDUser)

	require.NoError(t, err)
	require.False(t, summaries.LinuxDo.Bound)
	require.False(t, summaries.LinuxDo.CanBind)
	require.Empty(t, summaries.LinuxDo.BindStartPath)
}

func TestGetProfileIdentitySummaries_UsesBindStartRoute(t *testing.T) {
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:    16,
			Email: "alice@example.com",
		},
		identities: []identity.UserAuthIdentityRecord{
			{
				ProviderType:    "email",
				ProviderKey:     "email",
				ProviderSubject: "alice@example.com",
			},
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	summaries, err := svc.GetProfileIdentitySummaries(context.Background(), 16, repo.getByIDUser)

	require.NoError(t, err)
	require.Equal(
		t,
		"/api/v1/auth/oauth/linuxdo/bind/start?intent=bind_current_user&redirect=%2Fsettings%2Fprofile",
		summaries.LinuxDo.BindStartPath,
	)
	require.Equal(
		t,
		"/api/v1/auth/oauth/oidc/bind/start?intent=bind_current_user&redirect=%2Fsettings%2Fprofile",
		summaries.OIDC.BindStartPath,
	)
	require.Equal(
		t,
		"/api/v1/auth/oauth/wechat/bind/start?intent=bind_current_user&redirect=%2Fsettings%2Fprofile",
		summaries.WeChat.BindStartPath,
	)
}

func TestUpdateBalance_NilBillingCache_NoPanic(t *testing.T) {
	repo := &mockUserRepo{}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground) // billingCache = nil

	err := svc.UpdateBalance(context.Background(), 1, 50.0)
	require.NoError(t, err, "billingCache 为 nil 时不应 panic")
}

func TestUpdateBalance_CacheFailure_DoesNotAffectReturn(t *testing.T) {
	repo := &mockUserRepo{}
	cache := &mockBillingCache{invalidateErr: errors.New("redis connection refused")}
	svc := identity.NewUserService(repo, nil, nil, cache, runProfileBackground)

	err := svc.UpdateBalance(context.Background(), 99, 200.0)
	require.NoError(t, err, "缓存失效失败不应影响主流程返回值")

	// The failing cache operation must still be attempted asynchronously.
	require.Eventually(t, func() bool {
		return cache.invalidateCallCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond, "即使失败也应调用 InvalidateUserBalance")
}

func TestTouchLastActive_UpdatesWhenStale(t *testing.T) {
	stale := time.Now().Add(-11 * time.Minute)
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:           42,
			LastActiveAt: &stale,
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	svc.TouchLastActive(context.Background(), 42)

	require.Equal(t, []int64{42}, repo.updateLastActiveUserIDs)
	require.Len(t, repo.updateLastActiveAt, 1)
	require.WithinDuration(t, time.Now(), repo.updateLastActiveAt[0], 2*time.Second)
}

func TestTouchLastActive_SkipsWhenRecent(t *testing.T) {
	recent := time.Now().Add(-time.Minute)
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:           42,
			LastActiveAt: &recent,
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	svc.TouchLastActive(context.Background(), 42)

	require.Empty(t, repo.updateLastActiveUserIDs)
	require.Empty(t, repo.updateLastActiveAt)
}

func TestUpdateBalance_RepoError_ReturnsError(t *testing.T) {
	repo := &mockUserRepo{updateBalanceErr: errors.New("database error")}
	cache := &mockBillingCache{}
	svc := identity.NewUserService(repo, nil, nil, cache, runProfileBackground)

	err := svc.UpdateBalance(context.Background(), 1, 100.0)
	require.Error(t, err, "repo 失败时应返回错误")
	require.Contains(t, err.Error(), "update balance")

	// A failed database write must not trigger cache invalidation.
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, int64(0), cache.invalidateCallCount.Load(),
		"repo 失败时不应调用 InvalidateUserBalance")
}

func TestUpdateBalance_WithAuthCacheInvalidator(t *testing.T) {
	repo := &mockUserRepo{}
	auth := &mockAuthCacheInvalidator{}
	cache := &mockBillingCache{}
	svc := identity.NewUserService(repo, nil, auth, cache, runProfileBackground)

	err := svc.UpdateBalance(context.Background(), 77, 300.0)
	require.NoError(t, err)

	// Authentication cache invalidation completes before UpdateBalance returns.
	auth.mu.Lock()
	require.Equal(t, []int64{77}, auth.invalidatedUserIDs)
	auth.mu.Unlock()

	// Billing cache invalidation runs in the background.
	require.Eventually(t, func() bool {
		return cache.invalidateCallCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

// TestNewUserServiceSharesActivityTracker checks throttling across calls on one service instance.
func TestNewUserServiceSharesActivityTracker(t *testing.T) {
	user := &identity.User{ID: 77}
	repo := &mockUserRepo{getByIDUser: user}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)
	svc.TouchLastActiveForUser(context.Background(), identity.CopyUser(user))
	svc.TouchLastActiveForUser(context.Background(), user)
	require.Equal(t, []int64{77}, repo.updateLastActiveUserIDs)
}

func TestUpdateProfile_RejectsEmailChange(t *testing.T) {
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:       7,
			Email:    "current@example.com",
			Username: "current-user",
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)
	newEmail := "new@example.com"

	_, err := svc.UpdateProfile(context.Background(), 7, identity.UpdateProfileRequest{
		Email: &newEmail,
	})

	require.ErrorIs(t, err, identity.ErrProfileEmailChangeForbidden)
	require.Zero(t, repo.updateCalls)
	require.Equal(t, "current@example.com", repo.getByIDUser.Email)
}

func TestUpdateProfile_StoresInlineAvatarWithinLimit(t *testing.T) {
	raw := []byte("small-avatar")
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
	expectedSum := sha256.Sum256(raw)
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:       7,
			Email:    "avatar@example.com",
			Username: "avatar-user",
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	updated, err := svc.UpdateProfile(context.Background(), 7, identity.UpdateProfileRequest{
		AvatarURL: &dataURL,
	})
	require.NoError(t, err)
	require.Len(t, repo.upsertAvatarArgs, 1)
	require.Equal(t, "inline", repo.upsertAvatarArgs[0].StorageProvider)
	require.Equal(t, "image/png", repo.upsertAvatarArgs[0].ContentType)
	require.Equal(t, len(raw), repo.upsertAvatarArgs[0].ByteSize)
	require.Equal(t, hex.EncodeToString(expectedSum[:]), repo.upsertAvatarArgs[0].SHA256)
	require.Equal(t, dataURL, updated.AvatarURL)
	require.Equal(t, "inline", updated.AvatarSource)
	require.Equal(t, "image/png", updated.AvatarMIME)
	require.Equal(t, len(raw), updated.AvatarByteSize)
	require.Equal(t, hex.EncodeToString(expectedSum[:]), updated.AvatarSHA256)
}

func TestUpdateProfile_CompressesInlineAvatarToTwentyKilobytes(t *testing.T) {
	var encoded bytes.Buffer
	for _, size := range []int{192, 224, 256, 288} {
		encoded.Reset()
		var img image.RGBA
		img.Rect = image.Rect(0, 0, size, size)
		img.Stride = size * 4
		img.Pix = make([]byte, size*size*4)
		for y := range size {
			for x := range size {
				offset := y*img.Stride + x*4
				img.Pix[offset] = uint8((x*x + y*17) % 255)
				img.Pix[offset+1] = uint8((y*y + x*29) % 255)
				img.Pix[offset+2] = uint8(((x * y) + x*13 + y*7) % 255)
				img.Pix[offset+3] = 0xff
			}
		}
		require.NoError(t, png.Encode(&encoded, &img))
		if encoded.Len() > 20*1024 && encoded.Len() <= identity.ProfileMaxInlineAvatarBytes {
			break
		}
	}
	require.Greater(t, encoded.Len(), 20*1024)
	require.LessOrEqual(t, encoded.Len(), identity.ProfileMaxInlineAvatarBytes)

	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes())
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:       17,
			Email:    "avatar-compress@example.com",
			Username: "avatar-compress",
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	updated, err := svc.UpdateProfile(context.Background(), 17, identity.UpdateProfileRequest{
		AvatarURL: &dataURL,
	})
	require.NoError(t, err)
	require.Len(t, repo.upsertAvatarArgs, 1)
	require.Equal(t, "inline", repo.upsertAvatarArgs[0].StorageProvider)
	require.LessOrEqual(t, repo.upsertAvatarArgs[0].ByteSize, 20*1024)
	require.Equal(t, "image/jpeg", repo.upsertAvatarArgs[0].ContentType)
	require.Contains(t, repo.upsertAvatarArgs[0].URL, "data:image/jpeg;base64,")
	require.Equal(t, "inline", updated.AvatarSource)
	require.Equal(t, "image/jpeg", updated.AvatarMIME)
	require.LessOrEqual(t, updated.AvatarByteSize, 20*1024)
	require.Contains(t, updated.AvatarURL, "data:image/jpeg;base64,")
	require.NotEmpty(t, updated.AvatarSHA256)
}

func TestUpdateProfile_RejectsInlineAvatarOverLimit(t *testing.T) {
	raw := make([]byte, identity.ProfileMaxInlineAvatarBytes+1)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:       8,
			Email:    "large-avatar@example.com",
			Username: "too-large",
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	_, err := svc.UpdateProfile(context.Background(), 8, identity.UpdateProfileRequest{
		AvatarURL: &dataURL,
	})
	require.ErrorIs(t, err, identity.ErrAvatarTooLarge)
	require.Empty(t, repo.upsertAvatarArgs)
	require.Empty(t, repo.deleteAvatarIDs)
	require.Zero(t, repo.updateCalls)
}

func TestUpdateProfile_StoresRemoteAvatarURL(t *testing.T) {
	remoteURL := "https://cdn.example.com/avatar.png"
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:       9,
			Email:    "remote-avatar@example.com",
			Username: "remote-avatar",
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	updated, err := svc.UpdateProfile(context.Background(), 9, identity.UpdateProfileRequest{
		AvatarURL: &remoteURL,
	})
	require.NoError(t, err)
	require.Len(t, repo.upsertAvatarArgs, 1)
	require.Equal(t, "remote_url", repo.upsertAvatarArgs[0].StorageProvider)
	require.Equal(t, remoteURL, repo.upsertAvatarArgs[0].URL)
	require.Equal(t, remoteURL, updated.AvatarURL)
	require.Equal(t, "remote_url", updated.AvatarSource)
	require.Zero(t, updated.AvatarByteSize)
}

func TestUpdateProfile_DeletesAvatarOnEmptyString(t *testing.T) {
	empty := ""
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:           10,
			Email:        "delete-avatar@example.com",
			Username:     "delete-avatar",
			AvatarURL:    "https://cdn.example.com/old.png",
			AvatarSource: "remote_url",
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	updated, err := svc.UpdateProfile(context.Background(), 10, identity.UpdateProfileRequest{
		AvatarURL: &empty,
	})
	require.NoError(t, err)
	require.Equal(t, []int64{10}, repo.deleteAvatarIDs)
	require.Empty(t, repo.upsertAvatarArgs)
	require.Empty(t, updated.AvatarURL)
	require.Empty(t, updated.AvatarSource)
}

func TestUpdateProfile_RollsBackAvatarMutationWhenUserUpdateFails(t *testing.T) {
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:           11,
			Email:        "rollback@example.com",
			AvatarURL:    "https://cdn.example.com/original.png",
			AvatarSource: "remote_url",
		},
		updateFn: func(context.Context, *identity.User) error {
			return errors.New("write user failed")
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	remoteURL := "https://cdn.example.com/new.png"
	_, err := svc.UpdateProfile(context.Background(), 11, identity.UpdateProfileRequest{
		AvatarURL: &remoteURL,
	})

	require.EqualError(t, err, "update user: write user failed")
	require.Equal(t, 1, repo.txCalls)
	require.Empty(t, repo.upsertAvatarArgs)
	require.Empty(t, repo.deleteAvatarIDs)
	require.Equal(t, "https://cdn.example.com/original.png", repo.getByIDUser.AvatarURL)
	require.Equal(t, "remote_url", repo.getByIDUser.AvatarSource)
}

func TestGetProfile_HydratesAvatarFromRepository(t *testing.T) {
	repo := &mockUserRepo{
		getByIDUser: &identity.User{
			ID:       12,
			Email:    "profile-avatar@example.com",
			Username: "profile-avatar",
		},
		getAvatarFn: func(context.Context, int64) (*identity.UserAvatar, error) {
			return &identity.UserAvatar{
				StorageProvider: "remote_url",
				URL:             "https://cdn.example.com/profile.png",
			}, nil
		},
	}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	user, err := svc.GetProfile(context.Background(), 12)
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example.com/profile.png", user.AvatarURL)
	require.Equal(t, "remote_url", user.AvatarSource)
}

func TestUpdateProfile_OnlyDeclaresRequestedColumns(t *testing.T) {
	username := "renamed"
	tests := []struct {
		name string
		req  identity.UpdateProfileRequest
		want identity.UserUpdateFields
	}{
		{
			name: "username only",
			req:  identity.UpdateProfileRequest{Username: &username},
			want: identity.UserUpdateFields{Username: true},
		},
		{
			name: "notify settings only",
			req:  identity.UpdateProfileRequest{BalanceNotifyEnabled: boolPtr(true)},
			want: identity.UserUpdateFields{BalanceNotifySettings: true},
		},
		{
			name: "username and notify threshold",
			req:  identity.UpdateProfileRequest{Username: &username, BalanceNotifyThreshold: float64Ptr(1.5)},
			want: identity.UserUpdateFields{Username: true, BalanceNotifySettings: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockUserRepo{getByIDUser: &identity.User{ID: 7, Balance: 0.30, Status: identity.StatusActive}}
			svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

			_, err := svc.UpdateProfile(context.Background(), 7, tt.req)
			require.NoError(t, err)
			require.Equal(t, []identity.UserUpdateFields{tt.want}, repo.updateFields)
		})
	}
}

// TestUpdateProfile_AvatarOnlySkipsUserRowWrite checks that avatar changes select no user columns.
func TestUpdateProfile_AvatarOnlySkipsUserRowWrite(t *testing.T) {
	repo := &mockUserRepo{getByIDUser: &identity.User{ID: 7, Balance: 0.30}}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	avatar := "https://cdn.example.com/a.png"
	_, err := svc.UpdateProfile(context.Background(), 7, identity.UpdateProfileRequest{AvatarURL: &avatar})
	require.NoError(t, err)
	require.Len(t, repo.upsertAvatarArgs, 1, "avatar must still be stored")
	require.Equal(t, []identity.UserUpdateFields{{}}, repo.updateFields, "no user column should be declared")
}

func TestChangePassword_OnlyDeclaresPasswordHash(t *testing.T) {
	user := &identity.User{ID: 7, Balance: 0.30}
	require.NoError(t, user.SetPassword("old-password"))
	repo := &mockUserRepo{getByIDUser: user}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	err := svc.ChangePassword(context.Background(), 7, identity.ChangePasswordRequest{
		CurrentPassword: "old-password",
		NewPassword:     "new-password",
	})
	require.NoError(t, err)
	require.Equal(t, []identity.UserUpdateFields{{PasswordHash: true}}, repo.updateFields)
}

func TestUpdateStatus_OnlyDeclaresStatus(t *testing.T) {
	repo := &mockUserRepo{getByIDUser: &identity.User{ID: 7, Balance: 0.30, Status: identity.StatusActive}}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	require.NoError(t, svc.UpdateStatus(context.Background(), 7, "disabled"))
	require.Equal(t, []identity.UserUpdateFields{{Status: true}}, repo.updateFields)
}

// TestUpdateProfileLanguagePreference covers normalization, clearing, and independent field updates.
func TestUpdateProfileLanguagePreference(t *testing.T) {
	en, zh, invalid := "en", "zh", "unsupported"
	for _, tc := range []struct {
		name      string
		input     identity.UpdateProfileRequest
		expected  *string
		wantError bool
	}{
		{name: "alias", input: identity.UpdateProfileRequest{PreferredLocale: &zh}, expected: func() *string { value := "zh-Hans"; return &value }()},
		{name: "clear", input: identity.UpdateProfileRequest{ClearPreferredLocale: true}},
		{name: "omitted", input: identity.UpdateProfileRequest{}, expected: &en},
		{name: "invalid", input: identity.UpdateProfileRequest{PreferredLocale: &invalid}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &mockUserRepo{getByIDUser: &identity.User{ID: 7, PreferredLocale: &en, Status: identity.StatusActive}}
			service := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)
			updated, err := service.UpdateProfile(context.Background(), 7, tc.input)
			if tc.wantError {
				require.Error(t, err)
				require.Empty(t, repo.updateFields)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expected, updated.PreferredLocale)
			changed := tc.input.PreferredLocale != nil || tc.input.ClearPreferredLocale
			require.Equal(t, []identity.UserUpdateFields{{PreferredLocale: changed}}, repo.updateFields)
		})
	}
}
