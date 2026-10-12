package provider

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// refreshAPIProviderRepo simulates credential reads and writes for the refresh coordinator.
type refreshAPIProviderRepo struct {
	provider * // returned by GetByID
	Record
	getByIDErr              error
	getByIDCalls            int
	getByIDErrAfterCall     int
	getByIDErrAfterCallErr  error
	updateErr               error
	updateCalls             int
	updateCredentialsCalls  int
	successCASCalls         int
	beforeSuccessCAS        func(*refreshAPIProviderRepo)
	lastExpectedCredentials map[string]any
	lastExpectedProxyID     *int64
}

// refreshAPIExecutorStub supplies configurable eligibility and exchange results.
type refreshAPIExecutorStub struct {
	needsRefresh  bool
	cannotRefresh bool
	credentials   map[string]any
	err           error
	refreshCalls  int
	canRefresh    func(*Record) bool
	onRefresh     func()
	delay         time.Duration
}

// refreshAPICacheStub records distributed lock and token-cache operations.
type refreshAPICacheStub struct {
	lockResult    bool
	lockErr       error
	releaseCalls  int
	releaseCtxErr error
	deleteCalls   int
	deleteKey     string
	deleteCtxErr  error
}

// refreshAPIProviderRepoWithRace returns a replacement record on later reads to simulate another worker refreshing.
type refreshAPIProviderRepoWithRace struct {
	refreshAPIProviderRepo
	raceProvider * // returned on 2nd+ GetByID call
	Record
	getByIDCalls int
}

// dynamicRefreshExecutor injects eligibility and exchange callbacks.
type dynamicRefreshExecutor struct {
	canRefresh       bool
	cacheKey         string
	needsRefreshFunc func() bool
	refreshFunc      func(context.Context, *Record) (map[string]any, error)
}

// refreshCoreRepo rejects database access while a cancelled caller is still waiting for the refresh lock.
type refreshCoreRepo struct{}

type refreshCoreExecutor struct{ refreshCalls int }

func TestIsInvalidGrantError(t *testing.T) {
	require.True(t, IsInvalidGrantError(errors.New("invalid_grant: token revoked")))
	require.True(t, IsInvalidGrantError(errors.New("INVALID_GRANT")))
	require.False(t, IsInvalidGrantError(errors.New("invalid_client")))
	require.False(t, IsInvalidGrantError(nil))
}

func (r *refreshAPIProviderRepo) GetByID(_ context.Context, _ int64) (*Record, error) {
	r.getByIDCalls++
	if r.getByIDErrAfterCall > 0 && r.getByIDCalls >= r.getByIDErrAfterCall {
		return nil, r.getByIDErrAfterCallErr
	}
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	return activeRefreshAPITestProvider(r.provider), nil
}

func activeRefreshAPITestProvider(provider *Record) *Record {
	if provider == nil || provider.Status != "" {
		return provider
	}
	copy := *provider
	copy.Status = billing.StatusActive
	return &copy
}

func (r *refreshAPIProviderRepo) Update(_ context.Context, _ *Record) error {
	r.updateCalls++
	return r.updateErr
}

func (r *refreshAPIProviderRepo) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	r.updateCalls++
	r.updateCredentialsCalls++
	if r.updateErr != nil {
		return r.updateErr
	}
	if r.provider == nil || r.provider.ID != id {
		r.provider = &Record{ID: id}
	}
	r.provider.Credentials = querycache.ShallowMap(credentials)
	return nil
}

func (r *refreshAPIProviderRepo) UpdateGrokOAuthCredentialsIfUnchanged(
	_ context.Context,
	id int64,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	credentials map[string]any,
) (bool, error) {
	r.successCASCalls++
	r.lastExpectedCredentials = querycache.ShallowMap(expectedCredentials)
	if expectedProxyID != nil {
		proxyID := *expectedProxyID
		r.lastExpectedProxyID = &proxyID
	} else {
		r.lastExpectedProxyID = nil
	}
	if r.beforeSuccessCAS != nil {
		r.beforeSuccessCAS(r)
	}
	if r.updateErr != nil {
		return false, r.updateErr
	}
	if r.provider == nil || r.provider.ID != id || r.provider.Platform != capability.PlatformGrok ||
		r.provider.Type != capability.ProviderTypeOAuth ||
		!reflect.DeepEqual(r.provider.Credentials, expectedCredentials) ||
		!reflect.DeepEqual(r.provider.ProxyID, expectedProxyID) {
		return false, nil
	}
	r.updateCalls++
	r.updateCredentialsCalls++
	r.provider.Credentials = querycache.ShallowMap(credentials)
	return true, nil
}

