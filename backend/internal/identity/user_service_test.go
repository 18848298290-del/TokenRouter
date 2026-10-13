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

// Profile updates declare account columns separately from avatar persistence,
// and verified email binding remains a separate path. Tests inspect selected
// masks and prepared avatar metadata, using a staged fixture callback to
// reproduce rollback of its account pointer and avatar histories.
//
// That fixture copies outer account state and value-only avatar records,
// while counters, injected callbacks and nested account references stay
// outside staging. Cache fixtures distinguish synchronous authentication
// invalidation from submitted background balance cleanup: atomic attempt
// count can advance before its locked ID history is appended. Tests
// coordinate shared observations and limit failure claims to their
// represented callbacks and intervals; production PostgreSQL transaction
// participation requires its own evidence.

// mockUserRepo stages selected profile fields and records service inputs for
// sequential user tests.
type mockUserRepo struct {
	// updateBalanceErr is returned when updateBalanceFn is absent. Tests
	// configure it independently of account state to select the repository
	// failure path.
	updateBalanceErr error
	// updateBalanceFn overrides the balance update result when present. It
	// receives the original request context, identity and amount, and supersedes
	// updateBalanceErr.
	updateBalanceFn func(ctx context.Context, id int64, amount float64) error
	// deductBalanceFn supplies an error-only deduction callback when no result
	// callback is configured. Its returned amount is zero regardless of
	// submitted amount.
	deductBalanceFn func(ctx context.Context, id int64, amount float64) error
	// deductBalanceResultFn takes precedence over the error-only callback. It
	// returns the selected amount/error directly and owns any fixture mutation
	// performed by the test.
	deductBalanceResultFn func(ctx context.Context, id int64, amount float64) (float64, error)
	// getByIDUser retains the base fixture account pointer. Transaction
	// callbacks use a staged outer copy, whose nested account references remain
	// shared.
	getByIDUser *identity.User
	// getByIDErr wins before base/staged account selection. It can force read or
	// readback failure even when a current fixture account exists.
	getByIDErr error
	// identities retains configured identity records. Listing copies its outer
	// slice, while metadata maps and pointer fields remain shared; unbind
	// filters and republishes the outer records.
	identities []identity.UserAuthIdentityRecord
	// unbindIdentityErr prevents provider history and identity filtering before
	// they change. Nil allows removal to record and publish remaining fixture
	// identities.
	unbindIdentityErr error
	unboundProviders  []string // recorded successfully removed provider labels
	// updateLastActiveErr, updateLastActiveUserIDs and updateLastActiveAt
	// describe one attempted activity write. The error returns before recording;
	// successful calls append matching identities and times in order during
	// sequential tests.
	updateLastActiveErr     error       // configured activity write failure
	updateLastActiveUserIDs []int64     // recorded activity recipient identities
	updateLastActiveAt      []time.Time // recorded activity observation timestamps
	// updateFn overrides default account publication after update count and mask
	// recording. Its injected effects/errors are outside the profile
	// transaction's staged fields.
	//
	// updateFn, updateCalls and updateFields describe account-update
	// observation. Count/mask recording happens before an override callback and
	// stays outside WithUserProfileIdentityTx staging, including after a
	// callback failure.
	updateFn     func(ctx context.Context, user *identity.User) error
	updateCalls  int                         // observed account update count
	updateFields []identity.UserUpdateFields // recorded account column selection masks
	// upsertAvatarFn overrides avatar writes before default history/account
	// mutation. A configured callback receives the supplied context/metadata and
	// owns its result and side effects.
	upsertAvatarFn func(ctx context.Context, userID int64, input identity.UpsertUserAvatarInput) (*identity.UserAvatar, error)
	// upsertAvatarArgs retains value-only avatar metadata inputs for default
	// writes. Transaction callbacks append to copied histories and publish them
	// only after success.
	upsertAvatarArgs []identity.UpsertUserAvatarInput
	// deleteAvatarFn overrides deletion before default history/account mutation.
	// The callback receives the selected context and identity and owns its
	// errors/effects.
	deleteAvatarFn func(ctx context.Context, userID int64) error
	// deleteAvatarIDs records default avatar deletion targets. Transaction
	// callbacks use a copied history, while ordinary calls append directly to
	// base fixture state.
	deleteAvatarIDs []int64
	// getAvatarFn supplies avatar read results when present. Nil makes the
	// fixture return absent metadata with nil error for profile hydration.
	getAvatarFn func(ctx context.Context, userID int64) (*identity.UserAvatar, error)
	txCalls     int // observed profile callback transaction count
}

// mockUserRepoTxKey identifies the staged profile transaction value stored in
// callback contexts.
type mockUserRepoTxKey struct{}

