package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// providerRepoStubForClearProviderError records recovery steps while mutating a
// retained provider.
type providerRepoStubForClearProviderError struct {
	AdminStore // unused administrative store methods
	// provider is the retained record mutated by each recovery step. GetByID
	// returns a clone after those mutations for assertions.
	provider                 *Record
	clearErrorCalls          int // durable error cleanup count
	clearRateLimitCalls      int // global cooldown cleanup count
	clearAntigravityCalls    int // Antigravity scope cleanup count
	clearModelRateLimitCalls int // model cooldown cleanup count
	clearTempUnschedCalls    int // temporary pause cleanup count
}

// adminClearErrorRuntimeBlockRecorder records process scheduling blocks removed
// by administrative recovery.
type adminClearErrorRuntimeBlockRecorder struct {
	clearedIDs []int64 // runtime-unblocked local provider identifiers
}

// deleteProviderStore controls lookup and deletion failures for cascade-order
// tests.
type deleteProviderStore struct {
	AdminStore // unused administrative store methods
	// shadows is returned directly by ListShadowsByParent. The test owns this
	// slice and keeps its child pointers stable during cascade deletion.
	shadows  []*Record
	listErr, // injected shadow lookup error
	deleteErr error // injected provider deletion error
	deletedIDs []int64 // ordered attempted provider deletions
}

// providerRepoStubForAdminList records page filters and supplies configurable
// list results.
type providerRepoStubForAdminList struct {
	AdminStore // unused administrative store methods

	listWithFiltersCalls    int                         // administrator page query count
	listWithFiltersParams   pagination.PaginationParams // recorded pagination and sorting
	listWithFiltersPlatform string                      // recorded upstream platform filter
	listWithFiltersType     string                      // recorded provider authentication filter
	listWithFiltersStatus   string                      // recorded persisted status filter
	listWithFiltersSearch   string                      // recorded provider text search
	listWithFiltersPrivacy  string                      // recorded privacy mode filter
	// listWithFiltersProviders is returned as the retained slice. Its records and
	// their referenced values remain shared with the test after ListWithFilters.
	listWithFiltersProviders []Record
	// listWithFiltersResult overrides derived pagination when nonnil. Nil lets
	// ListWithFilters use the requested page and fixture row count.
	listWithFiltersResult *pagination.PaginationResult
	listWithFiltersErr    error // injected page query error
}

// resetProviderQuotaRepoStub distinguishes combined quota reset from separate
// health cleanup.
type resetProviderQuotaRepoStub struct {
	AdminStore                   // unused administrative store methods
	provider            *Record  // quota eligibility fixture record
	getByIDErr          error    // injected eligibility lookup error
	resetErr            error    // injected combined reset error
	resetCalls          int      // combined quota reset count
	clearRateLimitCalls int      // separate health cleanup count
	callOrder           []string // ordered quota operation names
	// overloaded is cleared by separate ClearRateLimit calls. Combined quota reset
	// leaves it unchanged so tests detect accidental overload cleanup.
	overloaded bool
}

// GetByID returns a supported-container copy of the recovery fixture's current
// provider. Tests inspect the reloaded result after cleanup without sharing its
// mutable JSON containers with the retained fixture record.
func (r *providerRepoStubForClearProviderError) GetByID(ctx context.Context, id int64) (*Record, error) {
	return CloneRecord(r.provider), nil
}

// ClearError records recovery and changes the fixture to active status with an
// empty message.
func (r *providerRepoStubForClearProviderError) ClearError(ctx context.Context, id int64) error {
	r.clearErrorCalls++
	r.provider.Status = billing.StatusActive
	r.provider.ErrorMessage = ""
	return nil
}

// ClearRateLimit records global cooldown cleanup and removes the fixture's
// rate-limit timestamps.
func (r *providerRepoStubForClearProviderError) ClearRateLimit(ctx context.Context, id int64) error {
	r.clearRateLimitCalls++
	r.provider.RateLimitedAt = nil
	r.provider.RateLimitResetAt = nil
	return nil
}

// ClearAntigravityQuotaScopes counts scope cleanup calls made during
// administrative recovery.
func (r *providerRepoStubForClearProviderError) ClearAntigravityQuotaScopes(ctx context.Context, id int64) error {
	r.clearAntigravityCalls++
	return nil
}

// ClearModelRateLimits records the model-specific cleanup call in the recovery
// fixture.
func (r *providerRepoStubForClearProviderError) ClearModelRateLimits(ctx context.Context, id int64) error {
	r.clearModelRateLimitCalls++
	return nil
}