func (e *refreshAPIExecutorStub) CanRefresh(provider *Record) bool {
	if e.cannotRefresh {
		return false
	}
	if e.canRefresh != nil {
		return e.canRefresh(provider)
	}
	return true
}

func (e *refreshAPIExecutorStub) NeedsRefresh(_ *Record, _ time.Duration) bool {
	return e.needsRefresh
}

func (e *refreshAPIExecutorStub) Refresh(_ context.Context, _ *Record) (map[string]any, error) {
	e.refreshCalls++
	if e.delay > 0 {
		time.Sleep(e.delay)
	}
	if e.onRefresh != nil {
		e.onRefresh()
	}
	if e.err != nil {
		return nil, e.err
	}
	return e.credentials, nil
}

func (e *refreshAPIExecutorStub) CacheKey(provider *Record) string {
	return "test:api:" + provider.Platform
}

func (c *refreshAPICacheStub) GetAccessToken(context.Context, string) (string, error) {
	return "", nil
}

func (c *refreshAPICacheStub) SetAccessToken(context.Context, string, string, time.Duration) error {
	return nil
}

func (c *refreshAPICacheStub) DeleteAccessToken(ctx context.Context, key string) error {
	c.deleteCalls++
	c.deleteKey = key
	c.deleteCtxErr = ctx.Err()
	return nil
}

func (c *refreshAPICacheStub) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return c.lockResult, c.lockErr
}

func (c *refreshAPICacheStub) ReleaseRefreshLock(ctx context.Context, _ string) error {
	c.releaseCalls++
	c.releaseCtxErr = ctx.Err()
	return nil
}

func TestRefreshIfNeeded_Success(t *testing.T) {
	provider := &Record{ID: 1, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "new-token"},
	}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.NoError(t, err)
	require.True(t, result.Refreshed)
	require.NotNil(t, result.NewCredentials)
	require.Equal(t, "new-token", result.NewCredentials["access_token"])
	require.NotNil(t, result.NewCredentials["_token_version"]) // version stamp set
	require.Equal(t, 1, repo.updateCalls)                      // DB updated
	require.Equal(t, 1, repo.updateCredentialsCalls)
	require.Equal(t, 1, cache.releaseCalls) // lock released
	require.Equal(t, 1, executor.refreshCalls)
}

func TestRefreshIfNeeded_UpdateCredentialsPreservesRateLimitState(t *testing.T) {
	resetAt := time.Now().Add(45 * time.Minute)
	provider := &Record{
		ID:               11,
		Platform:         capability.PlatformGemini,
		Type:             capability.ProviderTypeOAuth,
		Status:           billing.StatusActive,
		RateLimitResetAt: &resetAt,
	}
	repo := &refreshAPIProviderRepo{provider: provider}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "safe-token"},
	}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.NoError(t, err)
	require.True(t, result.Refreshed)
	require.Equal(t, 1, repo.updateCredentialsCalls)
	require.NotNil(t, repo.provider.RateLimitResetAt)
	require.WithinDuration(t, resetAt, *repo.provider.RateLimitResetAt, time.Second)
}

func TestRefreshIfNeeded_LockHeld(t *testing.T) {
	provider := &Record{ID: 2, Platform: capability.PlatformAnthropic, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	cache := &refreshAPICacheStub{lockResult: false} // lock not acquired
	executor := &refreshAPIExecutorStub{needsRefresh: true}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.NoError(t, err)
	require.True(t, result.LockHeld)
	require.False(t, result.Refreshed)
	require.Equal(t, 0, repo.updateCalls)
	require.Equal(t, 0, executor.refreshCalls)
}

func TestRefreshIfNeeded_LockErrorDegrades(t *testing.T) {
	provider := &Record{ID: 3, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	cache := &refreshAPICacheStub{lockErr: errors.New("redis down")} // lock error
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "degraded-token"},
	}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.NoError(t, err)
	require.True(t, result.Refreshed)       // still refreshed (degraded mode)
	require.Equal(t, 1, repo.updateCalls)   // DB updated
	require.Equal(t, 0, cache.releaseCalls) // no lock to release
	require.Equal(t, 1, executor.refreshCalls)
}

