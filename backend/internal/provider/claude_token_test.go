package provider

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// claudeTokenCacheStub holds locked token values and counts cache
// interactions.
type claudeTokenCacheStub struct {
	// mu protects tokens across peer publication and source reads. Error and
	// admission configuration stays fixed during activity; counters below use
	// atomic operations.
	mu             sync.Mutex        // token map synchronization mutex
	tokens         map[string]string // stored token text values
	getErr         error             // configured cache read failure
	setErr         error             // configured cache write failure
	deleteErr      error             // configured cache deletion failure
	lockAcquired   bool              // configured successful admission result
	lockErr        error             // configured cache admission failure
	releaseLockErr error             // configured admission release failure
	// getCalled, setCalled, lockCalled, and unlockCalled count operations with
	// atomic writes and reads, including calls that later return a configured
	// error.
	getCalled        int32 // atomic cache read count
	setCalled        int32 // atomic cache write count
	lockCalled       int32 // atomic admission call count
	unlockCalled     int32 // atomic release call count
	simulateLockRace bool  // configured peer admission contention
}

// claudeProviderRepoStub exposes borrowed provider storage for serial token
// tests.
type claudeProviderRepoStub struct {
	// provider is borrowed and replaced by unlocked fixture writes. Tests
	// serialize repository access; GetByID returns this pointer without copying.
	provider     *Record
	getErr       error // configured repository read failure
	updateErr    error // configured repository write failure
	getCalled    int32 // atomic repository read count
	updateCalled int32 // atomic repository write count
}

// claudeOAuthServiceStub returns configured exchange data and counts refresh
// attempts.
type claudeOAuthServiceStub struct {
	// tokenInfo is borrowed on successful exchange. Configure it before use
	// and keep its fields stable while the refresh callback consumes them.
	tokenInfo     *ClaudeTokenInfo
	refreshErr    error // configured token exchange failure
	refreshCalled int32 // atomic token exchange count
}

// claudeExchangeFixture combines configured exchange with Claude eligibility
// and credential merging.
type claudeExchangeFixture struct {
	// exchange is required by Refresh and retains its configured serial fixture.
	// Keep result/error configuration stable while that callback reads it.
	exchange *claudeOAuthServiceStub // borrowed serial exchange fixture
}

// newClaudeTokenCacheStub creates an empty token map with successful refresh
// admission. Tests configure error and contention fields before use; its cache
// values are synchronized but lock admission is a predicate.
func newClaudeTokenCacheStub() *claudeTokenCacheStub {
	return &claudeTokenCacheStub{
		tokens:       make(map[string]string),
		lockAcquired: true,
	}
}

