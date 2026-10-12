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

// The refresh coordinator needs to distinguish contention, current credentials,
// rejected exchanges, persistence conflicts and canceled work. Configurable
// repository, executor and cache fixtures expose those results so assertions can
// check exchanged values, write counts, cleanup contexts and returned provider
// state.
//
// Race fixtures substitute later provider reads or interleave reauthorization
// immediately before comparison. Concurrent fixtures share one key and join
// callers before counting exchanges, while delayed executors intentionally
// ignore cancellation to check discarded late credentials. Credential-copy
// assertions cover their selected nested maps. The stores model logical
// comparison and shallow fixture storage; transaction ownership and Redis
// socket timeouts follow their production storage and client implementations.

// refreshAPIProviderRepo models counted provider reads and conditional
// credential writes with injected failures.
type refreshAPIProviderRepo struct {
	// provider is borrowed by readers when its status is present; empty status
	// creates a shallow active copy. Fixture callers serialize shared row access.
	provider     *Record
	getByIDErr   error // injected ordinary provider read failure
	getByIDCalls int   // counted fixture provider reads
	// getByIDErrAfterCall enables the later read error when positive; GetByID uses
	// that error from this numbered call onward before its ordinary injected error.
	getByIDErrAfterCall int
	// getByIDErrAfterCallErr is returned after the enabled read threshold,
	// including nil if a fixture configures that combination; the row result is
	// then nil.
	getByIDErrAfterCallErr error
	updateErr              error // injected credential write failure
	updateCalls            int   // counted attempted fixture writes
	updateCredentialsCalls int   // counted credential replacement attempts
	successCASCalls        int   // counted Grok comparison calls
	// beforeSuccessCAS runs after attempted credentials and proxy are captured and
	// before comparison. Tests can replace administrator state here to force a lost
	// Grok success comparison.
	beforeSuccessCAS func(*refreshAPIProviderRepo)
	// lastExpectedCredentials copies the top-level comparison map while retaining
	// nested references. Tests keep its captured values stable while inspecting
	// attempted token identity.
	lastExpectedCredentials map[string]any
	// lastExpectedProxyID copies the attempted proxy scalar when supplied; nil
	// represents a comparison made for a provider without a selected proxy.
	lastExpectedProxyID *int64
}

// refreshAPIExecutorStub supplies configurable eligibility, borrowed exchange
// values and interleaving callbacks.
type refreshAPIExecutorStub struct {
	needsRefresh  bool // configured expiry check result
	cannotRefresh bool // forced credential eligibility rejection
	// credentials is returned by the executor as a borrowed map; the coordinator
	// copies supported containers before versioning, while the fixture does not
	// detach its result.
	credentials  map[string]any
	err          error              // injected exchange failure result
	refreshCalls int                // counted fixture exchange calls
	canRefresh   func(*Record) bool // optional credential eligibility callback
	onRefresh    func()             // optional pre-result interleaving callback
	// delay sleeps before the interleaving callback and exchange result, ignoring
	// cancellation so late-response rejection can be exercised by the
	// coordinator.
	delay time.Duration
}

// refreshAPICacheStub records cache cleanup contexts and supplies configured
// distributed lock results.
type refreshAPICacheStub struct {
	lockResult    bool   // configured distributed lease acquisition
	lockErr       error  // injected lease acquisition failure
	releaseCalls  int    // counted distributed lease releases
	releaseCtxErr error  // context error observed at release
	deleteCalls   int    // counted access-token deletion requests
	deleteKey     string // last token deletion key
	deleteCtxErr  error  // context error observed at deletion
}

// refreshAPIProviderRepoWithRace substitutes a supplied later row to model
// invalid-grant recovery.
type refreshAPIProviderRepoWithRace struct {
	refreshAPIProviderRepo // shared credential write fixture
	// raceProvider replaces the row after the first read when nonnil. With no
	// replacement, GetByID uses its ordinary injected error or the original row;
	// nil alone does not simulate a failed recovery read.
	raceProvider *Record
	getByIDCalls int // counted fixture provider reads
}