func TestRefreshIfNeeded_NoCacheNoLock(t *testing.T) {
	provider := &Record{ID: 4, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "no-cache-token"},
	}

	api := newRefreshAPIForTest(repo, nil) // No distributed lock; the process-local lock still applies.
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.NoError(t, err)
	require.True(t, result.Refreshed)
	require.Equal(t, 1, repo.updateCalls)
}

func TestRefreshIfNeeded_AlreadyRefreshed(t *testing.T) {
	provider := &Record{ID: 5, Platform: capability.PlatformAnthropic, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{needsRefresh: false} // already refreshed

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.NoError(t, err)
	require.False(t, result.Refreshed)
	require.False(t, result.LockHeld)
	require.NotNil(t, result.Provider) // returns fresh provider
	require.Equal(t, 0, repo.updateCalls)
	require.Equal(t, 0, executor.refreshCalls)
}

func TestRefreshIfNeeded_RefreshError(t *testing.T) {
	provider := &Record{ID: 6, Platform: capability.PlatformAnthropic, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		err:          errors.New("invalid_grant: token revoked"),
	}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.Error(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Provider)
	require.Equal(t, provider.ID, result.Provider.ID)
	require.Contains(t, err.Error(), "invalid_grant")
	require.Equal(t, 0, repo.updateCalls)   // no DB update on refresh error
	require.Equal(t, 1, cache.releaseCalls) // lock still released via defer
}

func TestRefreshIfNeeded_DBUpdateError(t *testing.T) {
	provider := &Record{ID: 7, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{
		provider:  provider,
		updateErr: errors.New("db connection lost"),
	}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "token"},
	}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.Error(t, err)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrRefreshCredentialPersist)
	require.Equal(t, 1, repo.updateCalls) // attempted
}

func TestRefreshIfNeeded_GrokSuccessCASLetsConcurrentReauthorizationWin(t *testing.T) {
	proxyID := int64(17)
	provider := &Record{
		ID:       70,
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
		ProxyID:  &proxyID,
		Credentials: map[string]any{
			"access_token":   "attempted-access",
			"refresh_token":  "attempted-refresh",
			"_token_version": int64(1),
		},
	}
	repo := &refreshAPIProviderRepo{provider: provider}
	repo.beforeSuccessCAS = func(r *refreshAPIProviderRepo) {
		repairedProxyID := int64(23)
		r.provider.ProxyID = &repairedProxyID
		r.provider.Credentials = map[string]any{
			"access_token":   "reauthorized-access",
			"refresh_token":  "reauthorized-refresh",
			"_token_version": int64(2),
		}
	}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials: map[string]any{
			"access_token":  "provider-access",
			"refresh_token": "provider-refresh",
		},
	}

	result, err := newRefreshAPIForTest(repo, nil).RefreshIfNeeded(context.Background(), provider, executor, time.Hour)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Refreshed, "a lost success CAS is an already-refreshed skip")
	require.Nil(t, result.NewCredentials)
	require.Equal(t, "reauthorized-refresh", result.Provider.GetGrokRefreshToken())
	require.NotNil(t, result.Provider.ProxyID)
	require.Equal(t, int64(23), *result.Provider.ProxyID)
	require.Equal(t, 1, repo.successCASCalls)
	require.Equal(t, "attempted-refresh", repo.lastExpectedCredentials["refresh_token"])
	require.NotNil(t, repo.lastExpectedProxyID)
	require.Equal(t, proxyID, *repo.lastExpectedProxyID)
	require.Zero(t, repo.updateCredentialsCalls, "the provider result must not overwrite a concurrent repair")
}

func TestRefreshIfNeeded_GrokSuccessPersistenceFailureIsProviderContainment(t *testing.T) {
	provider := &Record{
		ID:       71,
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"access_token":  "attempted-access",
			"refresh_token": "attempted-refresh",
		},
	}
	repo := &refreshAPIProviderRepo{provider: provider, updateErr: errors.New("database unavailable")}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials: map[string]any{
			"access_token":  "provider-access",
			"refresh_token": "provider-refresh",
		},
	}

	result, err := newRefreshAPIForTest(repo, nil).RefreshIfNeeded(context.Background(), provider, executor, time.Hour)

	require.Error(t, err)
	require.Nil(t, result)
	var containmentErr *ProviderCycleContainmentRefreshError
	require.ErrorAs(t, err, &containmentErr)
	require.Equal(t, "attempted-refresh", provider.GetGrokRefreshToken(),
		"an ambiguous persistence result must not mutate the in-memory provider")
	require.Equal(t, 1, repo.successCASCalls)
	require.Zero(t, repo.updateCredentialsCalls)
}