// ClearTempUnschedulable records cooldown cleanup and clears the retained
// fixture's temporary pause fields.
func (r *providerRepoStubForClearProviderError) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.clearTempUnschedCalls++
	r.provider.TempUnschedulableUntil = nil
	r.provider.TempUnschedulableReason = ""
	return nil
}

// TestAdminService_ClearProviderError_AlsoClearsRecoverableRuntimeState
// recovers a provider with error and cooldown state. Each storage cleanup is
// called once, the returned cooldown fields are cleared, and the optional
// runtime blocker records that provider ID.
func TestAdminService_ClearProviderError_AlsoClearsRecoverableRuntimeState(t *testing.T) {
	until := time.Now().Add(10 * time.Minute)
	resetAt := time.Now().Add(5 * time.Minute)
	repo := &providerRepoStubForClearProviderError{
		provider: &Record{
			ID:                      31,
			Platform:                capability.PlatformOpenAI,
			Type:                    capability.ProviderTypeOAuth,
			Status:                  StatusError,
			ErrorMessage:            "refresh failed",
			RateLimitResetAt:        &resetAt,
			TempUnschedulableUntil:  &until,
			TempUnschedulableReason: "missing refresh token",
		},
	}
	blocker := &adminClearErrorRuntimeBlockRecorder{}
	svc := NewAdmin(repo, AdminOptions{RuntimeBlocker: blocker})

	updated, err := svc.ClearProviderError(context.Background(), 31)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, repo.clearErrorCalls)
	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Equal(t, 1, repo.clearAntigravityCalls)
	require.Equal(t, 1, repo.clearModelRateLimitCalls)
	require.Equal(t, 1, repo.clearTempUnschedCalls)
	require.Nil(t, updated.RateLimitResetAt)
	require.Nil(t, updated.TempUnschedulableUntil)
	require.Empty(t, updated.TempUnschedulableReason)
	require.Equal(t, []int64{31}, blocker.clearedIDs)
}

// ClearProviderSchedulingBlock records the provider ID unblocked after storage
// recovery calls finish.
func (r *adminClearErrorRuntimeBlockRecorder) ClearProviderSchedulingBlock(id int64) {
	r.clearedIDs = append(r.clearedIDs, id)
}

// ListShadowsByParent returns the configured child pointers alongside the
// injected lookup error. Cascade tests control both values and inspect
// attempted deletes, so the returned slice remains fixture-owned.
func (s *deleteProviderStore) ListShadowsByParent(context.Context, int64) ([]*Record, error) {
	return s.shadows, s.listErr
}

// Delete records each attempted provider ID before returning the configured
// storage error. The fixture records order even on failure so tests can
// determine whether the parent call was attempted.
func (s *deleteProviderStore) Delete(_ context.Context, id int64) error {
	s.deletedIDs = append(s.deletedIDs, id)
	return s.deleteErr
}

// TestAdminDeleteProviderNotFound deletes a provider when storage reports it
// missing. The missing-provider error remains inspectable, and the fixture
// records the attempted parent deletion identifier.
func TestAdminDeleteProviderNotFound(t *testing.T) {
	repo := &deleteProviderStore{deleteErr: ErrProviderNotFound}
	err := NewAdmin(repo, AdminOptions{}).DeleteProvider(t.Context(), 55)
	require.ErrorIs(t, err, ErrProviderNotFound)
	require.Equal(t, []int64{55}, repo.deletedIDs)
}

// TestAdminDeleteProviderLookupFailureDoesNotDelete fails the shadow lookup
// before cascade deletion. The lookup cause is returned, and the fixture
// records no attempted deletion of the parent or a child.
func TestAdminDeleteProviderLookupFailureDoesNotDelete(t *testing.T) {
	cause := errors.New("db down")
	repo := &deleteProviderStore{listErr: cause}
	err := NewAdmin(repo, AdminOptions{}).DeleteProvider(t.Context(), 55)
	require.ErrorIs(t, err, cause)
	require.Empty(t, repo.deletedIDs)
}

// TestAdminDeleteProviderStorageFailure injects a parent deletion error after
// an empty shadow lookup. The error remains inspectable, and the fixture
// confirms that the requested parent was attempted.
func TestAdminDeleteProviderStorageFailure(t *testing.T) {
	cause := errors.New("delete failed")
	repo := &deleteProviderStore{deleteErr: cause}
	err := NewAdmin(repo, AdminOptions{}).DeleteProvider(t.Context(), 55)
	require.ErrorIs(t, err, cause)
	require.Equal(t, []int64{55}, repo.deletedIDs)
}