// dynamicRefreshExecutor binds per-test eligibility and exchange callbacks for
// coordinated concurrency fixtures.
type dynamicRefreshExecutor struct {
	canRefresh bool   // configured credential eligibility result
	cacheKey   string // shared fixture refresh lock key
	// needsRefreshFunc and refreshFunc are callable callbacks supplied by each
	// fixture. Callers synchronize their captured state while the coordinator
	// invokes them through the same shared refresh key.
	needsRefreshFunc func() bool // configured expiry decision callback
	// configured exchange execution callback
	refreshFunc func(context.Context, *Record) (map[string]any, error)
}

// refreshCoreRepo fails the fixture if storage is read before a canceled lock
// wait ends.
type refreshCoreRepo struct{}

// refreshCoreExecutor counts exchanges for a lock-wait fixture that should never
// reach upstream work.
type refreshCoreExecutor struct {
	refreshCalls int /* counted fixture exchange calls */
}

// TestIsInvalidGrantError checks lowercase and uppercase rejected-token text
// alongside an unrelated client error. Recognized strings match, while the
// unrelated error and nil return false.
func TestIsInvalidGrantError(t *testing.T) {
	require.True(t, IsInvalidGrantError(errors.New("invalid_grant: token revoked")))
	require.True(t, IsInvalidGrantError(errors.New("INVALID_GRANT")))
	require.False(t, IsInvalidGrantError(errors.New("invalid_client")))
	require.False(t, IsInvalidGrantError(nil))
}

// GetByID counts fixture reads and returns configured failures before its
// current provider. A positive later-error threshold takes precedence from that
// numbered call onward; otherwise empty status selects a shallow active row
// copy.
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

// activeRefreshAPITestProvider returns the input when nil or already carrying a
// status. Empty status creates a shallow record copy marked active, leaving the
// input status unchanged while its mutable data stays shared.
func activeRefreshAPITestProvider(provider *Record) *Record {
	if provider == nil || provider.Status != "" {
		return provider
	}
	copy := *provider
	copy.Status = billing.StatusActive
	return &copy
}

// Update counts a whole-record fixture write and returns the configured update
// error.
func (r *refreshAPIProviderRepo) Update(_ context.Context, _ *Record) error {
	r.updateCalls++
	return r.updateErr
}

// UpdateCredentials counts an attempted credential replacement and returns an
// injected error before mutation. Otherwise it creates a row when the ID changes
// and stores a shallow top-level copy whose nested values remain shared.
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

// UpdateGrokOAuthCredentialsIfUnchanged captures attempted credentials and proxy
// before the optional administrator interleaving callback. It compares the
// fixture's ID, Grok platform, OAuth kind, complete credentials and proxy,
// returning false for conflicts.
//
// Captured proxy values are detached, while captured maps and replacements are
// shallow copies. The callback runs before the configured write error and
// comparison so fixtures can distinguish failed storage from a newer identity
// winning. Matching credentials increment write counters and replace the stored
// top-level map. Callers serialize these fields through coordinator locking;
// this helper models comparison outcomes without database transaction
// guarantees.
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
	// Invoke the administrator interleaving after capture and before comparison
	// so the expected proxy and credentials still describe the exchange's input.
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

// CanRefresh applies forced rejection before an optional eligibility callback,
// otherwise admitting the fixture provider. Tests keep these captured settings
// stable while the coordinator performs its current-row eligibility checks.
func (e *refreshAPIExecutorStub) CanRefresh(provider *Record) bool {
	if e.cannotRefresh {
		return false
	}
	if e.canRefresh != nil {
		return e.canRefresh(provider)
	}
	return true
}

// NeedsRefresh returns the configured expiry decision for every supplied fixture
// provider.
func (e *refreshAPIExecutorStub) NeedsRefresh(_ *Record, _ time.Duration) bool {
	return e.needsRefresh
}