func TestRefreshIfNeeded_GrokSuccessDurableRereadFailureIsProviderContainment(t *testing.T) {
	provider := &Record{
		ID:       72,
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"access_token":  "attempted-access",
			"refresh_token": "attempted-refresh",
		},
	}
	repo := &refreshAPIProviderRepo{
		provider:               provider,
		getByIDErrAfterCall:    2,
		getByIDErrAfterCallErr: errors.New("durable state unavailable"),
	}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials: map[string]any{
			"access_token":  "provider-access",
			"refresh_token": "provider-refresh",
		},
	}

	result, err := newRefreshAPIForTest(repo, cache).RefreshIfNeeded(context.Background(), provider, executor, time.Hour)

	require.Error(t, err)
	require.Nil(t, result)
	var containmentErr *ProviderCycleContainmentRefreshError
	require.ErrorAs(t, err, &containmentErr)
	require.Equal(t, 2, repo.getByIDCalls)
	require.Equal(t, 1, repo.successCASCalls)
	require.Equal(t, 1, cache.deleteCalls, "a committed credential rotation must invalidate the pre-rotation access-token cache")
	require.NoError(t, cache.deleteCtxErr)
}

func TestRefreshIfNeeded_DBRereadFails(t *testing.T) {
	provider := &Record{ID: 8, Platform: capability.PlatformAnthropic, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{
		provider:   nil, // GetByID returns nil
		getByIDErr: errors.New("db timeout"),
	}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "fallback-token"},
	}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.Error(t, err)
	var stateUnavailable *RefreshStateUnavailableError
	require.ErrorAs(t, err, &stateUnavailable)
	require.Nil(t, result)
	require.Zero(t, executor.refreshCalls, "a failed DB reread must not refresh stale credentials")
	require.Zero(t, repo.updateCalls)
	require.Equal(t, 1, cache.releaseCalls)
}

func TestRefreshIfNeeded_RequestPathDBRereadNilFailsClosed(t *testing.T) {
	provider := &Record{ID: 81, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true}
	repo := &refreshAPIProviderRepo{}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{needsRefresh: true}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(WithRefreshRequestPath(context.Background()), provider, executor, 3*time.Minute)

	require.ErrorIs(t, err, ErrRefreshProviderStateChanged)
	require.Nil(t, result)
	require.Zero(t, executor.refreshCalls)
	require.Zero(t, repo.updateCalls)
	require.Equal(t, 1, cache.releaseCalls)
}

func TestRefreshIfNeeded_RequestPathDBRereadInactiveFailsClosed(t *testing.T) {
	provider := &Record{ID: 82, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true}
	freshProvider := &Record{ID: provider.ID, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Status: billing.StatusDisabled}
	repo := &refreshAPIProviderRepo{provider: freshProvider}
	executor := &refreshAPIExecutorStub{needsRefresh: true}

	api := newRefreshAPIForTest(repo, nil)
	result, err := api.RefreshIfNeeded(WithRefreshRequestPath(context.Background()), provider, executor, 3*time.Minute)

	require.ErrorContains(t, err, "provider is not active")
	require.Nil(t, result)
	require.Zero(t, executor.refreshCalls)
	require.Zero(t, repo.updateCalls)
}

func TestRefreshIfNeeded_RequestPathDBRereadRevalidatesExecutorContract(t *testing.T) {
	tests := []struct {
		name          string
		freshPlatform string
		freshType     string
	}{
		{name: "platform changed", freshPlatform: capability.PlatformAnthropic, freshType: capability.ProviderTypeOAuth},
		{name: "type changed", freshPlatform: capability.PlatformGrok, freshType: capability.ProviderTypeUpstream},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &Record{ID: 83, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true}
			freshProvider := &Record{ID: provider.ID, Platform: tt.freshPlatform, Type: tt.freshType, Status: billing.StatusActive, Schedulable: true}
			repo := &refreshAPIProviderRepo{provider: freshProvider}
			executor := NewGrokTokenRefresher(nil)

			api := newRefreshAPIForTest(repo, nil)
			result, err := api.RefreshIfNeeded(WithRefreshRequestPath(context.Background()), provider, executor, 3*time.Minute)

			require.ErrorIs(t, err, ErrRefreshProviderStateChanged)
			require.Nil(t, result)
			require.Zero(t, repo.updateCalls)
		})
	}
}

