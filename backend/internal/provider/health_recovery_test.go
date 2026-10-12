package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type rateLimitClearRepoStub struct {
	getByIDProvider           *Record
	getByIDErr                error
	getByIDCalls              int
	clearErrorCalls           int
	clearRateLimitCalls       int
	clearAntigravityCalls     int
	clearModelRateLimitCalls  int
	clearTempUnschedCalls     int
	clearErrorErr             error
	clearRateLimitErr         error
	clearAntigravityErr       error
	clearModelRateLimitErr    error
	clearTempUnschedulableErr error
}

type tempUnschedCacheRecorder struct {
	deletedIDs []int64
	deleteErr  error
}

type recoverTokenInvalidatorStub struct {
	providers []*Record
	err       error
}

// runtimeBlockRecorder records scheduling-block cleanup notifications.
type runtimeBlockRecorder struct{ clearedIDs []int64 }

func (r *rateLimitClearRepoStub) GetByID(ctx context.Context, id int64) (*Record, error) {
	r.getByIDCalls++
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	return r.getByIDProvider, nil
}

func (r *rateLimitClearRepoStub) ClearError(ctx context.Context, id int64) error {
	r.clearErrorCalls++
	return r.clearErrorErr
}

func (r *rateLimitClearRepoStub) ClearRateLimit(ctx context.Context, id int64) error {
	r.clearRateLimitCalls++
	return r.clearRateLimitErr
}

func (r *rateLimitClearRepoStub) ClearAntigravityQuotaScopes(ctx context.Context, id int64) error {
	r.clearAntigravityCalls++
	return r.clearAntigravityErr
}

func (r *rateLimitClearRepoStub) ClearModelRateLimits(ctx context.Context, id int64) error {
	r.clearModelRateLimitCalls++
	return r.clearModelRateLimitErr
}

func (r *rateLimitClearRepoStub) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.clearTempUnschedCalls++
	return r.clearTempUnschedulableErr
}

func (c *tempUnschedCacheRecorder) SetTempUnsched(ctx context.Context, providerID int64, state *TempUnschedState) error {
	return nil
}

func (c *tempUnschedCacheRecorder) GetTempUnsched(ctx context.Context, providerID int64) (*TempUnschedState, error) {
	return nil, nil
}

func (c *tempUnschedCacheRecorder) DeleteTempUnsched(ctx context.Context, providerID int64) error {
	c.deletedIDs = append(c.deletedIDs, providerID)
	return c.deleteErr
}

func (s *recoverTokenInvalidatorStub) InvalidateToken(ctx context.Context, provider *Record) error {
	s.providers = append(s.providers, provider)
	return s.err
}

func TestRecoveryService_ClearRateLimit_AlsoClearsTempUnschedulable(t *testing.T) {
	repo := &rateLimitClearRepoStub{}
	cache := &tempUnschedCacheRecorder{}
	svc := NewRecoveryService(repo, cache, RecoveryOptions{})

	err := svc.ClearRateLimit(context.Background(), 42)
	require.NoError(t, err)

	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Equal(t, 1, repo.clearAntigravityCalls)
	require.Equal(t, 1, repo.clearModelRateLimitCalls)
	require.Equal(t, 1, repo.clearTempUnschedCalls)
	require.Equal(t, []int64{42}, cache.deletedIDs)
}

func TestRecoveryService_ClearRateLimit_ClearTempUnschedulableFailed(t *testing.T) {
	repo := &rateLimitClearRepoStub{
		clearTempUnschedulableErr: errors.New("clear temp unsched failed"),
	}
	cache := &tempUnschedCacheRecorder{}
	svc := NewRecoveryService(repo, cache, RecoveryOptions{})

	err := svc.ClearRateLimit(context.Background(), 7)
	require.Error(t, err)

	require.Equal(t, 1, repo.clearTempUnschedCalls)
	require.Empty(t, cache.deletedIDs)
}