// Refresh counts an exchange, sleeps for delay, runs onRefresh, then returns the
// configured error or borrowed credentials. Its sleep and callback ignore the
// passed context, letting tests exercise rejected late responses and interleaved
// updates.
func (e *refreshAPIExecutorStub) Refresh(_ context.Context, _ *Record) (map[string]any, error) {
	e.refreshCalls++
	if e.delay > 0 {
		// Ignore context during this configured delay so the coordinator, rather
		// than the fixture transport, must reject credentials that arrive after
		// its deadline.
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

// CacheKey returns a platform-wide fixture key, requiring a nonnil provider
// record. All records on that platform use the same test lock rather than the
// production provider-ID key scheme.
func (e *refreshAPIExecutorStub) CacheKey(provider *Record) string {
	return "test:api:" + provider.Platform
}

// GetAccessToken returns an empty token with no error for the cache fixture.
func (c *refreshAPICacheStub) GetAccessToken(context.Context, string) (string, error) {
	return "", nil
}

// SetAccessToken accepts the fixture write without storing any supplied token
// value.
func (c *refreshAPICacheStub) SetAccessToken(context.Context, string, string, time.Duration) error {
	return nil
}

// DeleteAccessToken records the call count, key and passed context error, then
// succeeds. Tests pass a nonnil context and inspect these values after the
// synchronous cleanup call returns.
func (c *refreshAPICacheStub) DeleteAccessToken(ctx context.Context, key string) error {
	c.deleteCalls++
	c.deleteKey = key
	c.deleteCtxErr = ctx.Err()
	return nil
}

// AcquireRefreshLock returns the configured lease acquisition and failure
// results for the fixture.
func (c *refreshAPICacheStub) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return c.lockResult, c.lockErr
}

// ReleaseRefreshLock counts release and records the supplied nonnil context's
// current error before succeeding. These observations check cleanup-context
// propagation, while the fixture performs no Redis socket I/O.
func (c *refreshAPICacheStub) ReleaseRefreshLock(ctx context.Context, _ string) error {
	c.releaseCalls++
	c.releaseCtxErr = ctx.Err()
	return nil
}

// TestRefreshIfNeeded_Success performs one eligible exchange with an acquired
// cache lease. It checks versioned returned credentials, one credential write,
// one release and one exchange call.
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
	require.NotNil(t, result.NewCredentials["_token_version"])
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 1, repo.updateCredentialsCalls)
	require.Equal(t, 1, cache.releaseCalls)
	require.Equal(t, 1, executor.refreshCalls)
}

// TestRefreshIfNeeded_UpdateCredentialsPreservesRateLimitState refreshes a
// Gemini fixture with an existing future rate-limit reset. Its
// credential-specific write succeeds and the asserted reset timestamp remains
// within the fixture tolerance.
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
	// The assertion allows a one-second reset difference; larger changes would
	// fail this fixture even though it primarily exercises credential-specific
	// writes.
	require.WithinDuration(t, resetAt, *repo.provider.RateLimitResetAt, time.Second)
}

// TestRefreshIfNeeded_LockHeld supplies distributed contention before the
// provider reread or exchange. The result marks LockHeld, leaves Refreshed
// false, and records zero credential writes or exchanges.
func TestRefreshIfNeeded_LockHeld(t *testing.T) {
	provider := &Record{ID: 2, Platform: capability.PlatformAnthropic, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	cache := &refreshAPICacheStub{lockResult: false}
	executor := &refreshAPIExecutorStub{needsRefresh: true}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.NoError(t, err)
	require.True(t, result.LockHeld)
	require.False(t, result.Refreshed)
	require.Equal(t, 0, repo.updateCalls)
	require.Equal(t, 0, executor.refreshCalls)
}

// TestRefreshIfNeeded_LockErrorDegrades injects a cache acquisition error while
// local coordination remains available. Exchange and credential write still
// succeed once, and no distributed release is attempted.
func TestRefreshIfNeeded_LockErrorDegrades(t *testing.T) {
	provider := &Record{ID: 3, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	cache := &refreshAPICacheStub{lockErr: errors.New("redis down")}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  map[string]any{"access_token": "degraded-token"},
	}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.NoError(t, err)
	require.True(t, result.Refreshed)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 0, cache.releaseCalls)
	require.Equal(t, 1, executor.refreshCalls)
}

// TestRefreshIfNeeded_NoCacheNoLock refreshes with the distributed cache
// dependency omitted. The call reports completed refresh and one credential
// write through the coordinator's process-local locking path.
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