// mockUserRepoTxState holds the account pointer and avatar histories staged
// for one fixture callback.
type mockUserRepoTxState struct {
	// getByIDUser, upsertAvatarArgs and deleteAvatarIDs form the fixture's
	// staged profile state. The account is copied shallowly and avatar histories
	// copy their value records; successful callbacks publish all three. Failure
	// retains the base account pointer and avatar histories, while nested account
	// data remains shared outside staging.
	getByIDUser      *identity.User                   // staged outer account copy
	upsertAvatarArgs []identity.UpsertUserAvatarInput // staged avatar metadata history
	deleteAvatarIDs  []int64                          // staged avatar deletion identities
}

// mockUserSettingRepo retains setting strings for sequential profile provider
// and locale tests.
type mockUserSettingRepo struct {
	// values retains supplied settings-map storage. Read methods return copied
	// strings/maps, while writes merge into this unsynchronized map during
	// sequential fixture use.
	values map[string]string
}

// mockAuthCacheInvalidator records account-level authentication invalidation
// requests under a fixture mutex.
type mockAuthCacheInvalidator struct {
	// invalidatedUserIDs and mu coordinate shared account-invalidation
	// observations. Writers append under mu; tests take the same mutex while
	// inspecting the retained history after service calls.
	invalidatedUserIDs []int64    // recorded authentication invalidation identities
	mu                 sync.Mutex // authentication observation history lock
}

// mockBillingCache counts cleanup attempts and records account IDs for
// asynchronous profile funding tests.
type mockBillingCache struct {
	// invalidateErr, invalidateCallCount, invalidatedUserIDs and mu describe
	// cleanup attempts. Setup fixes the error before concurrent work; the atomic
	// counter increments before the locked ID append and error return, so its
	// observation alone is not completion.
	invalidateErr       error        // configured cache cleanup failure
	invalidateCallCount atomic.Int64 // observed atomic cleanup attempt count
	invalidatedUserIDs  []int64      // recorded billing invalidation identities
	mu                  sync.Mutex   // billing observation history lock
}

// TestUserService_UpdateProfile_RejectsEmailWhenNormalizationEnabled submits
// email and username together with normalization enabled. Profile email
// rejection occurs before conflict/guarded/update calls and leaves both
// retained account fields unchanged.
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

// TestUserService_UpdateProfile_RejectsEmailWhenNormalizationDisabled submits
// a duplicate email while normalization is disabled. Profile email rejection
// still precedes conflict lookup and update recording, keeping verified
// binding as the required email-change path.
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

// TestUpdateProfile_RejectsEmailBeforeEmailIdentityResync supplies
// email-change input and a failing identity replacement capability. The
// profile-specific rejection occurs first, leaving account updates and
// ensure/replace call histories empty.
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

// runProfileBackground starts the supplied task in a new goroutine and
// reports acceptance. Tests observe attempts through counters/history; this
// runner supplies no completion handle and returns before the task finishes.
func runProfileBackground(_ string, task func()) bool {
	// Submit cleanup asynchronously and return admission immediately. Attempt
	// counters and recorded history supply separate observations; neither this
	// runner nor its return value waits for task completion.
	go task()
	return true
}

// boolPtr returns a pointer to its copied boolean input for optional request
// values.
func boolPtr(value bool) *bool { return &value }

// float64Ptr returns a pointer to its copied amount input for optional
// request values.
func float64Ptr(value float64) *float64 { return &value }

// mockUserRepoStateFromContext reads an optional fixture transaction state
// from a usable context. An absent or differently typed value returns nil
// after discarding the assertion flag; returned state is retained by its
// caller.
func mockUserRepoStateFromContext(ctx context.Context) *mockUserRepoTxState {
	// Treat absent or differently typed context data as no fixture transaction,
	// selecting base account state while keeping actual staged pointers shared
	// for matching callback contexts.
	state, _ := ctx.Value(mockUserRepoTxKey{}).(*mockUserRepoTxState)
	return state
}

// currentUser selects the context-staged account when fixture transaction
// state is present. Otherwise it returns the retained base account pointer;
// both successful selections share the chosen account with its owner.
func (m *mockUserRepo) currentUser(ctx context.Context) *identity.User {
	if state := mockUserRepoStateFromContext(ctx); state != nil {
		return state.getByIDUser
	}
	return m.getByIDUser
}

// setCurrentUser replaces the context-staged account when fixture transaction
// state is present. Otherwise it replaces the retained base pointer; callers
// use this fixture sequentially and keep supplied account references stable.
func (m *mockUserRepo) setCurrentUser(ctx context.Context, user *identity.User) {
	if state := mockUserRepoStateFromContext(ctx); state != nil {
		state.getByIDUser = user
		return
	}
	m.getByIDUser = user
}