func TestRefreshIfNeeded_ReleasesDistributedLockAfterParentCancellation(t *testing.T) {
	provider := &Record{ID: 81, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	cache := &refreshAPICacheStub{lockResult: true}
	ctx, cancel := context.WithCancel(context.Background())
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		err:          errors.New("temporary provider error"),
		onRefresh:    cancel,
	}
	api := newRefreshAPIForTest(repo, cache)

	_, err := api.RefreshIfNeeded(ctx, provider, executor, time.Hour)

	require.Error(t, err)
	require.Equal(t, 1, cache.releaseCalls)
	require.NoError(t, cache.releaseCtxErr, "lock cleanup must not reuse the canceled attempt context")
}

func TestRefreshIfNeeded_RevalidatesFreshProviderBeforeRefresh(t *testing.T) {
	selected := &Record{ID: 82, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	tests := []struct {
		name  string
		fresh *Record
	}{
		{name: "converted to API key", fresh: &Record{ID: 82, Platform: capability.PlatformGrok, Type: capability.ProviderTypeAPIKey, Status: billing.StatusActive}},
		{name: "disabled", fresh: &Record{ID: 82, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Status: billing.StatusDisabled}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &refreshAPIProviderRepo{provider: tt.fresh}
			executor := &refreshAPIExecutorStub{
				needsRefresh: true,
				canRefresh: func(provider *Record) bool {
					return provider.Platform == capability.PlatformGrok && provider.Type == capability.ProviderTypeOAuth
				},
			}
			api := newRefreshAPIForTest(repo, nil)

			result, err := api.RefreshIfNeeded(context.Background(), selected, executor, time.Hour)

			require.NoError(t, err)
			require.False(t, result.Refreshed)
			require.Zero(t, executor.refreshCalls)
			require.Zero(t, repo.updateCalls)
		})
	}
}

func TestRefreshIfNeeded_RequestPathDBRereadMissingGrokRefreshCredentialReturnsPermanentSignal(t *testing.T) {
	provider := &Record{
		ID:          84,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"refresh_token": "caller-snapshot-refresh-token",
		},
	}
	freshProvider := &Record{ID: provider.ID, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Schedulable: true}
	repo := &refreshAPIProviderRepo{provider: freshProvider}
	executor := NewGrokTokenRefresher(nil)

	api := newRefreshAPIForTest(repo, nil)
	result, err := api.RefreshIfNeeded(WithRefreshRequestPath(context.Background()), provider, executor, 3*time.Minute)

	require.ErrorIs(t, err, ErrGrokOAuthRefreshTokenMissing)
	require.Nil(t, result)
	require.Zero(t, repo.updateCalls)
}

func TestRefreshIfNeeded_LateSuccessAfterDeadlineDoesNotPersist(t *testing.T) {
	provider := &Record{ID: 85, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "late-token"},
		delay:        30 * time.Millisecond,
	}
	api := newRefreshAPIForTest(repo, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	result, err := api.RefreshIfNeeded(ctx, provider, executor, time.Hour)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, result)
	require.Zero(t, repo.updateCredentialsCalls, "late credentials must not cross the unified API persistence boundary")
}

func TestRefreshIfNeeded_NilCredentials(t *testing.T) {
	provider := &Record{ID: 9, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  nil, // Refresh returns nil credentials
	}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.NoError(t, err)
	require.True(t, result.Refreshed)
	require.Nil(t, result.NewCredentials)
	require.Equal(t, 0, repo.updateCalls) // no DB update when credentials are nil
}

func TestMergeCredentials_Basic(t *testing.T) {
	old := map[string]any{"a": "1", "b": "2", "c": "3"}
	new := map[string]any{"a": "new", "d": "4"}

	result := MergeCredentials(old, new)

	require.Equal(t, "new", result["a"]) // new value preserved
	require.Equal(t, "2", result["b"])   // old value kept
	require.Equal(t, "3", result["c"])   // old value kept
	require.Equal(t, "4", result["d"])   // new value preserved
}

func TestMergeCredentials_NilNew(t *testing.T) {
	old := map[string]any{"a": "1"}

	result := MergeCredentials(old, nil)

	require.NotNil(t, result)
	require.Equal(t, "1", result["a"])
}

func TestMergeCredentials_NilOld(t *testing.T) {
	new := map[string]any{"a": "1"}

	result := MergeCredentials(nil, new)

	require.Equal(t, "1", result["a"])
}