// GetAccessToken counts reads and returns a locked token or the configured
// failure. The context is ignored, missing keys return empty success, and
// returned strings are scalar values.
func (s *claudeTokenCacheStub) GetAccessToken(ctx context.Context, cacheKey string) (string, error) {
	atomic.AddInt32(&s.getCalled, 1)
	if s.getErr != nil {
		return "", s.getErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[cacheKey], nil
}

// SetAccessToken counts writes and stores a token unless its configured error
// wins. The fixture ignores context and lifetime, so it records selected
// values without simulating TTL expiration.
func (s *claudeTokenCacheStub) SetAccessToken(ctx context.Context, cacheKey string, token string, ttl time.Duration) error {
	atomic.AddInt32(&s.setCalled, 1)
	if s.setErr != nil {
		return s.setErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[cacheKey] = token
	return nil
}

// DeleteAccessToken removes the locked token unless the configured deletion
// error wins. This fixture ignores the context; token mutation is synchronized
// and missing keys can be deleted successfully.
func (s *claudeTokenCacheStub) DeleteAccessToken(ctx context.Context, cacheKey string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, cacheKey)
	return nil
}

// AcquireRefreshLock counts admission calls and returns configured contention,
// error, or success. It creates no actual lock and ignores lifetime and
// context; tests set its configuration before concurrent token activity.
func (s *claudeTokenCacheStub) AcquireRefreshLock(ctx context.Context, cacheKey string, ttl time.Duration) (bool, error) {
	atomic.AddInt32(&s.lockCalled, 1)
	if s.lockErr != nil {
		return false, s.lockErr
	}
	if s.simulateLockRace {
		return false, nil
	}
	return s.lockAcquired, nil
}

// ReleaseRefreshLock counts releases and returns the configured release error.
// Admission is a fixture predicate, so this method changes no lock ownership
// and ignores its context and key.
func (s *claudeTokenCacheStub) ReleaseRefreshLock(ctx context.Context, cacheKey string) error {
	atomic.AddInt32(&s.unlockCalled, 1)
	return s.releaseLockErr
}

// GetByID counts observations and returns the configured borrowed record or
// error. The fixture ignores ID and context; callers configure and consume its
// unlocked provider pointer within serial test operations.
func (r *claudeProviderRepoStub) GetByID(ctx context.Context, id int64) (*Record, error) {
	atomic.AddInt32(&r.getCalled, 1)
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.provider, nil
}

// Update counts writes and replaces its provider pointer unless the configured
// error wins. It performs no copy or locking, so tests serialize mutation and
// retain the supplied record after success.
func (r *claudeProviderRepoStub) Update(ctx context.Context, provider *Record) error {
	atomic.AddInt32(&r.updateCalled, 1)
	if r.updateErr != nil {
		return r.updateErr
	}
	r.provider = provider
	return nil
}

// RefreshProviderToken counts exchanges and returns its configured borrowed
// token data or failure. Input record and context are ignored; configure
// result fields before use and keep them stable while the exchange runs.
func (s *claudeOAuthServiceStub) RefreshProviderToken(ctx context.Context, provider *Record) (*ClaudeTokenInfo, error) {
	atomic.AddInt32(&s.refreshCalled, 1)
	if s.refreshErr != nil {
		return nil, s.refreshErr
	}
	return s.tokenInfo, nil
}

// TestClaudeTokenProvider_TokenRefresh places an OAuth token within the
// refresh window and configures a successful exchange. It checks the refreshed
// token and exactly one upstream refresh invocation.
func TestClaudeTokenProvider_TokenRefresh(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	providerRepo := &claudeProviderRepoStub{}
	oauthService := &claudeOAuthServiceStub{
		tokenInfo: &ClaudeTokenInfo{
			AccessToken:  "refreshed-token",
			RefreshToken: "new-refresh-token",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		},
	}

	// The token is within the refresh window.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       102,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "old-refresh-token",
			"expires_at":    expiresAt,
		},
	}
	providerRepo.provider = provider

	tokenSource := newClaudeRefreshSourceFixture(providerRepo, cache, oauthService)

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "refreshed-token", token)
	require.Equal(t, int32(1), atomic.LoadInt32(&oauthService.refreshCalled))
}

// TestClaudeTokenProvider_LockRaceCondition injects failed lock admission
// while a goroutine publishes a peer token. It checks successful return with a
// nonempty token, without asserting the winner or wait duration.
func TestClaudeTokenProvider_LockRaceCondition(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	cache.simulateLockRace = true
	providerRepo := &claudeProviderRepoStub{}

	// The token is near expiry.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       103,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token": "race-token",
			"expires_at":   expiresAt,
		},
	}
	providerRepo.provider = provider

	// Publish a peer token after the caller can begin its cache/lock path.
	// The mutex protects the map while the assertion permits either usable winner.
	cacheKey := ClaudeTokenCacheKey(provider)
	go func() {
		time.Sleep(5 * time.Millisecond)
		cache.mu.Lock()
		cache.tokens[cacheKey] = "winner-token"
		cache.mu.Unlock()
	}()

	tokenSource := newClaudeRefreshSourceFixture(providerRepo, cache, nil)

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.NotEmpty(t, token)
}

// TestClaudeTokenProvider_RefreshError injects upstream refresh failure under
// the default Claude policy. It checks successful return of the existing token
// from the supplied near-expiry record.
func TestClaudeTokenProvider_RefreshError(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	providerRepo := &claudeProviderRepoStub{}
	oauthService := &claudeOAuthServiceStub{
		refreshErr: errors.New("oauth refresh failed"),
	}

	// The token is near expiry.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       111,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "old-refresh-token",
			"expires_at":    expiresAt,
		},
	}
	providerRepo.provider = provider

	tokenSource := newClaudeRefreshSourceFixture(providerRepo, cache, oauthService)

	// Refresh failure returns the existing token under the default policy.
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "old-token", token) // Existing token retained.
}