// TestAdminDeleteProviderDeletesShadowsBeforeParent supplies one child for
// cascade deletion. The recorded deletion sequence places the shadow first and
// its parent second, protecting the credential-owner lifetime.
func TestAdminDeleteProviderDeletesShadowsBeforeParent(t *testing.T) {
	repo := &deleteProviderStore{shadows: []*Record{{ID: 56}}}
	err := NewAdmin(repo, AdminOptions{}).DeleteProvider(t.Context(), 55)
	require.NoError(t, err)
	require.Equal(t, []int64{56, 55}, repo.deletedIDs)
}

// ListAllWithFilters supplies no score candidates for these ordinary provider
// paging fixtures.
func (s *providerRepoStubForAdminList) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Record, error) {
	return nil, nil
}

// ListWithFilters records page and filtering inputs before returning its
// configured result. It builds pagination from fixture rows when no explicit
// result is supplied, and configured errors stop the fixture response.
func (s *providerRepoStubForAdminList) ListWithFilters(_ context.Context, params pagination.PaginationParams, platform, providerType, status, search string, groupID int64, privacyMode string) ([]Record, *pagination.PaginationResult, error) {
	s.listWithFiltersCalls++
	s.listWithFiltersParams = params
	s.listWithFiltersPlatform = platform
	s.listWithFiltersType = providerType
	s.listWithFiltersStatus = status
	s.listWithFiltersSearch = search
	s.listWithFiltersPrivacy = privacyMode

	if s.listWithFiltersErr != nil {
		return nil, nil, s.listWithFiltersErr
	}

	result := s.listWithFiltersResult
	if result == nil {
		result = &pagination.PaginationResult{
			Total:    int64(len(s.listWithFiltersProviders)),
			Page:     params.Page,
			PageSize: params.PageSize,
		}
	}

	return s.listWithFiltersProviders, result, nil
}

// TestAdminService_ListProviders_WithSearch lists one fixture row using search
// and explicit sorting. The storage call receives the supplied filters and
// pagination, while its reported total is returned to administration.
func TestAdminService_ListProviders_WithSearch(t *testing.T) {
	t.Run("search 参数正常传递到 repository 层", func(t *testing.T) {
		repo := &providerRepoStubForAdminList{
			listWithFiltersProviders: []Record{{ID: 1, Name: "acc"}},
			listWithFiltersResult:    &pagination.PaginationResult{Total: 10},
		}
		svc := NewAdmin(repo, AdminOptions{})

		providers, total, err := svc.ListProviders(context.Background(), 1, 20, capability.PlatformGemini, capability.ProviderTypeOAuth, billing.StatusActive, "acc", 0, "", "name", "ASC")
		require.NoError(t, err)
		require.Equal(t, int64(10), total)
		require.Equal(t, []Record{{ID: 1, Name: "acc"}}, providers)

		require.Equal(t, 1, repo.listWithFiltersCalls)
		require.Equal(t, pagination.PaginationParams{Page: 1, PageSize: 20, SortBy: "name", SortOrder: "ASC"}, repo.listWithFiltersParams)
		require.Equal(t, capability.PlatformGemini, repo.listWithFiltersPlatform)
		require.Equal(t, capability.ProviderTypeOAuth, repo.listWithFiltersType)
		require.Equal(t, billing.StatusActive, repo.listWithFiltersStatus)
		require.Equal(t, "acc", repo.listWithFiltersSearch)
	})
}

// TestAdminService_ListProviders_WithPrivacyMode lists OpenAI OAuth fixtures
// using a privacy-mode filter. Administration returns the configured rows and
// total while passing the privacy selection through to storage.
func TestAdminService_ListProviders_WithPrivacyMode(t *testing.T) {
	t.Run("privacy_mode 参数正常传递到 repository 层", func(t *testing.T) {
		repo := &providerRepoStubForAdminList{
			listWithFiltersProviders: []Record{{ID: 2, Name: "acc2"}},
			listWithFiltersResult:    &pagination.PaginationResult{Total: 1},
		}
		svc := NewAdmin(repo, AdminOptions{})

		providers, total, err := svc.ListProviders(context.Background(), 1, 20, capability.PlatformOpenAI, capability.ProviderTypeOAuth, billing.StatusActive, "acc2", 0, openai.PrivacyModeCFBlocked, "", "")
		require.NoError(t, err)
		require.Equal(t, int64(1), total)
		require.Equal(t, []Record{{ID: 2, Name: "acc2"}}, providers)
		require.Equal(t, openai.PrivacyModeCFBlocked, repo.listWithFiltersPrivacy)
	})
}