func TestMergeCredentials_BothNil(t *testing.T) {
	result := MergeCredentials(nil, nil)
	require.NotNil(t, result)
	require.Empty(t, result)
}

func TestMergeCredentials_NewOverridesOld(t *testing.T) {
	old := map[string]any{"access_token": "old-token", "refresh_token": "old-refresh"}
	new := map[string]any{"access_token": "new-token"}

	result := MergeCredentials(old, new)

	require.Equal(t, "new-token", result["access_token"])    // overridden
	require.Equal(t, "old-refresh", result["refresh_token"]) // preserved
}

func (r *refreshAPIProviderRepoWithRace) GetByID(_ context.Context, _ int64) (*Record, error) {
	r.getByIDCalls++
	if r.getByIDCalls > 1 && r.raceProvider != nil {
		return activeRefreshAPITestProvider(r.raceProvider), nil
	}
	if r.getByIDErr != nil {
		return nil, r.getByIDErr
	}
	return activeRefreshAPITestProvider(r.provider), nil
}

func TestRefreshIfNeeded_InvalidGrantRaceRecovered(t *testing.T) {
	// The selected provider still holds the pre-refresh token.
	provider := &Record{
		ID:          10,
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "old-rt", "access_token": "old-at"},
	}
	// A concurrent worker has stored rotated tokens in the database.
	racedProvider := &Record{
		ID:          10,
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "new-rt", "access_token": "new-at"},
	}
	repo := &refreshAPIProviderRepoWithRace{
		refreshAPIProviderRepo: refreshAPIProviderRepo{provider: provider},
		raceProvider:           racedProvider,
	}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		err:          errors.New("invalid_grant: refresh token not found or invalid"),
	}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.NoError(t, err, "race-recovered invalid_grant should not return error")
	require.False(t, result.Refreshed)
	require.False(t, result.LockHeld)
	require.NotNil(t, result.Provider)
	require.Equal(t, "new-rt", result.Provider.GetCredential("refresh_token"))
	require.Equal(t, 0, repo.updateCalls) // no DB update needed, another worker did it
}

func TestRefreshIfNeeded_InvalidGrantGenuine(t *testing.T) {
	// The database still contains the revoked refresh token.
	provider := &Record{
		ID:          11,
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "revoked-rt", "access_token": "old-at"},
	}
	repo := &refreshAPIProviderRepoWithRace{
		refreshAPIProviderRepo: refreshAPIProviderRepo{provider: provider},
		raceProvider:           provider, // same refresh_token on re-read
	}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		err:          errors.New("invalid_grant: refresh token revoked"),
	}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.Error(t, err, "genuine invalid_grant should propagate error")
	require.NotNil(t, result)
	require.NotNil(t, result.Provider)
	require.Equal(t, "revoked-rt", result.Provider.GetCredential("refresh_token"))
	require.Contains(t, err.Error(), "invalid_grant")
}

func TestRefreshIfNeeded_InvalidGrantDBRereadFailsOnRecovery(t *testing.T) {
	provider := &Record{
		ID:          12,
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "old-rt"},
	}
	repo := &refreshAPIProviderRepoWithRace{
		refreshAPIProviderRepo: refreshAPIProviderRepo{provider: provider},
		raceProvider:           nil, // No replacement record is supplied; recovery rereads the original provider.
	}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		err:          errors.New("invalid_grant"),
	}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.Error(t, err, "should propagate error when recovery DB re-read fails")
	require.NotNil(t, result)
	require.NotNil(t, result.Provider)
	require.Equal(t, "old-rt", result.Provider.GetCredential("refresh_token"))
}

func TestRefreshIfNeeded_LocalMutexSerializesConcurrent(t *testing.T) {
	// Two goroutines refreshing one provider serialize through the local lock.
	// After the first exchange, NeedsRefresh returns false for the queued call.
	refreshed := &Record{
		ID:          20,
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "new-rt", "access_token": "new-at"},
	}
	callCount := 0
	repo := &refreshAPIProviderRepo{provider: &Record{
		ID:          20,
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "old-rt"},
	}}

	// The exchange counter makes subsequent eligibility checks return false.
	var mu sync.Mutex
	dynamicExecutor := &dynamicRefreshExecutor{
		canRefresh: true,
		cacheKey:   "test:mutex:anthropic",
		refreshFunc: func(_ context.Context, _ *Record) (map[string]any, error) {
			mu.Lock()
			callCount++
			mu.Unlock()
			time.Sleep(50 * time.Millisecond) // slow refresh
			return map[string]any{"access_token": "new-at"}, nil
		},
		needsRefreshFunc: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return callCount == 0 // only first call needs refresh
		},
	}

	_ = refreshed

	api := newRefreshAPIForTest(repo, nil) // no distributed lock, only local mutex

	var wg sync.WaitGroup
	results := make([]*OAuthRefreshResult, 2)
	errs := make([]error, 2)

	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = api.RefreshIfNeeded(context.Background(), repo.provider, dynamicExecutor, 3*time.Minute)
		}(i)
	}
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	// Both callers complete, but only one executes the exchange.
	mu.Lock()
	require.Equal(t, 1, callCount, "only one refresh call should have been made")
	mu.Unlock()
}