func TestRecoveryService_ClearRateLimit_ClearRateLimitFailed(t *testing.T) {
	repo := &rateLimitClearRepoStub{
		clearRateLimitErr: errors.New("clear rate limit failed"),
	}
	cache := &tempUnschedCacheRecorder{}
	svc := NewRecoveryService(repo, cache, RecoveryOptions{})

	err := svc.ClearRateLimit(context.Background(), 11)
	require.Error(t, err)

	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Equal(t, 0, repo.clearAntigravityCalls)
	require.Equal(t, 0, repo.clearModelRateLimitCalls)
	require.Equal(t, 0, repo.clearTempUnschedCalls)
	require.Empty(t, cache.deletedIDs)
}

func TestRecoveryService_ClearRateLimit_ClearAntigravityFailed(t *testing.T) {
	repo := &rateLimitClearRepoStub{
		clearAntigravityErr: errors.New("clear antigravity failed"),
	}
	cache := &tempUnschedCacheRecorder{}
	svc := NewRecoveryService(repo, cache, RecoveryOptions{})

	err := svc.ClearRateLimit(context.Background(), 12)
	require.Error(t, err)

	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Equal(t, 1, repo.clearAntigravityCalls)
	require.Equal(t, 0, repo.clearModelRateLimitCalls)
	require.Equal(t, 0, repo.clearTempUnschedCalls)
	require.Empty(t, cache.deletedIDs)
}

func TestRecoveryService_ClearRateLimit_ClearModelRateLimitsFailed(t *testing.T) {
	repo := &rateLimitClearRepoStub{
		clearModelRateLimitErr: errors.New("clear model rate limits failed"),
	}
	cache := &tempUnschedCacheRecorder{}
	svc := NewRecoveryService(repo, cache, RecoveryOptions{})

	err := svc.ClearRateLimit(context.Background(), 13)
	require.Error(t, err)

	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Equal(t, 1, repo.clearAntigravityCalls)
	require.Equal(t, 1, repo.clearModelRateLimitCalls)
	require.Equal(t, 0, repo.clearTempUnschedCalls)
	require.Empty(t, cache.deletedIDs)
}

func TestRecoveryService_ClearRateLimit_CacheDeleteFailedShouldNotFail(t *testing.T) {
	repo := &rateLimitClearRepoStub{}
	cache := &tempUnschedCacheRecorder{
		deleteErr: errors.New("cache delete failed"),
	}
	svc := NewRecoveryService(repo, cache, RecoveryOptions{})

	err := svc.ClearRateLimit(context.Background(), 14)
	require.NoError(t, err)

	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Equal(t, 1, repo.clearAntigravityCalls)
	require.Equal(t, 1, repo.clearModelRateLimitCalls)
	require.Equal(t, 1, repo.clearTempUnschedCalls)
	require.Equal(t, []int64{14}, cache.deletedIDs)
}

func TestRecoveryService_ClearRateLimit_WithoutTempUnschedCache(t *testing.T) {
	repo := &rateLimitClearRepoStub{}
	svc := NewRecoveryService(repo, nil, RecoveryOptions{})

	err := svc.ClearRateLimit(context.Background(), 15)
	require.NoError(t, err)

	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Equal(t, 1, repo.clearAntigravityCalls)
	require.Equal(t, 1, repo.clearModelRateLimitCalls)
	require.Equal(t, 1, repo.clearTempUnschedCalls)
}

func TestRecoveryService_RecoverProviderAfterSuccessfulTest_ClearsErrorAndRateLimitRelatedState(t *testing.T) {
	now := time.Now()
	repo := &rateLimitClearRepoStub{
		getByIDProvider: &Record{
			ID:                     42,
			Status:                 StatusError,
			RateLimitedAt:          &now,
			TempUnschedulableUntil: &now,
			Extra: map[string]any{
				"model_rate_limits": map[string]any{
					"claude-sonnet-4-5": map[string]any{
						"rate_limit_reset_at": now.Format(time.RFC3339),
					},
				},
				"antigravity_quota_scopes": map[string]any{"gemini": true},
			},
		},
	}
	cache := &tempUnschedCacheRecorder{}
	blocker := &runtimeBlockRecorder{}
	svc := NewRecoveryService(repo, cache, RecoveryOptions{})
	svc.options.ClearSchedulingBlock = blocker.ClearProviderSchedulingBlock

	result, err := svc.RecoverProviderAfterSuccessfulTest(context.Background(), 42)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClearedError)
	require.True(t, result.ClearedRateLimit)

	require.Equal(t, 1, repo.getByIDCalls)
	require.Equal(t, 1, repo.clearErrorCalls)
	require.Equal(t, 1, repo.clearRateLimitCalls)
	require.Equal(t, 1, repo.clearAntigravityCalls)
	require.Equal(t, 1, repo.clearModelRateLimitCalls)
	require.Equal(t, 1, repo.clearTempUnschedCalls)
	require.Equal(t, []int64{42}, cache.deletedIDs)
	require.Equal(t, []int64{42}, blocker.clearedIDs)
}