// TestRefreshIfNeeded_AlreadyRefreshed makes the current expiry decision reject
// another exchange after rereading. The returned provider remains available with
// both flags false, and no write or exchange occurs.
func TestRefreshIfNeeded_AlreadyRefreshed(t *testing.T) {
	provider := &Record{ID: 5, Platform: capability.PlatformAnthropic, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{needsRefresh: false}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.NoError(t, err)
	require.False(t, result.Refreshed)
	require.False(t, result.LockHeld)
	require.NotNil(t, result.Provider)
	require.Equal(t, 0, repo.updateCalls)
	require.Equal(t, 0, executor.refreshCalls)
}

// TestRefreshIfNeeded_RefreshError rejects exchange with unchanged invalid-grant
// credentials. The error and attempted provider are returned, no write occurs,
// and the acquired distributed lease is released.
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
	require.Equal(t, 0, repo.updateCalls)
	require.Equal(t, 1, cache.releaseCalls)
}

// TestRefreshIfNeeded_DBUpdateError injects a credential-write error after a
// successful Gemini exchange. It returns ErrRefreshCredentialPersist with a nil
// result and counts the attempted write.
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
	require.Equal(t, 1, repo.updateCalls)
}

// TestRefreshIfNeeded_GrokSuccessCASLetsConcurrentReauthorizationWin replaces
// Grok credentials and proxy just before success comparison. The attempted
// identity is captured, newer state is returned, and exchanged values cause no
// credential write.
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

// TestRefreshIfNeeded_GrokSuccessPersistenceFailureIsProviderContainment fails
// Grok success persistence after exchange. It returns the containment wrapper
// and nil result, retains the attempted input token, and counts no applied
// credential replacement.
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

// TestRefreshIfNeeded_GrokSuccessDurableRereadFailureIsProviderContainment fails
// the durable Grok reread after a matched success write. It checks containment,
// two reads and one access-token deletion whose passed context is live.
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

// TestRefreshIfNeeded_DBRereadFails injects the initial current-state repository
// error. It returns RefreshStateUnavailableError with a nil result, performs no
// exchange or write, and releases the acquired lease.
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

// TestRefreshIfNeeded_RequestPathDBRereadNilFailsClosed supplies a missing
// current row to marked request refresh. It returns
// ErrRefreshProviderStateChanged with a nil result, no exchange or write, and a
// released lease.
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

// TestRefreshIfNeeded_RequestPathDBRereadInactiveFailsClosed rereads a disabled
// Grok row during marked request refresh. The call reports lost active status,
// returns a nil result, and performs no exchange or write.
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