// Create accepts account creation as a successful profile-fixture no-op.
func (m *mockUserRepo) Create(context.Context, *identity.User) error { return nil }

// CreateWithNormalizedEmailGuard delegates to the successful Create no-op
// with the submitted account. Normalized text is ignored, so this fixture
// supplies the repository shape without creating rows or applying alias
// locks.
func (m *mockUserRepo) CreateWithNormalizedEmailGuard(ctx context.Context, user *identity.User, _ string) error {
	return m.Create(ctx, user)
}

// GetByID returns its configured read error before selecting current fixture
// state. Missing state produces a fresh ID-only account; present state
// returns an outer copy whose reference fields remain shared.
func (m *mockUserRepo) GetByID(ctx context.Context, id int64) (*identity.User, error) {
	if m.getByIDErr != nil {
		return nil, m.getByIDErr
	}
	user := m.currentUser(ctx)
	// Absent configured account state produces an ID-only fixture value, letting
	// generic profile tests exercise downstream calls without constructing
	// unrelated account data.
	if user == nil {
		return &identity.User{ID: id}, nil
	}
	cloned := *user
	return &cloned, nil
}

// GetByEmail returns a fresh zero account for every profile-fixture email
// lookup.
func (m *mockUserRepo) GetByEmail(context.Context, string) (*identity.User, error) {
	return &identity.User{}, nil
}

// GetFirstAdmin returns a fresh zero account for every fixture administrator
// lookup.
func (m *mockUserRepo) GetFirstAdmin(context.Context) (*identity.User, error) {
	return &identity.User{}, nil
}

// Update records count and mask before selecting an optional update callback.
// A callback owns its outcome and can bypass default publication; otherwise
// an outer account copy replaces context-staged or base fixture state.
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

// UpdateWithNormalizedEmailGuard delegates to Update with the original
// account and field mask. It ignores normalized text; this fixture records
// selected columns without enforcing mailbox uniqueness or performing
// database locking.
func (m *mockUserRepo) UpdateWithNormalizedEmailGuard(ctx context.Context, user *identity.User, _ string, fields identity.UserUpdateFields) error {
	return m.Update(ctx, user, fields)
}

// Delete accepts account deletion as a successful profile-fixture no-op.
func (m *mockUserRepo) Delete(context.Context, int64) error { return nil }

// GetUserAvatar delegates to the configured avatar read callback when
// present. Otherwise it returns nil with nil error, allowing profile
// hydration to clear the account's avatar display fields.
func (m *mockUserRepo) GetUserAvatar(ctx context.Context, userID int64) (*identity.UserAvatar, error) {
	if m.getAvatarFn != nil {
		return m.getAvatarFn(ctx, userID)
	}
	return nil, nil
}

// UpsertUserAvatar delegates to a configured callback or records avatar
// metadata in the selected fixture state. The default path appends the value
// input and mirrors representation fields into an outer copy of its current
// account.
//
// A context transaction keeps the avatar history and account pointer staged,
// while an ordinary call changes base fixture state directly. Metadata input
// fields are values, and a returned avatar record is freshly constructed.
// Callback overrides own their mutations/error and bypass this default
// recording; selected current account reference fields remain shared after
// the outer copy.
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

// DeleteUserAvatar delegates to a configured callback or records deletion in
// selected fixture state. The default path appends the requested ID and
// clears avatar display fields in an outer account copy.
//
// Context-staged calls change the transaction's history/account pointer,
// while ordinary calls change base fixture state. Callback overrides bypass
// those default mutations and own their errors. Account maps, slices and
// pointers outside cleared avatar values remain shared through the outer
// copy, so fixture rollback only controls its staged records and account
// pointer publication.
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