// TestClaudeTokenProvider_OAuthServiceNotConfigured omits the authorization
// fixture for a near-expiry token. It checks successful use of the existing
// credential through the source constructed without a coordinator callback.
func TestClaudeTokenProvider_OAuthServiceNotConfigured(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	providerRepo := &claudeProviderRepoStub{}

	// The token is near expiry.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       112,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token": "old-token",
			"expires_at":   expiresAt,
		},
	}
	providerRepo.provider = provider

	tokenSource := newClaudeRefreshSourceFixture(providerRepo, cache, nil)

	// An unconfigured OAuth service leaves the existing token usable.
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "old-token", token) // Existing token retained.
}

// TestClaudeTokenProvider_ProviderRepoGetError injects failure during the
// coordinator record reread. It checks retained access and refresh tokens,
// zero exchange calls, and zero writes under the tolerant request policy.
func TestClaudeTokenProvider_ProviderRepoGetError(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	providerRepo := &claudeProviderRepoStub{
		getErr: errors.New("db connection failed"),
	}
	oauthService := &claudeOAuthServiceStub{
		tokenInfo: &ClaudeTokenInfo{
			AccessToken:  "refreshed-token",
			RefreshToken: "new-refresh",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		},
	}

	// The token is near expiry.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       113,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "old-refresh",
			"expires_at":    expiresAt,
		},
	}

	tokenSource := newClaudeRefreshSourceFixture(providerRepo, cache, oauthService)

	// A coordinator read failure returns the existing token under the default
	// policy.
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "old-token", token)
	require.Zero(t, atomic.LoadInt32(&oauthService.refreshCalled))
	require.Zero(t, atomic.LoadInt32(&providerRepo.updateCalled))
	require.Equal(t, "old-refresh", provider.Credentials["refresh_token"])
}

// TestClaudeTokenProvider_ProviderUpdateError injects persistence failure
// after a successful upstream refresh. It checks the old returned token and
// stored credentials alongside exactly one exchange and one attempted write.
func TestClaudeTokenProvider_ProviderUpdateError(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	providerRepo := &claudeProviderRepoStub{
		updateErr: errors.New("db write failed"),
	}
	oauthService := &claudeOAuthServiceStub{
		tokenInfo: &ClaudeTokenInfo{
			AccessToken:  "refreshed-token",
			RefreshToken: "new-refresh",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		},
	}

	// The token is near expiry.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       114,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "old-refresh",
			"expires_at":    expiresAt,
		},
	}
	providerRepo.provider = provider

	tokenSource := newClaudeRefreshSourceFixture(providerRepo, cache, oauthService)

	// Persistence failure must not publish unsaved tokens or mutate the input
	// snapshot.
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "old-token", token)
	require.Equal(t, int32(1), atomic.LoadInt32(&oauthService.refreshCalled))
	require.Equal(t, int32(1), atomic.LoadInt32(&providerRepo.updateCalled))
	require.Equal(t, "old-token", providerRepo.provider.Credentials["access_token"])
	require.Equal(t, "old-refresh", provider.Credentials["refresh_token"])
}

// TestClaudeTokenProvider_RefreshPreservesExistingCredentials refreshes tokens
// on a record containing custom and organization fields. It checks replacement
// access and refresh tokens while both configured non-token credential values
// remain stored.
func TestClaudeTokenProvider_RefreshPreservesExistingCredentials(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	providerRepo := &claudeProviderRepoStub{}
	oauthService := &claudeOAuthServiceStub{
		tokenInfo: &ClaudeTokenInfo{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		},
	}

	// The token is near expiry.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       115,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":  "old-access-token",
			"refresh_token": "old-refresh-token",
			"expires_at":    expiresAt,
			"custom_field":  "should-be-preserved",
			"organization":  "test-org",
		},
	}
	providerRepo.provider = provider

	tokenSource := newClaudeRefreshSourceFixture(providerRepo, cache, oauthService)

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "new-access-token", token)

	// Stored non-token credential fields remain.
	require.Equal(t, "should-be-preserved", providerRepo.provider.Credentials["custom_field"])
	require.Equal(t, "test-org", providerRepo.provider.Credentials["organization"])
	// Exchange results replace the token fields.
	require.Equal(t, "new-access-token", providerRepo.provider.Credentials["access_token"])
	require.Equal(t, "new-refresh-token", providerRepo.provider.Credentials["refresh_token"])
}

