package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Recovery fixtures record each primary clear and inject failures at selected
// steps. The tests check that later clears and cache deletion stop after
// primary errors, while an auxiliary cache failure leaves full recovery
// successful; result flags and token invalidation are checked for observed
// error/runtime state.

// rateLimitClearRepoStub records primary recovery attempts and returns each
// configured read or clear failure.
type rateLimitClearRepoStub struct {
	// getByIDProvider is returned directly on a successful read; tests retain its
	// pointer while recovery inspects the fixture's supplied health fields.
	getByIDProvider *Record
	// getByIDErr selects a failed read before the supplied provider pointer can be
	// returned; the read-attempt counter still increments.
	getByIDErr                error
	getByIDCalls              int   // provider read attempts
	clearErrorCalls           int   // error clear attempts
	clearRateLimitCalls       int   // window clear attempts
	clearAntigravityCalls     int   // quota-scope clear attempts
	clearModelRateLimitCalls  int   // model-limit clear attempts
	clearTempUnschedCalls     int   // temporary pause clears
	clearErrorErr             error // error clear failure
	clearRateLimitErr         error // window clear failure
	clearAntigravityErr       error // quota-scope clear failure
	clearModelRateLimitErr    error // model-limit clear failure
	clearTempUnschedulableErr error // temporary pause failure
}

// tempUnschedCacheRecorder records cache deletion attempts and returns a
// configured cleanup failure.
type tempUnschedCacheRecorder struct {
	// deletedIDs and deleteErr distinguish attempted cache cleanup from its
	// outcome. DeleteTempUnsched records the ID before returning the configured
	// error, including a failed attempt.
	deletedIDs []int64 // attempted cache deletions
	deleteErr  error   // cache deletion failure
}

// recoverTokenInvalidatorStub records token cleanup inputs and returns its
// configured failure.
type recoverTokenInvalidatorStub struct {
	// providers and err record invalidation attempts separately from success.
	// InvalidateToken retains the exact supplied pointers before returning err;
	// tests inspect them after synchronous recovery.
	providers []*Record // invalidation input records
	err       error     // token invalidation failure
}

// runtimeBlockRecorder records the provider IDs passed to runtime-block cleanup
// callbacks.
type runtimeBlockRecorder struct {
	clearedIDs []int64 // runtime cleanup IDs
}

// GetByID counts a read and returns its configured failure or shared provider
// pointer. Tests keep that record stable while recovery reads it; context and
// requested ID do not select another fixture.
func (r *rateLimitClearRepoStub) GetByID(ctx context.Context, id int64) (*Record, error) {
	r.getByIDCalls++
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	return r.getByIDProvider, nil
}

// ClearError records a primary cleanup attempt and returns its configured
// failure.
func (r *rateLimitClearRepoStub) ClearError(ctx context.Context, id int64) error {
	r.clearErrorCalls++
	return r.clearErrorErr
}

// ClearRateLimit records a window cleanup attempt and returns its configured
// failure.
func (r *rateLimitClearRepoStub) ClearRateLimit(ctx context.Context, id int64) error {
	r.clearRateLimitCalls++
	return r.clearRateLimitErr
}

// ClearAntigravityQuotaScopes records a quota-scope cleanup attempt and returns
// its configured failure.
func (r *rateLimitClearRepoStub) ClearAntigravityQuotaScopes(ctx context.Context, id int64) error {
	r.clearAntigravityCalls++
	return r.clearAntigravityErr
}

// ClearModelRateLimits records a model-limit cleanup attempt and returns its
// configured failure.
func (r *rateLimitClearRepoStub) ClearModelRateLimits(ctx context.Context, id int64) error {
	r.clearModelRateLimitCalls++
	return r.clearModelRateLimitErr
}

// ClearTempUnschedulable records a temporary-pause cleanup attempt and returns
// its configured failure.
func (r *rateLimitClearRepoStub) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.clearTempUnschedCalls++
	return r.clearTempUnschedulableErr
}

// SetTempUnsched supplies a successful cache-write result for the recovery
// fixture.
func (c *tempUnschedCacheRecorder) SetTempUnsched(ctx context.Context, providerID int64, state *TempUnschedState) error {
	return nil
}