func TestRefreshIfNeeded_LocalLockWaitHonorsContextCancellation(t *testing.T) {
	provider := &Record{ID: 21, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var once sync.Once
	executor := &dynamicRefreshExecutor{
		canRefresh:       true,
		cacheKey:         "test:context-lock:grok",
		needsRefreshFunc: func() bool { return true },
		refreshFunc: func(context.Context, *Record) (map[string]any, error) {
			once.Do(func() { close(refreshStarted) })
			<-releaseRefresh
			return map[string]any{"access_token": "new-at"}, nil
		},
	}
	api := newRefreshAPIForTest(repo, nil)
	firstDone := make(chan error, 1)
	go func() {
		_, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)
		firstDone <- err
	}()
	<-refreshStarted

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	result, err := api.RefreshIfNeeded(ctx, provider, executor, 3*time.Minute)

	require.Nil(t, result)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(startedAt), 500*time.Millisecond)
	close(releaseRefresh)
	require.NoError(t, <-firstDone)
}

func TestRefreshIfNeeded_ReleasesDistributedLockWithCleanupContext(t *testing.T) {
	provider := &Record{
		ID:       22,
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
		Credentials: map[string]any{
			"access_token":  "old-access",
			"refresh_token": "old-refresh",
		},
	}
	repo := &refreshAPIProviderRepo{provider: provider}
	cache := &refreshAPICacheStub{lockResult: true}
	ctx, cancel := context.WithCancel(context.Background())
	executor := &dynamicRefreshExecutor{
		canRefresh:       true,
		cacheKey:         "test:cleanup:grok",
		needsRefreshFunc: func() bool { return true },
		refreshFunc: func(context.Context, *Record) (map[string]any, error) {
			cancel()
			return map[string]any{"access_token": "new-at"}, nil
		},
	}
	api := newRefreshAPIForTest(repo, cache)

	result, err := api.RefreshIfNeeded(ctx, provider, executor, 3*time.Minute)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, result)
	require.Zero(t, repo.updateCalls)
	require.Equal(t, "old-access", provider.GetGrokAccessToken())
	require.Zero(t, provider.GetCredentialAsInt64("_token_version"))
	require.Equal(t, 1, cache.releaseCalls)
	require.NoError(t, cache.releaseCtxErr)
}

func (e *dynamicRefreshExecutor) CanRefresh(_ *Record) bool { return e.canRefresh }

func (e *dynamicRefreshExecutor) NeedsRefresh(_ *Record, _ time.Duration) bool {
	return e.needsRefreshFunc()
}

func (e *dynamicRefreshExecutor) Refresh(ctx context.Context, provider *Record) (map[string]any, error) {
	return e.refreshFunc(ctx, provider)
}

func (e *dynamicRefreshExecutor) CacheKey(_ *Record) string {
	return e.cacheKey
}

func (r *refreshAPIProviderRepo) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version CredentialVersion, credentials map[string]any) (bool, error) {
	current := activeRefreshAPITestProvider(r.provider)
	if current == nil || current.ID != version.ID || current.Platform != version.Platform || current.Type != version.Type || current.Status != version.Status || !reflect.DeepEqual(querycache.ShallowMap(current.Credentials), version.Credentials) || !reflect.DeepEqual(current.ProxyID, version.ProxyID) {
		return false, nil
	}
	err := r.UpdateCredentials(ctx, version.ID, credentials)
	return err == nil, err
}