// TestClaudeTokenProvider_DoubleCheckCacheAfterLock starts peer cache
// publication alongside a coordinated token lookup. It checks successful
// nonempty return; the assertions do not establish which token or timing wins.
func TestClaudeTokenProvider_DoubleCheckCacheAfterLock(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	providerRepo := &claudeProviderRepoStub{}
	oauthService := &claudeOAuthServiceStub{
		tokenInfo: &ClaudeTokenInfo{
			AccessToken:  "refreshed-token",
			RefreshToken: "new-refresh",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		},
	}

	// The token is near expiry.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       116,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token": "old-token",
			"expires_at":   expiresAt,
		},
	}
	providerRepo.provider = provider
	cacheKey := ClaudeTokenCacheKey(provider)

	// Delay peer publication while the source can enter its coordinator path.
	// Map mutation is locked; this scenario only asserts that lookup returns
	// usable text.
	go func() {
		time.Sleep(5 * time.Millisecond)
		cache.mu.Lock()
		cache.tokens[cacheKey] = "cached-by-other-worker"
		cache.mu.Unlock()
	}()

	tokenSource := newClaudeRefreshSourceFixture(providerRepo, cache, oauthService)

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.NotEmpty(t, token)
}

// TestClaudeTokenProvider_CacheHit prepopulates the token cache with a value
// different from credentials. It checks the cached result, one cache read, and
// no publication of the record token.
func TestClaudeTokenProvider_CacheHit(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	provider := &Record{
		ID:       100,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "db-token",
		},
	}
	cacheKey := ClaudeTokenCacheKey(provider)
	cache.tokens[cacheKey] = "cached-token"

	tokenSource := newClaudeTokenSourceContract(cache)

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "cached-token", token)
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.getCalled))
	require.Equal(t, int32(0), atomic.LoadInt32(&cache.setCalled))
}

// TestClaudeTokenProvider_CacheMiss_FromCredentials supplies valid
// future-expiry credentials to an empty cache. It checks the returned
// credential token and its subsequent presence under the provider cache key.
func TestClaudeTokenProvider_CacheMiss_FromCredentials(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	// The token is outside the refresh window.
	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       101,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "credential-token",
			"expires_at":   expiresAt,
		},
	}

	tokenSource := newClaudeTokenSourceContract(cache)

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "credential-token", token)

	// The credential token is cached.
	cacheKey := ClaudeTokenCacheKey(provider)
	require.Equal(t, "credential-token", cache.tokens[cacheKey])
}

// TestClaudeTokenProvider_NilProvider passes nil into the request token
// source. It checks the provider-is-nil diagnostic and empty token returned by
// this invalid-input guard.
func TestClaudeTokenProvider_NilProvider(t *testing.T) {
	tokenSource := newClaudeTokenSourceContract(nil)

	token, err := tokenSource.GetAccessToken(context.Background(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider is nil")
	require.Empty(t, token)
}

// TestClaudeTokenProvider_WrongPlatform passes an OpenAI OAuth record to the
// Claude source. It checks the platform/type rejection diagnostic and the
// empty token accompanying that error.
func TestClaudeTokenProvider_WrongPlatform(t *testing.T) {
	tokenSource := newClaudeTokenSourceContract(nil)
	provider := &Record{
		ID:       104,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
	}

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not an anthropic oauth or service account")
	require.Empty(t, token)
}

// TestClaudeTokenProvider_WrongProviderType passes an Anthropic API Key record
// to the Claude source. It checks the unsupported platform/type diagnostic and
// empty token returned for this authentication type.
func TestClaudeTokenProvider_WrongProviderType(t *testing.T) {
	tokenSource := newClaudeTokenSourceContract(nil)
	provider := &Record{
		ID:       105,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeAPIKey,
	}

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not an anthropic oauth or service account")
	require.Empty(t, token)
}

// TestClaudeTokenProvider_SetupTokenType passes an Anthropic Setup Token
// record to request token lookup. It checks rejection and an empty result
// despite background Claude refresh accepting that separate type.
func TestClaudeTokenProvider_SetupTokenType(t *testing.T) {
	tokenSource := newClaudeTokenSourceContract(nil)
	provider := &Record{
		ID:       106,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeSetupToken,
	}

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not an anthropic oauth or service account")
	require.Empty(t, token)
}

// TestClaudeTokenProvider_NilCache supplies a future-expiry credential with no
// token cache. It checks that the source successfully returns the stored token
// through its uncached record path.
func TestClaudeTokenProvider_NilCache(t *testing.T) {
	// The token is outside the refresh window.
	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       107,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "nocache-token",
			"expires_at":   expiresAt,
		},
	}

	tokenSource := newClaudeTokenSourceContract(nil)

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "nocache-token", token)
}