// List returns absent accounts and pagination with success for
// profile-fixture listing.
func (m *mockUserRepo) List(context.Context, pagination.PaginationParams) ([]identity.User, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

// ListWithFilters returns absent accounts and pagination with success for
// filtered fixture listing.
func (m *mockUserRepo) ListWithFilters(context.Context, pagination.PaginationParams, identity.UserListFilters) ([]identity.User, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

// UpdateBalance delegates to updateBalanceFn when configured, otherwise
// returns updateBalanceErr. No balance value is mutated by its default path;
// callers select repository success/failure independently from cache
// invalidation observations.
func (m *mockUserRepo) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	if m.updateBalanceFn != nil {
		return m.updateBalanceFn(ctx, id, amount)
	}
	return m.updateBalanceErr
}

// AddBalance accepts balance additions without mutating the profile fixture
// account.
func (m *mockUserRepo) AddBalance(context.Context, int64, float64) error { return nil }

// DeductBalance selects result-returning callback, error-only callback, then
// amount-as-result success in that order. The error-only path returns zero
// result, and the fixture default performs no balance deduction or
// remaining-funds calculation.
func (m *mockUserRepo) DeductBalance(ctx context.Context, id int64, amount float64) (float64, error) {
	// Select the amount-producing callback before the error-only override;
	// absent callbacks retain the submitted amount as a fixed fixture result
	// without changing any account funds.
	if m.deductBalanceResultFn != nil {
		return m.deductBalanceResultFn(ctx, id, amount)
	}
	if m.deductBalanceFn != nil {
		return 0, m.deductBalanceFn(ctx, id, amount)
	}
	return amount, nil
}

// AdjustBalance panics when signed funding adjustment reaches this profile
// fixture.
func (m *mockUserRepo) AdjustBalance(ctx context.Context, id int64, delta float64) (identity.BalanceChange, error) {
	panic("unexpected AdjustBalance call")
}

// SetBalance panics when balance replacement reaches this profile fixture.
func (m *mockUserRepo) SetBalance(ctx context.Context, id int64, value float64) (identity.BalanceChange, error) {
	panic("unexpected SetBalance call")
}

// UpdateConcurrency accepts concurrency updates without changing the profile
// fixture account.
func (m *mockUserRepo) UpdateConcurrency(context.Context, int64, int) error { return nil }

// BatchSetConcurrency returns zero affected accounts for fixture concurrency
// replacement requests.
func (m *mockUserRepo) BatchSetConcurrency(context.Context, []int64, int) (int, error) {
	return 0, nil
}

// BatchAddConcurrency returns zero affected accounts for fixture concurrency
// increment requests.
func (m *mockUserRepo) BatchAddConcurrency(context.Context, []int64, int) (int, error) {
	return 0, nil
}

// ExistsByEmail reports no exact mailbox conflict for the profile fixture.
func (m *mockUserRepo) ExistsByEmail(context.Context, string) (bool, error) { return false, nil }

// ExistsByNormalizedEmail reports no alias mailbox conflict for the profile
// fixture.
func (m *mockUserRepo) ExistsByNormalizedEmail(context.Context, string) (bool, error) {
	return false, nil
}

// LockRegistrationEmail accepts mailbox locking as a successful
// profile-fixture no-op.
func (m *mockUserRepo) LockRegistrationEmail(context.Context, string) error { return nil }

// RemoveGroupFromAllowedGroups returns zero affected accounts for fixture
// group removal requests.
func (m *mockUserRepo) RemoveGroupFromAllowedGroups(context.Context, int64) (int64, error) {
	return 0, nil
}

// BatchUpdateLimits returns zero affected accounts for fixture account-limit
// update requests.
func (m *mockUserRepo) BatchUpdateLimits(context.Context, []int64, *int, *int) (int, error) {
	return 0, nil
}

// AddGroupToAllowedGroups accepts group addition without changing the profile
// fixture account.
func (m *mockUserRepo) AddGroupToAllowedGroups(context.Context, int64, int64) error { return nil }

// ListUserAuthIdentities returns a fresh outer copy of the configured
// identity slice. Identity metadata maps and pointer fields remain shared
// with fixture records, so callers keep them stable during summary checks.
func (m *mockUserRepo) ListUserAuthIdentities(context.Context, int64) ([]identity.UserAuthIdentityRecord, error) {
	out := make([]identity.UserAuthIdentityRecord, len(m.identities))
	copy(out, m.identities)
	return out, nil
}

// GetLatestUsedAtByUserIDs returns a fresh empty map for profile-fixture
// usage-time reads.
func (m *mockUserRepo) GetLatestUsedAtByUserIDs(context.Context, []int64) (map[int64]*time.Time, error) {
	return map[int64]*time.Time{}, nil
}

// GetLatestUsedAtByUserID returns an absent usage timestamp with no
// profile-fixture error.
func (m *mockUserRepo) GetLatestUsedAtByUserID(context.Context, int64) (*time.Time, error) {
	return nil, nil
}

// UpdateUserLastActiveAt returns its configured error before recording a
// timestamp write. Success appends account ID and time in matching order;
// this sequential fixture changes no account timestamp field.
func (m *mockUserRepo) UpdateUserLastActiveAt(_ context.Context, userID int64, activeAt time.Time) error {
	if m.updateLastActiveErr != nil {
		return m.updateLastActiveErr
	}
	m.updateLastActiveUserIDs = append(m.updateLastActiveUserIDs, userID)
	m.updateLastActiveAt = append(m.updateLastActiveAt, activeAt)
	return nil
}

// UpdateTotpSecret accepts TOTP secret writes without changing the profile
// fixture account.
func (m *mockUserRepo) UpdateTotpSecret(context.Context, int64, *string) error { return nil }

// EnableTotp accepts TOTP enablement without changing the profile fixture
// account.
func (m *mockUserRepo) EnableTotp(context.Context, int64) error { return nil }

// DisableTotp accepts TOTP disablement without changing the profile fixture
// account.
func (m *mockUserRepo) DisableTotp(context.Context, int64) error { return nil }

// RemoveGroupFromUserAllowedGroups accepts group removal without changing the
// profile fixture account.
func (m *mockUserRepo) RemoveGroupFromUserAllowedGroups(context.Context, int64, int64) error {
	return nil
}

// UnbindUserAuthProvider returns its configured error before recording
// provider removal. Success filters matching provider records through
// existing slice storage, then publishes a fresh outer slice; retained
// identity metadata/pointers remain shared.
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

// GetByIDIncludeDeleted delegates to GetByID with its configured error,
// shallow copy and missing-state account. The fixture holds no deletion
// filter, so include-deleted lookup selects the same current staged/base
// state.
func (m *mockUserRepo) GetByIDIncludeDeleted(ctx context.Context, id int64) (*identity.User, error) {
	return m.GetByID(ctx, id)
}

// WithUserProfileIdentityTx executes the callback with a staged outer account
// copy and copied avatar histories, publishing those fields to base fixture
// state only after callback success. Failure retains the base account pointer
// and avatar histories.
//
// Call counters, update masks, injected callback effects and nested account
// references remain outside this staging. The helper initializes from base
// state even when its input context carries another fixture state; callers
// use sequential top-level transactions for these tests. It models the
// represented profile rollback observation, not PostgreSQL store
// participation or independent copies of every account field.
func (m *mockUserRepo) WithUserProfileIdentityTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	m.txCalls++
	// Stage an outer account copy and separate avatar histories before the
	// callback, so its default mutations can be discarded together when a later
	// account write fails.
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
	// Publish the three staged fields only after callback success; call
	// counters, mask records, callback effects and shared nested account data
	// remain outside this publication decision.
	m.getByIDUser = txState.getByIDUser
	m.upsertAvatarArgs = txState.upsertAvatarArgs
	m.deleteAvatarIDs = txState.deleteAvatarIDs
	return nil
}