func TestRecoveryService_RecoverProviderAfterSuccessfulTest_NoRecoverableStateIsNoop(t *testing.T) {
	repo := &rateLimitClearRepoStub{
		getByIDProvider: &Record{
			ID:          7,
			Status:      StatusActive,
			Schedulable: true,
			Extra:       map[string]any{},
		},
	}
	cache := &tempUnschedCacheRecorder{}
	svc := NewRecoveryService(repo, cache, RecoveryOptions{})

	result, err := svc.RecoverProviderAfterSuccessfulTest(context.Background(), 7)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ClearedError)
	require.False(t, result.ClearedRateLimit)

	require.Equal(t, 1, repo.getByIDCalls)
	require.Equal(t, 0, repo.clearErrorCalls)
	require.Equal(t, 0, repo.clearRateLimitCalls)
	require.Equal(t, 0, repo.clearAntigravityCalls)
	require.Equal(t, 0, repo.clearModelRateLimitCalls)
	require.Equal(t, 0, repo.clearTempUnschedCalls)
	require.Empty(t, cache.deletedIDs)
}

func TestRecoveryService_RecoverProviderAfterSuccessfulTest_ClearErrorFailed(t *testing.T) {
	repo := &rateLimitClearRepoStub{
		getByIDProvider: &Record{
			ID:     9,
			Status: StatusError,
		},
		clearErrorErr: errors.New("clear error failed"),
	}
	svc := NewRecoveryService(repo, nil, RecoveryOptions{})

	result, err := svc.RecoverProviderAfterSuccessfulTest(context.Background(), 9)
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, 1, repo.getByIDCalls)
	require.Equal(t, 1, repo.clearErrorCalls)
	require.Equal(t, 0, repo.clearRateLimitCalls)
}

func TestRecoveryService_RecoverProviderState_InvalidatesOAuthTokenOnErrorRecovery(t *testing.T) {
	repo := &rateLimitClearRepoStub{
		getByIDProvider: &Record{
			ID:     21,
			Type:   ProviderTypeOAuth,
			Status: StatusError,
		},
	}
	invalidator := &recoverTokenInvalidatorStub{}
	svc := NewRecoveryService(repo, nil, RecoveryOptions{})
	svc.options.InvalidateToken = invalidator.InvalidateToken

	result, err := svc.RecoverProviderState(context.Background(), 21, ProviderRecoveryOptions{
		InvalidateToken: true,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClearedError)
	require.False(t, result.ClearedRateLimit)
	require.Equal(t, 1, repo.clearErrorCalls)
	require.Len(t, invalidator.providers, 1)
	require.Equal(t, int64(21), invalidator.providers[0].ID)
}

func TestRecoveryService_RecoverProviderState_InvalidatesQoderCosyTokenOnErrorRecovery(t *testing.T) {
	repo := &rateLimitClearRepoStub{
		getByIDProvider: &Record{
			ID:       22,
			Platform: PlatformQoder,
			Type:     ProviderTypeCosy,
			Status:   StatusError,
		},
	}
	invalidator := &recoverTokenInvalidatorStub{}
	svc := NewRecoveryService(repo, nil, RecoveryOptions{})
	svc.options.InvalidateToken = invalidator.InvalidateToken

	result, err := svc.RecoverProviderState(context.Background(), 22, ProviderRecoveryOptions{
		InvalidateToken: true,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClearedError)
	require.False(t, result.ClearedRateLimit)
	require.Equal(t, 1, repo.clearErrorCalls)
	require.Len(t, invalidator.providers, 1)
	require.Equal(t, int64(22), invalidator.providers[0].ID)
}

func (r *runtimeBlockRecorder) ClearProviderSchedulingBlock(id int64) {
	r.clearedIDs = append(r.clearedIDs, id)
}