// GetTempUnsched returns a cache miss with no error for every fixture lookup.
// Status reconstruction can therefore use persistent state when this recorder
// supplies the optional cache.
func (c *tempUnschedCacheRecorder) GetTempUnsched(ctx context.Context, providerID int64) (*TempUnschedState, error) {
	return nil, nil
}

// DeleteTempUnsched records the requested ID before returning the fixture's
// cleanup error. Successful and failed deletion attempts are both visible in
// deletedIDs so tests can inspect reached cache work.
func (c *tempUnschedCacheRecorder) DeleteTempUnsched(ctx context.Context, providerID int64) error {
	c.deletedIDs = append(c.deletedIDs, providerID)
	return c.deleteErr
}

// InvalidateToken records the exact provider pointer before returning the
// configured failure. The fixture keeps those pointers for inspection after the
// synchronous command, and the callback performs no provider copy.
func (s *recoverTokenInvalidatorStub) InvalidateToken(ctx context.Context, provider *Record) error {
	s.providers = append(s.providers, provider)
	return s.err
}

// TestRecoveryService_ClearRateLimit_AlsoClearsTempUnschedulable supplies
// successful primary and cache cleanup. It checks all four primary clear calls
// occur once and the cache deletion receives the selected provider ID.
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

// TestRecoveryService_ClearRateLimit_ClearTempUnschedulableFailed injects
// failure at the temporary-pause step. It checks the error returns after that
// step is attempted and cache deletion remains unreached.
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

// TestRecoveryService_ClearRateLimit_ClearRateLimitFailed fails the first
// global-window clear. It checks all later primary clears and cache deletion
// remain unreached while the initial attempt is recorded.
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

// TestRecoveryService_ClearRateLimit_ClearAntigravityFailed fails quota-scope
// cleanup after the global clear. It checks the earlier call remains recorded
// and model, temporary and cache cleanup are skipped.
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

// TestRecoveryService_ClearRateLimit_ClearModelRateLimitsFailed fails
// model-limit cleanup after earlier steps. It checks the method returns an
// error with the attempted model clear and no subsequent cache deletion.
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

// TestRecoveryService_ClearRateLimit_CacheDeleteFailedShouldNotFail fails cache
// deletion after successful primary cleanup. It checks full recovery still
// returns nil and records all primary calls plus the attempted deletion ID.
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

// TestRecoveryService_ClearRateLimit_WithoutTempUnschedCache omits the optional
// cache dependency. It checks every primary clear still runs once and the
// command completes successfully for the selected provider.
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

// TestRecoveryService_RecoverProviderAfterSuccessfulTest_ClearsErrorAndRateLimitRelatedState
// supplies an errored record with window and scoped health data. It checks both
// result flags, each reached clear, cache deletion and runtime cleanup ID.
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

// TestRecoveryService_RecoverProviderAfterSuccessfulTest_NoRecoverableStateIsNoop
// supplies an active record with empty Extra and no windows. It checks one
// read, false result flags and no primary clear or cache deletion.
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

// TestRecoveryService_RecoverProviderAfterSuccessfulTest_ClearErrorFailed fails
// primary error cleanup on an errored record. It checks a nil recovery result,
// one error-clear attempt and no later global-window clear.
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

// TestRecoveryService_RecoverProviderState_InvalidatesOAuthTokenOnErrorRecovery
// requests invalidation after an OAuth error clear. It checks the error flag,
// unselected window flag and the recorded provider ID passed to token cleanup.
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

// TestRecoveryService_RecoverProviderState_InvalidatesQoderCosyTokenOnErrorRecovery
// requests invalidation for an errored Qoder COSY record. It checks the same
// generic recovery callback receives that provider, with error recovery true
// and window recovery false.
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

// ClearProviderSchedulingBlock records the provider ID selected for runtime
// recovery cleanup.
func (r *runtimeBlockRecorder) ClearProviderSchedulingBlock(id int64) {
	r.clearedIDs = append(r.clearedIDs, id)
}