// GetByID returns the configured lookup error alongside a supported-container
// provider clone. Administration stops on that error before quota writing,
// while successful lookups exercise parent or shadow eligibility.
func (r *resetProviderQuotaRepoStub) GetByID(context.Context, int64) (*Record, error) {
	return CloneRecord(r.provider), r.getByIDErr
}

// ResetQuotaUsedAndClearRateLimitCooldown records the combined reset operation
// and returns its injected error.
func (r *resetProviderQuotaRepoStub) ResetQuotaUsedAndClearRateLimitCooldown(context.Context, int64) error {
	r.resetCalls++
	r.callOrder = append(r.callOrder, "reset_quota_and_clear_rate_limit_cooldown")
	return r.resetErr
}

// ClearRateLimit records separate health cleanup and clears the fixture's
// overload marker.
func (r *resetProviderQuotaRepoStub) ClearRateLimit(context.Context, int64) error {
	r.clearRateLimitCalls++
	r.callOrder = append(r.callOrder, "clear_rate_limit")
	r.overloaded = false
	return nil
}

// TestResetProviderQuota_ClearsSchedulerRateLimitWithoutClearingOverload resets
// quota on an overloaded parent fixture. Only the combined quota/cooldown
// writer is called, and the unrelated overload marker remains set.
func TestResetProviderQuota_ClearsSchedulerRateLimitWithoutClearingOverload(t *testing.T) {
	repo := &resetProviderQuotaRepoStub{provider: &Record{ID: 42}, overloaded: true}
	svc := NewAdmin(repo, AdminOptions{Quotas: repo})

	err := svc.ResetProviderQuota(context.Background(), 42)

	require.NoError(t, err)
	require.Equal(t, 1, repo.resetCalls)
	require.Zero(t, repo.clearRateLimitCalls)
	require.Equal(t, []string{"reset_quota_and_clear_rate_limit_cooldown"}, repo.callOrder)
	require.True(t, repo.overloaded, "quota reset must preserve an unrelated overload block")
}

// TestResetProviderQuota_PreservesLookupAndSparkShadowShortCircuits requests
// quota reset after a failed lookup and for a Spark shadow. Both paths stop
// before the combined reset writer or separate health cleanup is called.
func TestResetProviderQuota_PreservesLookupAndSparkShadowShortCircuits(t *testing.T) {
	t.Run("lookup failure", func(t *testing.T) {
		getErr := errors.New("get provider failed")
		repo := &resetProviderQuotaRepoStub{getByIDErr: getErr}
		svc := NewAdmin(repo, AdminOptions{Quotas: repo})

		err := svc.ResetProviderQuota(context.Background(), 42)

		require.ErrorIs(t, err, getErr)
		require.Zero(t, repo.resetCalls)
		require.Zero(t, repo.clearRateLimitCalls)
	})

	t.Run("spark shadow", func(t *testing.T) {
		parentID := int64(7)
		repo := &resetProviderQuotaRepoStub{
			provider: &Record{ID: 42, ParentProviderID: &parentID},
		}
		svc := NewAdmin(repo, AdminOptions{Quotas: repo})

		err := svc.ResetProviderQuota(context.Background(), 42)

		require.Error(t, err)
		require.Zero(t, repo.resetCalls)
		require.Zero(t, repo.clearRateLimitCalls)
	})
}

// TestResetProviderQuota_PropagatesAtomicRepositoryFailure injects failure into
// the combined quota/cooldown writer. Administration returns that cause,
// records one combined call, and performs no separate ClearRateLimit operation.
func TestResetProviderQuota_PropagatesAtomicRepositoryFailure(t *testing.T) {
	resetErr := errors.New("atomic reset failed")
	repo := &resetProviderQuotaRepoStub{
		provider: &Record{ID: 42},
		resetErr: resetErr,
	}
	svc := NewAdmin(repo, AdminOptions{Quotas: repo})

	err := svc.ResetProviderQuota(context.Background(), 42)

	require.ErrorIs(t, err, resetErr)
	require.Equal(t, 1, repo.resetCalls)
	require.Zero(t, repo.clearRateLimitCalls)
}
