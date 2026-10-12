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

type providerRepoStubForClearProviderError struct {
	AdminStore
	provider                 *Record
	clearErrorCalls          int
	clearRateLimitCalls      int
	clearAntigravityCalls    int
	clearModelRateLimitCalls int
	clearTempUnschedCalls    int
}

// adminClearErrorRuntimeBlockRecorder records in-memory blocks cleared by admin recovery.
type adminClearErrorRuntimeBlockRecorder struct{ clearedIDs []int64 }

// deleteProviderStore supplies deletion storage calls so tests can verify Admin's cascade order.
type deleteProviderStore struct {
	AdminStore
	shadows            []*Record
	listErr, deleteErr error
	deletedIDs         []int64
}

type providerRepoStubForAdminList struct {
	AdminStore

	listWithFiltersCalls     int
	listWithFiltersParams    pagination.PaginationParams
	listWithFiltersPlatform  string
	listWithFiltersType      string
	listWithFiltersStatus    string
	listWithFiltersSearch    string
	listWithFiltersPrivacy   string
	listWithFiltersProviders []Record
	listWithFiltersResult    *pagination.PaginationResult
	listWithFiltersErr       error
}

type resetProviderQuotaRepoStub struct {
	AdminStore
	provider            *Record
	getByIDErr          error
	resetErr            error
	resetCalls          int
	clearRateLimitCalls int
	callOrder           []string
	overloaded          bool
}

func (r *providerRepoStubForClearProviderError) GetByID(ctx context.Context, id int64) (*Record, error) {
	return CloneRecord(r.provider), nil
}

func (r *providerRepoStubForClearProviderError) ClearError(ctx context.Context, id int64) error {
	r.clearErrorCalls++
	r.provider.Status = billing.StatusActive
	r.provider.ErrorMessage = ""
	return nil
}

func (r *providerRepoStubForClearProviderError) ClearRateLimit(ctx context.Context, id int64) error {
	r.clearRateLimitCalls++
	r.provider.RateLimitedAt = nil
	r.provider.RateLimitResetAt = nil
	return nil
}

func (r *providerRepoStubForClearProviderError) ClearAntigravityQuotaScopes(ctx context.Context, id int64) error {
	r.clearAntigravityCalls++
	return nil
}

func (r *providerRepoStubForClearProviderError) ClearModelRateLimits(ctx context.Context, id int64) error {
	r.clearModelRateLimitCalls++
	return nil
}

func (r *providerRepoStubForClearProviderError) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.clearTempUnschedCalls++
	r.provider.TempUnschedulableUntil = nil
	r.provider.TempUnschedulableReason = ""
	return nil
}

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

func (r *adminClearErrorRuntimeBlockRecorder) ClearProviderSchedulingBlock(id int64) {
	r.clearedIDs = append(r.clearedIDs, id)
}

func (s *deleteProviderStore) ListShadowsByParent(context.Context, int64) ([]*Record, error) {
	return s.shadows, s.listErr
}

func (s *deleteProviderStore) Delete(_ context.Context, id int64) error {
	s.deletedIDs = append(s.deletedIDs, id)
	return s.deleteErr
}

func TestAdminDeleteProviderNotFound(t *testing.T) {
	repo := &deleteProviderStore{deleteErr: ErrProviderNotFound}
	err := NewAdmin(repo, AdminOptions{}).DeleteProvider(t.Context(), 55)
	require.ErrorIs(t, err, ErrProviderNotFound)
	require.Equal(t, []int64{55}, repo.deletedIDs)
}

func TestAdminDeleteProviderLookupFailureDoesNotDelete(t *testing.T) {
	cause := errors.New("db down")
	repo := &deleteProviderStore{listErr: cause}
	err := NewAdmin(repo, AdminOptions{}).DeleteProvider(t.Context(), 55)
	require.ErrorIs(t, err, cause)
	require.Empty(t, repo.deletedIDs)
}

func TestAdminDeleteProviderStorageFailure(t *testing.T) {
	cause := errors.New("delete failed")
	repo := &deleteProviderStore{deleteErr: cause}
	err := NewAdmin(repo, AdminOptions{}).DeleteProvider(t.Context(), 55)
	require.ErrorIs(t, err, cause)
	require.Equal(t, []int64{55}, repo.deletedIDs)
}

func TestAdminDeleteProviderDeletesShadowsBeforeParent(t *testing.T) {
	repo := &deleteProviderStore{shadows: []*Record{{ID: 56}}}
	err := NewAdmin(repo, AdminOptions{}).DeleteProvider(t.Context(), 55)
	require.NoError(t, err)
	require.Equal(t, []int64{56, 55}, repo.deletedIDs)
}

func (s *providerRepoStubForAdminList) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Record, error) {
	return nil, nil
}

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

func (r *resetProviderQuotaRepoStub) GetByID(context.Context, int64) (*Record, error) {
	return CloneRecord(r.provider), r.getByIDErr
}

func (r *resetProviderQuotaRepoStub) ResetQuotaUsedAndClearRateLimitCooldown(context.Context, int64) error {
	r.resetCalls++
	r.callOrder = append(r.callOrder, "reset_quota_and_clear_rate_limit_cooldown")
	return r.resetErr
}

func (r *resetProviderQuotaRepoStub) ClearRateLimit(context.Context, int64) error {
	r.clearRateLimitCalls++
	r.callOrder = append(r.callOrder, "clear_rate_limit")
	r.overloaded = false
	return nil
}

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