// GetValue returns a configured string from an optional receiver/map. Missing
// state or key returns a fresh setting-not-found error, allowing profile
// provider/default readers to exercise absence without a settings sentinel.
func (s *mockUserSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if s != nil && s.values != nil {
		if value, ok := s.values[key]; ok {
			return value, nil
		}
	}
	return "", errors.New("setting not found")
}

// Set merges one setting into the retained map, allocating it when absent.
// The receiver is required and callers use this fixture sequentially; input
// text is stored unchanged.
func (s *mockUserSettingRepo) Set(_ context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

// GetMultiple returns a fresh subset map for requested keys. Nil receiver or
// absent map produces an allocated empty result with nil error; configured
// strings remain unchanged and setup stays sequential.
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

// SetMultiple delegates each submitted map entry to Set in iteration order.
// It returns the first delegated error and provides no rollback; callers use
// this fixture sequentially while its retained map changes.
func (s *mockUserSettingRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	for key, value := range settings {
		if err := s.Set(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

// GetAll returns a fresh copy of configured setting strings. Nil receiver/map
// yields an allocated empty result; callers can modify the returned map
// independently of fixture state.
func (s *mockUserSettingRepo) GetAll(context.Context) (map[string]string, error) {
	out := map[string]string{}
	if s == nil || s.values == nil {
		return out, nil
	}
	maps.Copy(out, s.values)
	return out, nil
}

// Delete removes one key from the retained setting map with fixture success.
// A receiver is required and deletion on nil map is harmless; callers
// coordinate this unsynchronized fixture themselves.
func (s *mockUserSettingRepo) Delete(_ context.Context, key string) error {
	delete(s.values, key)
	return nil
}

// InvalidateAuthCacheByKey accepts individual credential invalidation without
// recording profile-fixture state.
func (m *mockAuthCacheInvalidator) InvalidateAuthCacheByKey(context.Context, string) {}

// InvalidateAuthCacheByGroupID accepts group authentication invalidation
// without recording profile-fixture state.
func (m *mockAuthCacheInvalidator) InvalidateAuthCacheByGroupID(context.Context, int64) {}

// InvalidateAuthCacheByUserID records the requested account ID under the
// fixture mutex. Callers take that mutex for shared-history assertions and
// complete direct fixture setup before concurrent cache callback attempts.
func (m *mockAuthCacheInvalidator) InvalidateAuthCacheByUserID(_ context.Context, userID int64) {
	// Serialize account-history append with test inspection, retaining each
	// observed invalidation request even when separate billing-cache callbacks
	// can run asynchronously.
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidatedUserIDs = append(m.invalidatedUserIDs, userID)
}

// GetUserBalance returns zero cached funds with success for every fixture
// account.
func (m *mockBillingCache) GetUserBalance(context.Context, int64) (float64, error) { return 0, nil }

// SetUserBalance accepts cached balance writes without retaining
// profile-fixture funds.
func (m *mockBillingCache) SetUserBalance(context.Context, int64, float64) error { return nil }

// DeductUserBalance accepts cached balance deductions without retaining
// profile-fixture funds.
func (m *mockBillingCache) DeductUserBalance(context.Context, int64, float64) error { return nil }

// InvalidateUserBalance increments its atomic attempt count before recording
// the account ID under a mutex. The configured error is returned after
// recording, so failure tests can still observe that cleanup was attempted.
func (m *mockBillingCache) InvalidateUserBalance(_ context.Context, userID int64) error {
	// Count the attempted cleanup before locking and appending its account
	// identity, allowing tests to observe admission separately from completion
	// of recorded history and error return.
	m.invalidateCallCount.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidatedUserIDs = append(m.invalidatedUserIDs, userID)
	return m.invalidateErr
}

// TestUpdateBalance_Success accepts a repository balance update and uses the
// asynchronous cache runner. It waits for one invalidation attempt and reads
// the recorded account ID under the cache fixture mutex.
func TestUpdateBalance_Success(t *testing.T) {
	repo := &mockUserRepo{}
	cache := &mockBillingCache{}
	svc := identity.NewUserService(repo, nil, nil, cache, runProfileBackground)

	err := svc.UpdateBalance(context.Background(), 42, 100.0)
	require.NoError(t, err)

	// The attempt counter advances before history append. Locking makes the
	// read race-free but can precede the writer's lock; this wait does not prove
	// that the ID required by the following assertion has already been recorded.
	require.Eventually(t, func() bool {
		return cache.invalidateCallCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond, "应异步调用 InvalidateUserBalance")

	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Equal(t, []int64{42}, cache.invalidatedUserIDs, "应对 userID=42 失效缓存")
}

// TestGetProfileIdentitySummaries_AllowsUnbindWhenAnotherLoginMethodRemains
// represents an email identity beside LinuxDo. The profile summary permits
// LinuxDo unbinding, shows its recorded display name and returns a nonempty
// subject hint.
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

// TestUnbindUserAuthProviderRejectsLastRemainingLoginMethod represents only
// LinuxDo with a reserved compatibility mailbox. Unbinding returns the
// last-method restriction and the repository records no provider removal
// attempt.
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

// TestGetProfileIdentitySummaries_DoesNotTreatOAuthOnlyCompatEmailAsAlternativeLoginMethod
// represents an OIDC-only account with an ordinary-looking compatibility
// mailbox. Summary and unbind results still block removing its only login
// identity, with no recorded removal.
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

// TestGetProfileIdentitySummaries_DoesNotTreatCompatBackfilledEmailIdentityAsAlternativeLoginMethod
// adds a migration-backfilled email record to a WeChat-only account. Email
// appears bound, but summary and unbind results keep WeChat protected as its
// usable login method.
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

// TestUnbindUserAuthProviderRemovesProviderAndReturnsUpdatedProfile
// represents a usable email identity beside LinuxDo. Unbind removes the
// provider, returns the account and records authentication invalidation; the
// refreshed summary exposes LinuxDo binding again.
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

// TestGetProfileIdentitySummaries_HidesBindActionWhenProviderExplicitlyDisabled
// stores an explicit false LinuxDo switch for an unbound account. The summary
// keeps Bound/CanBind false and returns no bind-start path for that provider.
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

// TestGetProfileIdentitySummaries_UsesBindStartRoute requests summaries for
// an email-authenticated account with provider switches omitted. LinuxDo,
// OIDC and WeChat bind links include current-user intent and the encoded
// profile return destination.
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

// TestUpdateBalance_NilBillingCache_NoPanic accepts a balance update with no
// billing-cache capability. The service returns success, protecting optional
// cache cleanup after the represented repository operation.
func TestUpdateBalance_NilBillingCache_NoPanic(t *testing.T) {
	repo := &mockUserRepo{}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	err := svc.UpdateBalance(context.Background(), 1, 50.0)
	require.NoError(t, err, "billingCache 为 nil 时不应 panic")
}

// TestUpdateBalance_CacheFailure_DoesNotAffectReturn makes background
// balance-cache invalidation fail after an accepted repository update. The
// service still returns success and the test observes one attempted cleanup
// call.
func TestUpdateBalance_CacheFailure_DoesNotAffectReturn(t *testing.T) {
	repo := &mockUserRepo{}
	cache := &mockBillingCache{invalidateErr: errors.New("redis connection refused")}
	svc := identity.NewUserService(repo, nil, nil, cache, runProfileBackground)

	err := svc.UpdateBalance(context.Background(), 99, 200.0)
	require.NoError(t, err, "缓存失效失败不应影响主流程返回值")

	// Observe the background attempt despite its configured cleanup failure,
	// because repository success determines the returned result while this cache
	// outcome is diagnosed separately.
	require.Eventually(t, func() bool {
		return cache.invalidateCallCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond, "即使失败也应调用 InvalidateUserBalance")
}

// TestTouchLastActive_UpdatesWhenStale supplies an account activity timestamp
// eleven minutes old. Touch records one update for that account with a
// timestamp near the current test clock.
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
	// Service execution and this assertion sample wall time separately.
	require.WithinDuration(t, time.Now(), repo.updateLastActiveAt[0], 2*time.Second)
}

// TestTouchLastActive_SkipsWhenRecent supplies an account activity timestamp
// one minute old. Touch records neither account ID nor timestamp, protecting
// the represented recent-activity suppression path.
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

// TestUpdateBalance_RepoError_ReturnsError fails the repository balance
// update with a configured error. The service returns update context and the
// cache counter remains zero during the represented observation interval.
func TestUpdateBalance_RepoError_ReturnsError(t *testing.T) {
	repo := &mockUserRepo{updateBalanceErr: errors.New("database error")}
	cache := &mockBillingCache{}
	svc := identity.NewUserService(repo, nil, nil, cache, runProfileBackground)

	err := svc.UpdateBalance(context.Background(), 1, 100.0)
	require.Error(t, err, "repo 失败时应返回错误")
	require.Contains(t, err.Error(), "update balance")

	// Allow the represented background observation interval to pass before
	// checking zero attempts; this assertion detects accidental cleanup
	// scheduling after the injected repository failure.
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, int64(0), cache.invalidateCallCount.Load(),
		"repo 失败时不应调用 InvalidateUserBalance")
}

// TestUpdateBalance_WithAuthCacheInvalidator accepts a balance update with
// both cache capabilities configured. Authentication invalidation is recorded
// before return, while the test waits separately for the background
// balance-cache attempt.
func TestUpdateBalance_WithAuthCacheInvalidator(t *testing.T) {
	repo := &mockUserRepo{}
	auth := &mockAuthCacheInvalidator{}
	cache := &mockBillingCache{}
	svc := identity.NewUserService(repo, nil, auth, cache, runProfileBackground)

	err := svc.UpdateBalance(context.Background(), 77, 300.0)
	require.NoError(t, err)

	// Read authentication history under its writer mutex after service return,
	// distinguishing that synchronous invalidation from the separately submitted
	// billing-cache task below.
	auth.mu.Lock()
	require.Equal(t, []int64{77}, auth.invalidatedUserIDs)
	auth.mu.Unlock()

	// Wait for the separate background billing-cache attempt after inspecting
	// synchronous authentication history, so this test does not infer task
	// completion from UpdateBalance returning.
	require.Eventually(t, func() bool {
		return cache.invalidateCallCount.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

// TestNewUserServiceSharesActivityTracker touches two account values with the
// same ID on one service instance. Only one repository timestamp write is
// recorded, protecting process-local throttling across distinct supplied
// copies.
func TestNewUserServiceSharesActivityTracker(t *testing.T) {
	user := &identity.User{ID: 77}
	repo := &mockUserRepo{getByIDUser: user}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)
	svc.TouchLastActiveForUser(context.Background(), identity.CopyUser(user))
	svc.TouchLastActiveForUser(context.Background(), user)
	require.Equal(t, []int64{77}, repo.updateLastActiveUserIDs)
}

// TestUpdateProfile_RejectsEmailChange submits a replacement mailbox through
// the ordinary profile API. The service returns
// profile-email-change-forbidden, records no user update and retains the
// fixture's current email.
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

// TestUpdateProfile_StoresInlineAvatarWithinLimit submits a small declared
// PNG data URL with known bytes. Avatar storage and returned account metadata
// retain its representation, byte count and SHA256 computed from those bytes.
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

// TestUpdateProfile_CompressesInlineAvatarToTwentyKilobytes builds a PNG
// above the 20-KiB target and within the accepted input cap. Update stores
// and returns an inline JPEG no larger than the target, with a populated
// hash.
func TestUpdateProfile_CompressesInlineAvatarToTwentyKilobytes(t *testing.T) {
	var encoded bytes.Buffer
	// Try bounded image sizes until encoded PNG exceeds the compression target
	// while staying within the input cap, making the subsequent update exercise
	// JPEG compression rather than small-data passthrough.
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

// TestUpdateProfile_RejectsInlineAvatarOverLimit submits decoded inline bytes
// one larger than the input cap. The service returns avatar-too-large before
// any avatar upsert/deletion or user update is recorded.
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

// TestUpdateProfile_StoresRemoteAvatarURL submits a remote HTTPS avatar
// address. The fixture records remote_url storage and the returned profile
// retains that URL/source with no inline byte count.
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

// TestUpdateProfile_DeletesAvatarOnEmptyString submits a present empty avatar
// URL for an account with a prior remote avatar. Deletion records its account
// ID and the returned URL/source clear without an upsert.
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

// TestUpdateProfile_RollsBackAvatarMutationWhenUserUpdateFails combines
// avatar replacement with a failing account-update callback. The fixture
// transaction returns that failure and keeps its base avatar/history
// unchanged; update counters remain outside fixture staging.
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

// TestGetProfile_HydratesAvatarFromRepository returns separate remote avatar
// metadata alongside an account lacking it. GetProfile fills the returned
// URL/source from that repository result, protecting profile view hydration.
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

// TestUpdateProfile_OnlyDeclaresRequestedColumns varies username and
// balance-notification inputs beside unchanged account funding/status.
// Recorded field masks contain each requested column group, documenting
// service selection rather than database mask enforcement by the fixture.
func TestUpdateProfile_OnlyDeclaresRequestedColumns(t *testing.T) {
	username := "renamed"
	tests := []struct {
		name string // descriptive field selection case
		// req retains optional pointer inputs supplied by the table. Tests keep
		// those pointed values stable while the service selects the represented
		// update mask.
		req  identity.UpdateProfileRequest // submitted optional profile inputs
		want identity.UserUpdateFields     // expected selected account columns
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

// TestUpdateProfile_AvatarOnlySkipsUserRowWrite submits only a remote avatar
// address. Avatar storage is recorded and the account-update fixture receives
// an empty field mask, protecting the declared column selection for that
// path.
func TestUpdateProfile_AvatarOnlySkipsUserRowWrite(t *testing.T) {
	repo := &mockUserRepo{getByIDUser: &identity.User{ID: 7, Balance: 0.30}}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	avatar := "https://cdn.example.com/a.png"
	_, err := svc.UpdateProfile(context.Background(), 7, identity.UpdateProfileRequest{AvatarURL: &avatar})
	require.NoError(t, err)
	require.Len(t, repo.upsertAvatarArgs, 1, "avatar must still be stored")
	require.Equal(t, []identity.UserUpdateFields{{}}, repo.updateFields, "no user column should be declared")
}

// TestChangePassword_OnlyDeclaresPasswordHash submits valid current-password
// proof and a replacement password. The repository receives a
// PasswordHash-only mask, keeping unrelated columns outside the service's
// declared update.
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

// TestUpdateStatus_OnlyDeclaresStatus changes an active account to disabled
// through the status operation. The fixture records only the Status field
// mask, protecting the service's selected account columns.
func TestUpdateStatus_OnlyDeclaresStatus(t *testing.T) {
	repo := &mockUserRepo{getByIDUser: &identity.User{ID: 7, Balance: 0.30, Status: identity.StatusActive}}
	svc := identity.NewUserService(repo, nil, nil, nil, runProfileBackground)

	require.NoError(t, svc.UpdateStatus(context.Background(), 7, "disabled"))
	require.Equal(t, []identity.UserUpdateFields{{Status: true}}, repo.updateFields)
}

// TestUpdateProfileLanguagePreference varies locale alias, clearing, omission
// and invalid text. Returned preferences and recorded locale masks follow
// selected inputs; invalid language rejects before any mask is recorded.
func TestUpdateProfileLanguagePreference(t *testing.T) {
	en, zh, invalid := "en", "zh", "unsupported"
	for _, tc := range []struct {
		name string // descriptive locale update case
		// input retains the requested locale pointer or clearing flag. The table
		// keeps pointed locale text stable until each synchronous profile request
		// returns.
		input identity.UpdateProfileRequest // submitted locale preference controls
		// expected is nil for cleared preference and points to selected locale text
		// otherwise. The table keeps those strings stable while returned profile
		// values are asserted.
		expected *string // expected resulting locale pointer
		// wantError selects invalid-language rejection before update masks are
		// recorded. False cases inspect returned locale and the selected field mask
		// instead.
		wantError bool // expected locale validation failure
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