// TestRefreshResultDoesNotShareExecutorValues checks that nested executor credentials are independent of
// returned values.
func TestRefreshResultDoesNotShareExecutorValues(t *testing.T) {
	value := &Record{ID: 1001, Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: value}
	credentials := map[string]any{"access_token": "new", "session": map[string]any{"cookie": "initial"}}
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: credentials}
	result, err := newRefreshAPIForTest(repo, nil).RefreshIfNeeded(context.Background(), value, executor, time.Minute)
	require.NoError(t, err)
	require.True(t, result.Refreshed)
	session, ok := result.NewCredentials["session"].(map[string]any)
	require.True(t, ok)
	session["cookie"] = "caller"
	original, ok := credentials["session"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "initial", original["cookie"])
	returned, ok := result.Provider.Credentials["session"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "initial", returned["cookie"])
}

// newRefreshAPIForTest builds a coordinator with test logging and platform policy.
func newRefreshAPIForTest(repo RefreshRepository, cache AccessTokenCache, lockTTL ...time.Duration) *OAuthRefreshAPI {
	options := RefreshOptions{Now: time.Now, Warn: slog.Warn, Info: slog.Info, Error: slog.Error, Platform: ProviderRefreshPlatformPolicy()}
	if len(lockTTL) > 0 {
		options.LockTTL = lockTTL[0]
	}
	return NewOAuthRefreshAPI(repo, cache, options)
}

// TestRefreshPreservesAdministratorCredentials simulates reauthorization after exchange begins but before persistence.
func TestRefreshPreservesAdministratorCredentials(t *testing.T) {
	for _, platform := range []string{capability.PlatformOpenAI, capability.PlatformAnthropic, capability.PlatformGemini, capability.PlatformAntigravity} {
		t.Run(platform, func(t *testing.T) {
			original := &Record{ID: 1, Platform: platform, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive, Credentials: map[string]any{"refresh_token": "before", "access_token": "before"}}
			repo := &refreshAPIProviderRepo{provider: original}
			executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"refresh_token": "rotated-old", "access_token": "rotated-old"}, onRefresh: func() {
				current := *original
				current.Credentials = map[string]any{"refresh_token": "administrator-new", "access_token": "administrator-new"}
				repo.provider = &current
			}}
			api := newRefreshAPIForTest(repo, nil)
			result, err := api.RefreshIfNeeded(context.Background(), original, executor, time.Minute)
			require.NoError(t, err)
			require.False(t, result.Refreshed)
			require.Nil(t, result.NewCredentials)
			require.Equal(t, "administrator-new", result.Provider.Credentials["refresh_token"])
			require.Equal(t, "administrator-new", repo.provider.Credentials["refresh_token"])
			require.Equal(t, 1, executor.refreshCalls)
		})
	}
}

func TestRefreshIfNeeded_LocalLockWaitHonorsContext(t *testing.T) {
	provider := &Record{ID: 80, Platform: PlatformGrok, Type: ProviderTypeOAuth, Status: StatusActive}
	repo := &refreshCoreRepo{}
	executor := &refreshCoreExecutor{}
	api := NewOAuthRefreshAPI(repo, nil, RefreshOptions{})
	lock := api.getLocalLock(executor.CacheKey(provider))
	require.NoError(t, lock.Lock(context.Background()))
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	result, err := api.RefreshIfNeeded(ctx, provider, executor, time.Hour)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, result)
	require.Zero(t, executor.refreshCalls)
}

func (*refreshCoreRepo) GetByID(context.Context, int64) (*Record, error) {
	panic("等待锁时不应访问仓储")
}

func (*refreshCoreExecutor) CanRefresh(*Record) bool { return true }

func (*refreshCoreExecutor) NeedsRefresh(*Record, time.Duration) bool { return true }

func (*refreshCoreExecutor) CacheKey(*Record) string { return "test:refresh" }

func (e *refreshCoreExecutor) Refresh(context.Context, *Record) (map[string]any, error) {
	e.refreshCalls++
	return nil, nil
}

// TestRefreshAttemptSnapshotIsolation checks that comparison snapshots retain pre-exchange nested credentials.
func TestRefreshAttemptSnapshotIsolation(t *testing.T) {
	credentials := map[string]any{"refresh_token": "initial", "session": map[string]any{"cookie": "initial"}}
	provider := &Record{ID: 1, Platform: PlatformOpenAI, Type: ProviderTypeOAuth, Credentials: credentials}
	attempted := snapshotRefreshRecord(provider)
	session, ok := credentials["session"].(map[string]any)
	require.True(t, ok)
	session["cookie"] = "changed-during-exchange"
	expected, ok := attempted.Credentials["session"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "initial", expected["cookie"])
}