// TestClaudeTokenProvider_CacheGetError injects cache read failure for a
// future-expiry record. It checks successful fallback to the stored credential
// token under the configured request-source policy.
func TestClaudeTokenProvider_CacheGetError(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	cache.getErr = errors.New("redis connection failed")

	// The token is outside the refresh window.
	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       108,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "fallback-token",
			"expires_at":   expiresAt,
		},
	}

	tokenSource := newClaudeTokenSourceContract(cache)

	// Cache read failure falls back to the credential token.
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "fallback-token", token)
}

// TestClaudeTokenProvider_CacheSetError injects publication failure after
// reading a valid stored credential. It checks successful return of that token
// despite the cache refusing its write.
func TestClaudeTokenProvider_CacheSetError(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	cache.setErr = errors.New("redis write failed")

	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       109,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "still-works-token",
			"expires_at":   expiresAt,
		},
	}

	tokenSource := newClaudeTokenSourceContract(cache)

	// Cache write failure still returns the token.
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "still-works-token", token)
}

// TestClaudeTokenProvider_MissingAccessToken supplies an expiry value but
// omits the access token. It checks the missing-credential error text and
// empty token after cache lookup misses.
func TestClaudeTokenProvider_MissingAccessToken(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       110,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"expires_at": expiresAt,
			// access_token is absent.
		},
	}

	tokenSource := newClaudeTokenSourceContract(cache)

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.Error(t, err)
	require.Contains(t, err.Error(), "access_token not found")
	require.Empty(t, token)
}

// TestClaudeTokenProvider_TTLCalculation varies future expiry durations using
// a cache fixture that ignores TTL. Each case checks successful lookup and
// stored token value, leaving numeric TTL behavior unasserted.
func TestClaudeTokenProvider_TTLCalculation(t *testing.T) {
	tests := []struct {
		name      string        // token source case name
		expiresIn time.Duration // configured future expiry duration
	}{
		{
			name:      "far_future_expiry",
			expiresIn: 1 * time.Hour,
		},
		{
			name:      "medium_expiry",
			expiresIn: 10 * time.Minute,
		},
		{
			name:      "near_expiry",
			expiresIn: 6 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newClaudeTokenCacheStub()
			expiresAt := time.Now().Add(tt.expiresIn).Format(time.RFC3339)
			provider := &Record{
				ID:       200,
				Platform: capability.PlatformAnthropic,
				Type:     capability.ProviderTypeOAuth,
				Credentials: map[string]any{
					"access_token": "test-token",
					"expires_at":   expiresAt,
				},
			}

			tokenSource := newClaudeTokenSourceContract(cache)

			_, err := tokenSource.GetAccessToken(context.Background(), provider)
			require.NoError(t, err)

			// Check the cached token.
			cacheKey := ClaudeTokenCacheKey(provider)
			require.Equal(t, "test-token", cache.tokens[cacheKey])
		})
	}
}

// TestClaudeTokenProvider_Real_LockFailedWait configures lock contention and
// publishes a peer cache token after a delay. It checks successful nonempty
// return, without requiring the peer value or a measured wait.
func TestClaudeTokenProvider_Real_LockFailedWait(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	cache.lockAcquired = false // Lock acquisition fails.

	// Near expiry triggers lock acquisition.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       300,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "fallback-token",
			"expires_at":   expiresAt,
		},
	}

	// Delay a peer write until the contended source can enter its wait.
	// The fixture locks map mutation while the assertion accepts any nonempty
	// result.
	cacheKey := ClaudeTokenCacheKey(provider)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cache.mu.Lock()
		cache.tokens[cacheKey] = "refreshed-by-other"
		cache.mu.Unlock()
	}()

	tokenSource := newClaudeTokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.NotEmpty(t, token)
}