// TestRefreshIfNeeded_RequestPathDBRereadRevalidatesExecutorContract changes
// platform or credential kind before marked Grok refresh rereads. Both cases
// return changed-state failure with a nil result and no credential write.
func TestRefreshIfNeeded_RequestPathDBRereadRevalidatesExecutorContract(t *testing.T) {
	tests := []struct {
		name          string // changed-role fixture case label
		freshPlatform string // platform supplied by reread
		freshType     string // credential kind supplied by reread
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

// TestRefreshIfNeeded_ReleasesDistributedLockAfterParentCancellation cancels the
// parent from an exchange returning an error. The call fails, and the
// lease-release fixture records one call with a live cleanup context.
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

// TestRefreshIfNeeded_RevalidatesFreshProviderBeforeRefresh rereads a converted
// API-key row or disabled provider during background work. Both cases return an
// unrefreshed result with no exchange or credential write.
func TestRefreshIfNeeded_RevalidatesFreshProviderBeforeRefresh(t *testing.T) {
	selected := &Record{ID: 82, Platform: capability.PlatformGrok, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	tests := []struct {
		name  string  // changed-state fixture case label
		fresh *Record // current record supplied by reread
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

// TestRefreshIfNeeded_RequestPathDBRereadMissingGrokRefreshCredentialReturnsPermanentSignal
// rereads an eligible Grok row whose refresh credential disappeared. The marked
// request receives ErrGrokOAuthRefreshTokenMissing and no result, and records no
// credential write.
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

// TestRefreshIfNeeded_LateSuccessAfterDeadlineDoesNotPersist uses an executor
// that sleeps beyond its context deadline and returns credentials. The
// coordinator returns that deadline error with a nil result and no credential
// write.
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

// TestRefreshIfNeeded_NilCredentials completes an eligible exchange with nil
// credentials and no error. The result marks Refreshed while NewCredentials
// remains nil and storage receives no write.
func TestRefreshIfNeeded_NilCredentials(t *testing.T) {
	provider := &Record{ID: 9, Platform: capability.PlatformGemini, Type: capability.ProviderTypeOAuth, Status: billing.StatusActive}
	repo := &refreshAPIProviderRepo{provider: provider}
	cache := &refreshAPICacheStub{lockResult: true}
	executor := &refreshAPIExecutorStub{
		needsRefresh: true,
		credentials:  nil,
	}

	api := newRefreshAPIForTest(repo, cache)
	result, err := api.RefreshIfNeeded(context.Background(), provider, executor, 3*time.Minute)

	require.NoError(t, err)
	require.True(t, result.Refreshed)
	require.Nil(t, result.NewCredentials)
	require.Equal(t, 0, repo.updateCalls)
}

// TestMergeCredentials_Basic combines overlapping and disjoint old and new
// credential keys. Supplied new values take precedence, and omitted old values
// remain present in the returned map.
func TestMergeCredentials_Basic(t *testing.T) {
	old := map[string]any{"a": "1", "b": "2", "c": "3"}
	new := map[string]any{"a": "new", "d": "4"}

	result := MergeCredentials(old, new)

	require.Equal(t, "new", result["a"])
	require.Equal(t, "2", result["b"])
	require.Equal(t, "3", result["c"])
	require.Equal(t, "4", result["d"])
}

// TestMergeCredentials_NilNew merges an old credential map into a nil
// destination. The returned map is allocated and contains the asserted inherited
// credential value from the old map.
func TestMergeCredentials_NilNew(t *testing.T) {
	old := map[string]any{"a": "1"}

	result := MergeCredentials(old, nil)

	require.NotNil(t, result)
	require.Equal(t, "1", result["a"])
}

// TestMergeCredentials_NilOld merges a supplied new credential map with nil old
// credentials. The returned credential value remains the one supplied by the new
// map in the fixture.
func TestMergeCredentials_NilOld(t *testing.T) {
	new := map[string]any{"a": "1"}

	result := MergeCredentials(nil, new)

	require.Equal(t, "1", result["a"])
}

// TestMergeCredentials_BothNil merges two nil credential maps through the
// allocation path. The returned map is nonnil and empty, protecting the result
// shape when both inputs are absent.
func TestMergeCredentials_BothNil(t *testing.T) {
	result := MergeCredentials(nil, nil)
	require.NotNil(t, result)
	require.Empty(t, result)
}

// TestMergeCredentials_NewOverridesOld supplies a new access token while
// omitting the old refresh token. The returned map contains the new access token
// and retains the old refresh token.
func TestMergeCredentials_NewOverridesOld(t *testing.T) {
	old := map[string]any{"access_token": "old-token", "refresh_token": "old-refresh"}
	new := map[string]any{"access_token": "new-token"}

	result := MergeCredentials(old, new)

	require.Equal(t, "new-token", result["access_token"])
	require.Equal(t, "old-refresh", result["refresh_token"])
}

// GetByID returns a configured replacement after the first read, before
// considering the ordinary injected read error. With no replacement, the
// injected error takes precedence over returning the original provider through
// activeRefreshAPITestProvider, including on recovery rereads.
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

// TestRefreshIfNeeded_InvalidGrantRaceRecovered returns a rotated refresh token
// on the recovery reread after rejection. The call succeeds with an unrefreshed
// result containing that replacement token and no local write.
func TestRefreshIfNeeded_InvalidGrantRaceRecovered(t *testing.T) {
	// The initial row carries the rejected token so recovery needs a later
	// credential observation, not another exchange of this same refresh value.
	provider := &Record{
		ID:          10,
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "old-rt", "access_token": "old-at"},
	}
	// Supply this row on the recovery reread to model another worker's completed
	// rotation; accepting it should return current credentials without local
	// persistence.
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
	require.Equal(t, 0, repo.updateCalls)
}

// TestRefreshIfNeeded_InvalidGrantGenuine returns the same rejected refresh
// token on the recovery reread. The invalid-grant error remains visible, with a
// retained attempted provider containing the asserted token.
func TestRefreshIfNeeded_InvalidGrantGenuine(t *testing.T) {
	// Keep the same token on the recovery reread so an invalid-grant error
	// cannot become success merely because the provider can still be loaded.
	provider := &Record{
		ID:          11,
		Platform:    capability.PlatformAnthropic,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Credentials: map[string]any{"refresh_token": "revoked-rt", "access_token": "old-at"},
	}
	repo := &refreshAPIProviderRepoWithRace{
		refreshAPIProviderRepo: refreshAPIProviderRepo{provider: provider},
		raceProvider:           provider,
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

// TestRefreshIfNeeded_InvalidGrantDBRereadFailsOnRecovery supplies no
// replacement row to the race repository, which therefore rereads its original
// token. Recovery remains unsuccessful and returns the attempted provider; no
// read error is injected.
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

// TestRefreshIfNeeded_LocalMutexSerializesConcurrent starts two callers using
// the same fixture key and a shared expiry decision. Both joined results
// succeed, while the asserted exchange counter records one call.
func TestRefreshIfNeeded_LocalMutexSerializesConcurrent(t *testing.T) {
	// Both callers share this key and the later expiry check observes the
	// synchronized exchange counter, making the second exchange unnecessary.
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

	// Protect the exchange counter used by NeedsRefresh so the later lock owner
	// can observe completed exchange state independently of repository
	// credentials.
	var mu sync.Mutex
	dynamicExecutor := &dynamicRefreshExecutor{
		canRefresh: true,
		cacheKey:   "test:mutex:anthropic",
		refreshFunc: func(_ context.Context, _ *Record) (map[string]any, error) {
			mu.Lock()
			callCount++
			mu.Unlock()
			// Extend the first exchange before its lock release so another caller
			// can remain queued while the shared expiry counter already records
			// work.
			time.Sleep(50 * time.Millisecond)
			return map[string]any{"access_token": "new-at"}, nil
		},
		needsRefreshFunc: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return callCount == 0
		},
	}

	_ = refreshed

	api := newRefreshAPIForTest(repo, nil) // no distributed lock, only local mutex

	// Join both callers before inspecting their results so successful returns
	// and the exchange count describe completed work through the shared local
	// key.
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

	// Joined caller results make the final exchange count observable after both
	// returns; the assertion covers one exchange rather than a scheduling
	// overlap.
	mu.Lock()
	require.Equal(t, 1, callCount, "only one refresh call should have been made")
	mu.Unlock()
}

// TestRefreshIfNeeded_LocalLockWaitHonorsContextCancellation holds one exchange
// while a second caller waits with a short deadline. The wait returns its
// deadline error within the asserted tolerance, and the released owner later
// completes successfully.
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
			// Signal only the first owner, then hold its lock through this wait so the
			// second coordinator call can exercise its cancellable admission path.
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
	// The tolerance distinguishes a deadline-limited wait from waiting for the
	// blocked owner, which this test does not release until after the assertion.
	require.Less(t, time.Since(startedAt), 500*time.Millisecond)
	// Release and join the original owner after observing the waiter's deadline
	// so no blocked exchange remains while the fixture concludes.
	close(releaseRefresh)
	require.NoError(t, <-firstDone)
}

// TestRefreshIfNeeded_ReleasesDistributedLockWithCleanupContext cancels during
// an exchange that still returns a token map. No credentials or version are
// stored, and the release fixture receives one live cleanup context.
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

// CanRefresh returns the captured credential eligibility flag for the dynamic
// fixture.
func (e *dynamicRefreshExecutor) CanRefresh(_ *Record) bool { return e.canRefresh }

// NeedsRefresh invokes the required expiry callback, ignoring the supplied
// provider and window. Fixtures synchronize any mutable callback state
// themselves while coordinator callers can query that decision concurrently.
func (e *dynamicRefreshExecutor) NeedsRefresh(_ *Record, _ time.Duration) bool {
	return e.needsRefreshFunc()
}

// Refresh invokes the required exchange callback with its context and provider.
// The callback supplies its own cancellation, result ownership and
// synchronization behavior for the scenario being exercised.
func (e *dynamicRefreshExecutor) Refresh(ctx context.Context, provider *Record) (map[string]any, error) {
	return e.refreshFunc(ctx, provider)
}

// CacheKey returns the captured shared key used by the dynamic refresh fixture.
func (e *dynamicRefreshExecutor) CacheKey(_ *Record) string {
	return e.cacheKey
}

// UpdateOAuthCredentialsIfUnchanged compares the active-normalized fixture row
// with the captured non-Grok version. A mismatch leaves write counters
// unchanged, while a match delegates shallow credential storage and reports its
// configured error.
//
// Tests serialize comparison and mutation through coordinator locking and their
// interleaving callbacks. The helper normalizes current nil credentials to an
// empty top-level map before reflect.DeepEqual, then compares the supplied
// expectation as it stands. The observed result models this fixture's identity
// gate and counters; PostgreSQL row locks and transaction commits belong to the
// storage implementation.
func (r *refreshAPIProviderRepo) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version CredentialVersion, credentials map[string]any) (bool, error) {
	current := activeRefreshAPITestProvider(r.provider)
	if current == nil || current.ID != version.ID || current.Platform != version.Platform || current.Type != version.Type || current.Status != version.Status || !reflect.DeepEqual(querycache.ShallowMap(current.Credentials), version.Credentials) || !reflect.DeepEqual(current.ProxyID, version.ProxyID) {
		return false, nil
	}
	err := r.UpdateCredentials(ctx, version.ID, credentials)
	return err == nil, err
}

// TestRefreshResultDoesNotShareExecutorValues mutates the returned nested
// session map after successful exchange. The executor's map and the returned
// provider's session retain their original asserted cookie values.
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

// newRefreshAPIForTest binds real clock, logging and platform classification
// with fixture storage. The first optional lease duration is used when supplied;
// other variadic values are ignored and construction applies its usual defaults.
func newRefreshAPIForTest(repo RefreshRepository, cache AccessTokenCache, lockTTL ...time.Duration) *OAuthRefreshAPI {
	options := RefreshOptions{Now: time.Now, Warn: slog.Warn, Info: slog.Info, Error: slog.Error, Platform: ProviderRefreshPlatformPolicy()}
	if len(lockTTL) > 0 {
		options.LockTTL = lockTTL[0]
	}
	return NewOAuthRefreshAPI(repo, cache, options)
}

// TestRefreshPreservesAdministratorCredentials replaces authentication during
// exchanges for four non-Grok OAuth platforms. Each result returns administrator
// credentials with no replacement map, after the single counted exchange.
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

// TestRefreshIfNeeded_LocalLockWaitHonorsContext pre-acquires the fixture key
// before a deadline-limited coordinator call. It receives the deadline error and
// nil result with zero exchanges; any early repository read would panic.
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

// GetByID panics if the lock-wait fixture reaches storage before its waiter is
// canceled. This deliberate failure proves that the tested deadline path stops
// before any current-row reread.
func (*refreshCoreRepo) GetByID(context.Context, int64) (*Record, error) {
	panic("等待锁时不应访问仓储")
}

// CanRefresh admits every candidate for the lock-wait fixture executor.
func (*refreshCoreExecutor) CanRefresh(*Record) bool { return true }

// NeedsRefresh marks every candidate in the lock-wait fixture as requiring
// exchange.
func (*refreshCoreExecutor) NeedsRefresh(*Record, time.Duration) bool { return true }

// CacheKey returns the fixed key held by the lock-wait fixture.
func (*refreshCoreExecutor) CacheKey(*Record) string { return "test:refresh" }

// Refresh counts an unexpectedly reached fixture exchange and returns nil
// credentials successfully.
func (e *refreshCoreExecutor) Refresh(context.Context, *Record) (map[string]any, error) {
	e.refreshCalls++
	return nil, nil
}

// TestRefreshAttemptSnapshotIsolation captures an attempted record before
// mutating its nested source credential map. The asserted snapshot cookie
// retains its earlier value, protecting the tested supported-container
// observation.
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