// TestClaudeTokenProvider_Real_CacheHitAfterWait configures contended
// admission while a goroutine publishes a winner token. It checks successful
// nonempty return, with no assertion about exact token identity or elapsed
// duration.
func TestClaudeTokenProvider_Real_CacheHitAfterWait(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	cache.lockAcquired = false // Lock acquisition fails.

	// The token is near expiry.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       301,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "original-token",
			"expires_at":   expiresAt,
		},
	}

	cacheKey := ClaudeTokenCacheKey(provider)
	// Publish through the fixture mutex after a delay so cache rereads can
	// observe peer activity; the test assertion does not select an exact token.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cache.mu.Lock()
		cache.tokens[cacheKey] = "winner-token"
		cache.mu.Unlock()
	}()

	tokenSource := newClaudeTokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.NotEmpty(t, token)
}

// TestClaudeTokenProvider_Real_NoExpiresAt omits expiry and configures
// contended cache admission without peer publication. It checks successful
// return of the configured stored token after the wait path.
func TestClaudeTokenProvider_Real_NoExpiresAt(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	cache.lockAcquired = false // Simulate a lock held by another worker.

	// expires_at is absent.
	provider := &Record{
		ID:       302,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "no-expiry-token",
		},
	}

	// Return the credential token after lock waiting finishes.
	tokenSource := newClaudeTokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "no-expiry-token", token)
}

// TestClaudeTokenProvider_Real_WhitespaceToken puts whitespace-only text in
// the cache with valid credentials available. It checks that the returned
// token is the configured credential value rather than blank cache text.
func TestClaudeTokenProvider_Real_WhitespaceToken(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	cacheKey := "claude:provider:303"
	cache.tokens[cacheKey] = "   " // Whitespace-only cache entries are treated as empty.

	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       303,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "real-token",
			"expires_at":   expiresAt,
		},
	}

	tokenSource := newClaudeTokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "real-token", token)
}

// TestClaudeTokenProvider_Real_EmptyCredentialToken supplies whitespace-only
// credentials after an empty cache read. It checks the missing-access-token
// error and empty result for this otherwise qualified OAuth record.
func TestClaudeTokenProvider_Real_EmptyCredentialToken(t *testing.T) {
	cache := newClaudeTokenCacheStub()

	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       304,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "   ", // Whitespace only.
			"expires_at":   expiresAt,
		},
	}

	tokenSource := newClaudeTokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.Error(t, err)
	require.Contains(t, err.Error(), "access_token not found")
	require.Empty(t, token)
}

// TestClaudeTokenProvider_Real_LockError injects a refresh admission error for
// a near-expiry token. It checks successful return of the stored fallback
// token through the direct cache-lock path.
func TestClaudeTokenProvider_Real_LockError(t *testing.T) {
	cache := newClaudeTokenCacheStub()
	cache.lockErr = errors.New("redis lock failed")

	// The token is within the refresh window.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       305,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "fallback-on-lock-error",
			"expires_at":   expiresAt,
		},
	}

	tokenSource := newClaudeTokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "fallback-on-lock-error", token)
}

// TestClaudeTokenProvider_Real_NilCredentials supplies a credential map
// containing expiry alone. It checks the missing-access-token error and empty
// result; the configured map itself is nonnil.
func TestClaudeTokenProvider_Real_NilCredentials(t *testing.T) {
	cache := newClaudeTokenCacheStub()

	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       306,
		Platform: capability.PlatformAnthropic,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"expires_at": expiresAt,
			// access_token is absent from credentials.
		},
	}

	tokenSource := newClaudeTokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.Error(t, err)
	require.Contains(t, err.Error(), "access_token not found")
	require.Empty(t, token)
}

// newClaudeTokenSourceContract creates a source with Claude request policy and
// diagnostics. Its optional cache enables token/wait behavior, while absent
// repository and exchange callbacks keep this fixture focused on credential
// lookup.
func newClaudeTokenSourceContract(cache AccessTokenCache) *ClaudeTokenSource {
	return &ClaudeTokenSource{Options: ClaudeTokenOptions{
		Cache: cache, Policy: ClaudeProviderRefreshPolicy(), Debug: slog.Debug, Warn: slog.Warn,
	}}
}

// newClaudeRefreshSourceFixture binds borrowed repository and cache with
// Claude policy. A supplied authorization fixture enables the real
// OAuthRefreshAPI conditional exchange path; nil authorization leaves the
// source without a refresh callback.
func newClaudeRefreshSourceFixture(repo *claudeProviderRepoStub, cache *claudeTokenCacheStub, authorization *claudeOAuthServiceStub) *ClaudeTokenSource {
	source := &ClaudeTokenSource{Options: ClaudeTokenOptions{Repository: repo, Cache: cache, Policy: ClaudeProviderRefreshPolicy(), Debug: slog.Debug, Warn: slog.Warn}}
	if authorization != nil {
		api := NewOAuthRefreshAPI(repo, cache, RefreshOptions{Platform: ProviderRefreshPlatformPolicy()})
		executor := claudeExchangeFixture{authorization}
		source.Options.Refresh = func(ctx context.Context, value *Record, window time.Duration) (*OAuthRefreshResult, error) {
			return api.RefreshIfNeeded(ctx, value, executor, window)
		}
	}
	return source
}

// CanRefresh delegates fixture eligibility to the Claude platform rule.
func (claudeExchangeFixture) CanRefresh(value *Record) bool { return CanRefreshClaude(value) }

// NeedsRefresh delegates stored-expiry qualification with the supplied refresh
// window.
func (claudeExchangeFixture) NeedsRefresh(value *Record, window time.Duration) bool {
	return NeedsRefreshClaude(value, window)
}

// CacheKey delegates the row-based Claude token cache identity rule.
func (claudeExchangeFixture) CacheKey(value *Record) string { return ClaudeTokenCacheKey(value) }

// Refresh delegates Claude credential conversion through the configured
// exchange fixture. The fixture must be nonnil, and exchanged fields merge
// under RefreshClaudeCredentials before the coordinator attempts persistence.
func (e claudeExchangeFixture) Refresh(ctx context.Context, value *Record) (map[string]any, error) {
	return RefreshClaudeCredentials(ctx, value, e.exchange.RefreshProviderToken)
}

// UpdateOAuthCredentialsIfUnchanged compares the configured record with the
// captured credential version. A missing record or mismatch returns false,
// while a match copies the record and supported credential values before
// calling Update.
//
// Update failure leaves the prior provider pointer intact. The comparison and
// replacement have no lock, so this fixture requires serial repository access
// and does not simulate a database atomic update. A successful call reports
// true after its copied credentials replace the fixture record.
func (r *claudeProviderRepoStub) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version CredentialVersion, credentials map[string]any) (bool, error) {
	if r.provider == nil || !reflect.DeepEqual(FailureVersion(r.provider).CredentialVersion, version) {
		return false, nil
	}
	// Copy the matched record before attempting Update so configured write
	// failure retains the prior credentials instead of exposing an unsaved
	// exchange.
	next := CloneRecord(r.provider)
	next.Credentials = CloneValues(credentials)
	err := r.Update(ctx, next)
	return err == nil, err
}

// TestClaudeTokenCacheKey varies row IDs and token contents across configured
// records. It checks the exact row-based namespace for ordinary, zero, and
// large IDs.
func TestClaudeTokenCacheKey(t *testing.T) {
	tests := []struct {
		name     string  // token source case name
		provider *Record // input provider identity record
		expected string  // expected complete cache key
	}{
		{
			name: "basic_provider",
			provider: &Record{
				ID: 400,
			},
			expected: "claude:provider:400",
		},
		{
			name: "provider_with_credentials",
			provider: &Record{
				ID: 401,
				Credentials: map[string]any{
					"access_token": "claude-token",
				},
			},
			expected: "claude:provider:401",
		},
		{
			name: "provider_id_zero",
			provider: &Record{
				ID: 0,
			},
			expected: "claude:provider:0",
		},
		{
			name: "large_provider_id",
			provider: &Record{
				ID: 9999999999,
			},
			expected: "claude:provider:9999999999",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ClaudeTokenCacheKey(tt.provider)
			require.Equal(t, tt.expected, result)
		})
	}
}
