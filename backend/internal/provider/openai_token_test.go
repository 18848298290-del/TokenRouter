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

// OpenAI token retrieval chooses between cache values, configured refresh
// results and captured credentials. The fixtures store token strings and count
// logical method invocations, allowing these cases to observe returned tokens,
// injected failures, requested disable actions and refresh or contention
// metrics.
//
// Delayed writers use the same map mutex as cache methods, while repository
// observations and fixture error flags are configured by the test owner. Some
// lock-named cases omit a refresh token and therefore skip acquisition; their
// assertions protect token availability rather than a particular lock
// interleaving. The TTL table observes cached content because its stub
// discards ttl. Cases asserting polling metrics or a specific winning token
// document those stronger observable results, and canceled waits are checked
// with immediate fixture operations rather than Redis socket timing.

// openAITokenCacheStub stores token strings and records logical cache and
// lease method calls.
type openAITokenCacheStub struct {
	// mu protects tokens for cache methods and delayed test writers. Those
	// writers use the same guard, while owners configure fixture flags before concurrent use.
	mu     sync.Mutex        // shared token map guard
	tokens map[string]string // configured or published token strings
	// getErr, setErr, deleteErr, lockErr and releaseLockErr inject method outcomes.
	// Configure them before use; token-method errors precede any map mutation and
	// lockErr takes precedence over both lock-result flags.
	getErr    error // injected token read failure
	setErr    error // injected token write failure
	deleteErr error // injected token deletion failure
	// lockAcquired supplies the logical result after lockErr and simulateLockRace
	// have been checked. The fixture records no actual lease ownership or TTL.
	lockAcquired   bool
	lockErr        error // injected logical acquisition failure
	releaseLockErr error // injected logical release failure
	// getCalled, setCalled, deleteCalled, lockCalled and unlockCalled are atomic
	// invocation counters incremented before injected failures. They count fixture
	// methods, including failed attempts, rather than Redis retries or server effects.
	getCalled    int32 // token read method invocations
	setCalled    int32 // token write method invocations
	deleteCalled int32 // token delete method invocations
	lockCalled   int32 // logical acquisition method invocations
	unlockCalled int32 // logical release method invocations
	// simulateLockRace forces an unavailable result when lockErr is nil,
	// overriding lockAcquired. It models the source's reported contention input without creating a lease.
	simulateLockRace bool
}

// openAIProviderRepoStub supplies and replaces one configured provider for
// refresh-source tests.
type openAIProviderRepoStub struct {
	// provider is returned by reference and replaced after successful Update.
	// Test owners serialize its reads and writes and keep borrowed snapshots stable.
	provider     *Record
	getErr       error // injected provider lookup failure
	updateErr    error // injected provider update failure
	getCalled    int32 // atomic repository lookup invocations
	updateCalled int32 // atomic repository update invocations
}

// openAIOAuthServiceStub counts authorization callbacks and supplies
// configured token metadata or failure.
type openAIOAuthServiceStub struct {
	// tokenInfo and refreshErr are configured together before use. A nonnil
	// error wins over the borrowed metadata pointer; successful refresh callers require nonnil metadata.
	tokenInfo     *OpenAITokenInfo // configured authorization metadata result
	refreshErr    error            // configured authorization callback failure
	refreshCalled int32            // atomic authorization callback invocations
}

// openAITokenStateWriter records error-write requests made by
// missing-refresh-token handling.
type openAITokenStateWriter struct {
	// RefreshRepository satisfies the source's repository availability guard and is
	// nil in this fixture. The expired-token path returns before its embedded methods are needed.
	RefreshRepository
	setErrorCalls int    // serial error write invocations
	lastErrorMsg  string // latest requested provider error
}

// openAITokenBlockRecorder pairs observed provider pointers with requested
// runtime block reasons.
type openAITokenBlockRecorder struct {
	// providers and reasons append together for each block request, preserving
	// their positional association. Calls are serial, and tests keep the recorded provider pointers stable.
	providers []*Record // borrowed blocked provider observations
	reasons   []string  // corresponding runtime block reasons
}

// TestOpenAITokenProvider_NoRefreshTokenExpiredAccessTokenReturnsError
// supplies a known expired access token with no refresh token or repository.
// Retrieval returns the missing-refresh-token error and an empty token,
// without requiring persistence dependencies.
func TestOpenAITokenProvider_NoRefreshTokenExpiredAccessTokenReturnsError(t *testing.T) {
	tokenSource := &OpenAITokenSource{Metrics: &OpenAITokenMetricsStore{}, Policy: OpenAIProviderRefreshPolicy(), Debug: slog.Debug, Warn: slog.Warn}
	expiresAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	provider := &Record{
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "expired-access-token",
			"expires_at":   expiresAt,
		},
	}

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.Error(t, err)
	require.Empty(t, token)
	require.Contains(t, err.Error(), "refresh_token is missing")
}

// newOpenAITokenCacheStub creates an empty token map with successful logical
// lock acquisition configured by default. Set error flags before concurrent
// use; cache methods guard token membership, while this fixture neither tracks
// TTLs nor models Redis leases.
func newOpenAITokenCacheStub() *openAITokenCacheStub {
	return &openAITokenCacheStub{
		tokens:       make(map[string]string),
		lockAcquired: true,
	}
}

// GetAccessToken counts one logical read, then returns the configured error or
// the mutex-protected token value. Missing keys return an empty string and
// nil; context is ignored by this immediate fixture operation.
func (s *openAITokenCacheStub) GetAccessToken(ctx context.Context, cacheKey string) (string, error) {
	atomic.AddInt32(&s.getCalled, 1)
	if s.getErr != nil {
		return "", s.getErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[cacheKey], nil
}

// SetAccessToken counts one logical write and stores token under the shared
// map guard unless setErr is configured. The fixture discards ttl and context,
// so its contents do not establish expiry or I/O deadline behavior.
func (s *openAITokenCacheStub) SetAccessToken(ctx context.Context, cacheKey string, token string, ttl time.Duration) error {
	atomic.AddInt32(&s.setCalled, 1)
	if s.setErr != nil {
		return s.setErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[cacheKey] = token
	return nil
}

// DeleteAccessToken counts one logical deletion before checking deleteErr,
// then removes membership under the map guard on success. Failed attempts
// retain token state; context is ignored, and counts describe fixture
// invocations rather than Redis executions.
func (s *openAITokenCacheStub) DeleteAccessToken(ctx context.Context, cacheKey string) error {
	atomic.AddInt32(&s.deleteCalled, 1)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, cacheKey)
	return nil
}

// AcquireRefreshLock counts a logical attempt and returns lockErr before
// consulting contention flags. Otherwise simulateLockRace forces false, then
// lockAcquired supplies the result; the fixture stores no lease and ignores
// ttl and context.
func (s *openAITokenCacheStub) AcquireRefreshLock(ctx context.Context, cacheKey string, ttl time.Duration) (bool, error) {
	atomic.AddInt32(&s.lockCalled, 1)
	if s.lockErr != nil {
		return false, s.lockErr
	}
	if s.simulateLockRace {
		return false, nil
	}
	return s.lockAcquired, nil
}

// ReleaseRefreshLock counts a logical release and returns releaseLockErr. It
// has no lease state to remove and ignores context, so assertions using this
// fixture measure callback invocation rather than actual lock ownership or
// server effects.
func (s *openAITokenCacheStub) ReleaseRefreshLock(ctx context.Context, cacheKey string) error {
	atomic.AddInt32(&s.unlockCalled, 1)
	return s.releaseLockErr
}

// GetByID counts a lookup and returns the configured provider pointer or
// getErr, ignoring requested ID and context. The record is borrowed, and tests
// serialize repository configuration and writes around these immediate reads.
func (r *openAIProviderRepoStub) GetByID(ctx context.Context, id int64) (*Record, error) {
	atomic.AddInt32(&r.getCalled, 1)
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.provider, nil
}

// Update counts a persistence invocation and replaces the configured provider
// pointer only when updateErr is nil. The supplied record is retained by
// reference, and callers serialize repository access because this fixture has
// no internal record mutex.
func (r *openAIProviderRepoStub) Update(ctx context.Context, provider *Record) error {
	atomic.AddInt32(&r.updateCalled, 1)
	if r.updateErr != nil {
		return r.updateErr
	}
	r.provider = provider
	return nil
}

// RefreshProviderToken counts an authorization invocation and returns
// refreshErr before the configured metadata pointer. Input provider and
// context are ignored; configure both result fields before use and retain the
// returned metadata as a borrowed fixture object.
func (s *openAIOAuthServiceStub) RefreshProviderToken(ctx context.Context, provider *Record) (*OpenAITokenInfo, error) {
	atomic.AddInt32(&s.refreshCalled, 1)
	if s.refreshErr != nil {
		return nil, s.refreshErr
	}
	return s.tokenInfo, nil
}

// TestOpenAITokenProvider_TokenRefresh supplies a near-expiry renewable OAuth
// record and configured successful authorization metadata. Retrieval returns
// the refreshed access token and the fixture records one authorization
// invocation.
func TestOpenAITokenProvider_TokenRefresh(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	providerRepo := &openAIProviderRepoStub{}
	oauthService := &openAIOAuthServiceStub{
		tokenInfo: &OpenAITokenInfo{
			AccessToken:  "refreshed-token",
			RefreshToken: "new-refresh-token",
			ExpiresIn:    3600,
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		},
	}

	// The token expires within the refresh lead time.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       102,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "old-refresh-token",
			"expires_at":    expiresAt,
		},
	}
	providerRepo.provider = provider

	customProvider := newOpenAIRefreshSourceFixture(providerRepo, cache, oauthService)

	token, err := customProvider.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "refreshed-token", token)
	require.Equal(t, int32(1), atomic.LoadInt32(&oauthService.refreshCalled))
}

// TestOpenAITokenProvider_LockRaceCondition combines an unexpired access-only
// record with a delayed cache fill. Retrieval returns a nonempty token; the
// absent refresh token skips acquisition despite the configured contention
// flag.
func TestOpenAITokenProvider_LockRaceCondition(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cache.simulateLockRace = true
	providerRepo := &openAIProviderRepoStub{}

	// The recorded expiry is still in the future and no refresh token is supplied.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       103,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token": "race-token",
			"expires_at":   expiresAt,
		},
	}
	providerRepo.provider = provider

	// Launch a delayed cache fill under the source's map guard. This fixture
	// does not join the writer, and its assertion accepts either available token.
	cacheKey := OpenAITokenCacheKey(provider)
	go func() {
		time.Sleep(5 * time.Millisecond)
		cache.mu.Lock()
		cache.tokens[cacheKey] = "winner-token"
		cache.mu.Unlock()
	}()

	tokenSource := newOpenAIRefreshSourceFixture(providerRepo, cache, nil)

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.NotEmpty(t, token)
}

// TestOpenAITokenProvider_RefreshError supplies near-expiry renewable
// credentials and an authorization callback that fails. OpenAI recovery policy
// returns the captured access token with no retrieval error.
func TestOpenAITokenProvider_RefreshError(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	providerRepo := &openAIProviderRepoStub{}
	oauthService := &openAIOAuthServiceStub{
		refreshErr: errors.New("oauth refresh failed"),
	}

	// The token expires within the refresh lead time.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       110,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "old-refresh-token",
			"expires_at":    expiresAt,
		},
	}
	providerRepo.provider = provider

	tokenSource := newOpenAIRefreshSourceFixture(providerRepo, cache, oauthService)

	// The configured policy returns the stored token after refresh failure.
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "old-token", token)
}

// TestOpenAITokenProvider_OAuthServiceNotConfigured supplies an unexpired
// access-only record and omits authorization from the fixture. Retrieval
// returns its stored token; the missing refresh token also bypasses the direct
// lease path.
func TestOpenAITokenProvider_OAuthServiceNotConfigured(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	providerRepo := &openAIProviderRepoStub{}

	// Future expiry permits captured credentials when this fixture has no refresh token.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       111,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token": "old-token",
			"expires_at":   expiresAt,
		},
	}
	providerRepo.provider = provider

	tokenSource := newOpenAIRefreshSourceFixture(providerRepo, cache, nil)

	// With no OAuth service configured, use stored credentials.
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "old-token", token)
}

// TestOpenAITokenProvider_DoubleCheckAfterLock combines an initially empty
// cache and delayed fill with an access-only record. The nonempty-result
// assertion protects availability, while missing refresh credentials keep this
// case outside lock acquisition and coordinated exchange.
func TestOpenAITokenProvider_DoubleCheckAfterLock(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	providerRepo := &openAIProviderRepoStub{}
	oauthService := &openAIOAuthServiceStub{
		tokenInfo: &OpenAITokenInfo{
			AccessToken:  "refreshed-token",
			RefreshToken: "new-refresh",
			ExpiresIn:    3600,
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		},
	}

	// The fixture has a near-expiry access token but no refresh token.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       112,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token": "old-token",
			"expires_at":   expiresAt,
		},
	}
	providerRepo.provider = provider
	cacheKey := OpenAITokenCacheKey(provider)

	// The cache begins empty and a concurrent worker may fill it during the call.

	cache.tokens[cacheKey] = ""

	tokenSource := newOpenAIRefreshSourceFixture(providerRepo, cache, oauthService)

	// Schedule a guarded cache fill alongside credential retrieval. Missing refresh
	// credentials skip lock work here, and the writer is not joined by this fixture.
	go func() {
		time.Sleep(5 * time.Millisecond)
		cache.mu.Lock()
		cache.tokens[cacheKey] = "cached-by-other"
		cache.mu.Unlock()
	}()

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.NotEmpty(t, token)
}

// TestOpenAITokenProvider_CacheHit supplies a cached token different from the
// record's stored access token. Retrieval returns the cached value with one
// fixture read and no cache write.
func TestOpenAITokenProvider_CacheHit(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	provider := &Record{
		ID:       100,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "db-token",
		},
	}
	cacheKey := OpenAITokenCacheKey(provider)
	cache.tokens[cacheKey] = "cached-token"

	tokenSource := newOpenAITokenSourceContract(cache)

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "cached-token", token)
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.getCalled))
	require.Equal(t, int32(0), atomic.LoadInt32(&cache.setCalled))
}

// TestOpenAITokenProvider_CacheMiss_FromCredentials supplies an empty cache
// and a token whose expiry is beyond refresh admission. Retrieval returns the
// stored credential and populates that provider's cache identity with it.
func TestOpenAITokenProvider_CacheMiss_FromCredentials(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	// The expiry is beyond the refresh lead time.
	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       101,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "credential-token",
			"expires_at":   expiresAt,
		},
	}

	tokenSource := newOpenAITokenSourceContract(cache)

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "credential-token", token)

	// A cache miss populates the cache from credentials.
	cacheKey := OpenAITokenCacheKey(provider)
	require.Equal(t, "credential-token", cache.tokens[cacheKey])
}

// TestOpenAITokenProvider_NilProvider invokes an initialized token source with
// no provider record. Retrieval returns an empty token and the nil-provider
// error before accessing any credentials.
func TestOpenAITokenProvider_NilProvider(t *testing.T) {
	tokenSource := newOpenAITokenSourceContract(nil)

	token, err := tokenSource.GetAccessToken(context.Background(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider is nil")
	require.Empty(t, token)
}

// TestOpenAITokenProvider_WrongPlatform supplies a Gemini OAuth record to the
// OpenAI token source. Retrieval returns an empty token and rejects the
// platform before cache or refresh processing.
func TestOpenAITokenProvider_WrongPlatform(t *testing.T) {
	tokenSource := newOpenAITokenSourceContract(nil)
	provider := &Record{
		ID:       104,
		Platform: capability.PlatformGemini,
		Type:     capability.ProviderTypeOAuth,
	}

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not an openai oauth provider")
	require.Empty(t, token)
}

// TestOpenAITokenProvider_WrongProviderType supplies an OpenAI API-key record
// to the OAuth token source. Retrieval returns an empty token and rejects the
// authentication type before reading OAuth credentials.
func TestOpenAITokenProvider_WrongProviderType(t *testing.T) {
	tokenSource := newOpenAITokenSourceContract(nil)
	provider := &Record{
		ID:       105,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeAPIKey,
	}

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not an openai oauth provider")
	require.Empty(t, token)
}

// TestOpenAITokenProvider_NilCache supplies an unexpired OAuth access token
// while omitting the cache dependency. Retrieval returns the stored token,
// protecting the uncached credential path.
func TestOpenAITokenProvider_NilCache(t *testing.T) {
	// The stored token is unexpired.
	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       106,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "nocache-token",
			"expires_at":   expiresAt,
		},
	}

	tokenSource := newOpenAITokenSourceContract(nil)

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "nocache-token", token)
}

// TestOpenAITokenProvider_CacheGetError injects a cache-read failure while
// supplying unexpired stored credentials. Retrieval still returns the captured
// access token with no error, protecting recovery from unavailable cache
// reads.
func TestOpenAITokenProvider_CacheGetError(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cache.getErr = errors.New("redis connection failed")

	// The stored token is unexpired.
	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       107,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "fallback-token",
			"refresh_token": "refresh-token",
			"expires_at":    expiresAt,
		},
	}

	tokenSource := newOpenAITokenSourceContract(cache)

	// A cache-read error still permits stored credentials.
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "fallback-token", token)
}

// TestOpenAITokenProvider_CacheSetError injects a cache-write failure after an
// ordinary credential cache miss. Retrieval returns the stored access token
// with no error, separating token availability from optional cache
// publication.
func TestOpenAITokenProvider_CacheSetError(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cache.setErr = errors.New("redis write failed")

	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       108,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "still-works-token",
			"expires_at":   expiresAt,
		},
	}

	tokenSource := newOpenAITokenSourceContract(cache)

	// A cache-write error does not prevent returning the token.
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "still-works-token", token)
}

// TestOpenAITokenProvider_MissingAccessToken supplies future expiry metadata
// with no access-token field and an empty cache. Retrieval returns the
// missing-access-token error and an empty string.
func TestOpenAITokenProvider_MissingAccessToken(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       109,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"expires_at": expiresAt,
			// Deliberately omit access_token.
		},
	}

	tokenSource := newOpenAITokenSourceContract(cache)

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.Error(t, err)
	require.Contains(t, err.Error(), "access_token not found")
	require.Empty(t, token)
}

// TestOpenAITokenProvider_TTLCalculation supplies three future-expiry
// configurations and verifies their cached token contents. The stub discards
// ttl, so these assertions do not inspect the selected cache durations.
func TestOpenAITokenProvider_TTLCalculation(t *testing.T) {
	tests := []struct {
		name      string        // expiry configuration case label
		expiresIn time.Duration // supplied future expiry interval
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
			cache := newOpenAITokenCacheStub()
			expiresAt := time.Now().Add(tt.expiresIn).Format(time.RFC3339)
			provider := &Record{
				ID:       200,
				Platform: capability.PlatformOpenAI,
				Type:     capability.ProviderTypeOAuth,
				Credentials: map[string]any{
					"access_token": "test-token",
					"expires_at":   expiresAt,
				},
			}

			tokenSource := newOpenAITokenSourceContract(cache)

			_, err := tokenSource.GetAccessToken(context.Background(), provider)
			require.NoError(t, err)

			cacheKey := OpenAITokenCacheKey(provider)
			require.Equal(t, "test-token", cache.tokens[cacheKey])
		})
	}
}

// TestOpenAITokenProvider_Real_LockFailedWait configures an unavailable lease
// and delayed cache fill for near-expiry renewable credentials. The assertion
// accepts any nonempty returned token, including captured credentials if no
// observed cache fill wins.
func TestOpenAITokenProvider_Real_LockFailedWait(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cache.lockAcquired = false

	// Near expiry and a supplied refresh token make acquisition eligible after
	// a cache miss; an already observed delayed cache fill can still return earlier.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       200,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "fallback-token",
			"refresh_token": "refresh-token",
			"expires_at":    expiresAt,
		},
	}

	// Publish a delayed token under the cache guard alongside the retrieval call.
	// The nonempty-result assertion also permits captured credentials if this fill is not observed.
	cacheKey := OpenAITokenCacheKey(provider)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cache.mu.Lock()
		cache.tokens[cacheKey] = "refreshed-by-other"
		cache.mu.Unlock()
	}()

	tokenSource := newOpenAITokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.NotEmpty(t, token)
}

// TestOpenAITokenProvider_Real_CacheHitAfterWait combines a delayed cache fill
// with an unexpired access-only record. Retrieval returns a nonempty token;
// its missing refresh token prevents this fixture from forcing lock waiting.
func TestOpenAITokenProvider_Real_CacheHitAfterWait(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cache.lockAcquired = false

	// This fixture has a near-expiry access token but no refresh token.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       201,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "original-token",
			"expires_at":   expiresAt,
		},
	}

	cacheKey := OpenAITokenCacheKey(provider)
	// Publish a delayed value under the cache guard while retrieval can use
	// unexpired access-only credentials. No writer join or contention assertion is made here.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cache.mu.Lock()
		cache.tokens[cacheKey] = "winner-token"
		cache.mu.Unlock()
	}()

	tokenSource := newOpenAITokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.NotEmpty(t, token)
}

// TestOpenAITokenProvider_Real_ExpiredWithoutRefreshToken supplies an access
// token with both expiry and refresh metadata absent. The stored token is
// returned successfully because this fixture has no known expired timestamp.
func TestOpenAITokenProvider_Real_ExpiredWithoutRefreshToken(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cache.lockAcquired = false // Model an unavailable refresh lock.

	// Missing expiry and refresh-token metadata allow stored credentials to be used.
	provider := &Record{
		ID:       202,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "no-expiry-token",
		},
	}

	tokenSource := newOpenAITokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	// No refresh is attempted because the refresh token is absent.
	require.NoError(t, err)
	require.Equal(t, "no-expiry-token", token)
}

// TestOpenAITokenProvider_Real_WhitespaceToken places a whitespace-only value
// in the cache and supplies an unexpired stored token. Retrieval treats the
// cached value as empty and returns the credential token.
func TestOpenAITokenProvider_Real_WhitespaceToken(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cacheKey := "openai:provider:203"
	cache.tokens[cacheKey] = "   " // whitespace-only cache miss fixture

	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       203,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "real-token",
			"expires_at":   expiresAt,
		},
	}

	tokenSource := newOpenAITokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "real-token", token)
}

// TestOpenAITokenProvider_Real_LockError configures a lease error alongside an
// unexpired access-only record. Retrieval returns its stored token; absent
// refresh credentials skip acquisition, leaving the injected lease error
// unexercised.
func TestOpenAITokenProvider_Real_LockError(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cache.lockErr = errors.New("redis lock failed")

	// The token is near expiry, but its missing refresh token skips lock acquisition.
	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       204,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "fallback-on-lock-error",
			"expires_at":   expiresAt,
		},
	}

	tokenSource := newOpenAITokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "fallback-on-lock-error", token)
}

// TestOpenAITokenProvider_Real_WhitespaceCredentialToken supplies
// whitespace-only stored credentials and an empty cache. Retrieval returns an
// empty token and the missing-access-token error rather than accepting that
// blank value.
func TestOpenAITokenProvider_Real_WhitespaceCredentialToken(t *testing.T) {
	cache := newOpenAITokenCacheStub()

	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       205,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "   ",
			"expires_at":   expiresAt,
		},
	}

	tokenSource := newOpenAITokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.Error(t, err)
	require.Contains(t, err.Error(), "access_token not found")
	require.Empty(t, token)
}

// TestOpenAITokenProvider_Real_NilCredentials supplies an expiry-only
// credential map with the access token omitted. Retrieval returns an empty
// string and the missing-access-token error; the map itself is nonnil.
func TestOpenAITokenProvider_Real_NilCredentials(t *testing.T) {
	cache := newOpenAITokenCacheStub()

	expiresAt := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
	provider := &Record{
		ID:       206,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"expires_at": expiresAt,
			// Deliberately omit access_token.
		},
	}

	tokenSource := newOpenAITokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.Error(t, err)
	require.Contains(t, err.Error(), "access_token not found")
	require.Empty(t, token)
}

// TestOpenAITokenProvider_Real_LockRace_PollingHitsCache configures
// unavailable acquisition and publishes a winning token concurrently.
// Retrieval returns that winning value, while the assertions leave which cache
// read observed it unspecified.
func TestOpenAITokenProvider_Real_LockRace_PollingHitsCache(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cache.lockAcquired = false // Model another worker holding the lock.

	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       207,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "fallback-token",
			"refresh_token": "refresh-token",
			"expires_at":    expiresAt,
		},
	}

	// Publish the specific winning token under the cache guard while retrieval
	// sees a configured unavailable lease. The assertion observes this value, not a join.
	cacheKey := OpenAITokenCacheKey(provider)
	go func() {
		time.Sleep(5 * time.Millisecond)
		cache.mu.Lock()
		cache.tokens[cacheKey] = "winner-token"
		cache.mu.Unlock()
	}()

	tokenSource := newOpenAITokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "winner-token", token)
}

// TestOpenAITokenProvider_Real_LockRace_ContextCanceled supplies a canceled
// context and unavailable logical acquisition with immediate cache fixtures.
// Retrieval returns context.Canceled and no token within the asserted
// interval, protecting the polling cancellation path.
func TestOpenAITokenProvider_Real_LockRace_ContextCanceled(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cache.lockAcquired = false // Model another worker holding the lock.

	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       208,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "fallback-token",
			"refresh_token": "refresh-token",
			"expires_at":    expiresAt,
		},
	}

	// Cancel before retrieval reaches its timer wait, with cache and lease methods
	// completing immediately so the observed cancellation result concerns this fixture's polling path.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tokenSource := newOpenAITokenSourceContract(cache)
	start := time.Now()
	token, err := tokenSource.GetAccessToken(ctx, provider)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, token)
	// Allow scheduling slack around an already-canceled polling wait with immediate fixtures.
	require.Less(t, time.Since(start), 50*time.Millisecond)
}

// TestOpenAITokenProvider_RuntimeMetrics_LockWaitHitAndSnapshot returns a
// delayed winning cache token after configured contention. Snapshot assertions
// observe refresh-path entry, contention, completed wait samples, a polling
// hit and a populated observation timestamp.
func TestOpenAITokenProvider_RuntimeMetrics_LockWaitHitAndSnapshot(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cache.lockAcquired = false

	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       209,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "fallback-token",
			"refresh_token": "refresh-token",
			"expires_at":    expiresAt,
		},
	}
	// Delay publication under the cache guard so the source can enter polling;
	// the metric assertions require a wait sample and a hit on this published value.
	cacheKey := OpenAITokenCacheKey(provider)
	go func() {
		time.Sleep(10 * time.Millisecond)
		cache.mu.Lock()
		cache.tokens[cacheKey] = "winner-token"
		cache.mu.Unlock()
	}()

	tokenSource := newOpenAITokenSourceContract(cache)
	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "winner-token", token)

	metrics := tokenSource.SnapshotRuntimeMetrics()
	require.GreaterOrEqual(t, metrics.RefreshRequests, int64(1))
	require.GreaterOrEqual(t, metrics.LockContention, int64(1))
	require.GreaterOrEqual(t, metrics.LockWaitSamples, int64(1))
	require.GreaterOrEqual(t, metrics.LockWaitHit, int64(1))
	require.GreaterOrEqual(t, metrics.LockWaitTotalMs, int64(0))
	require.GreaterOrEqual(t, metrics.LastObservedUnixMs, int64(1))
}

// TestOpenAITokenProvider_RuntimeMetrics_LockAcquireFailure supplies renewable
// near-expiry credentials and an injected acquisition error. Retrieval
// returns no error, while the metric snapshot records direct acquisition
// failure and refresh-path entry. The returned token is discarded by this case.
func TestOpenAITokenProvider_RuntimeMetrics_LockAcquireFailure(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cache.lockErr = errors.New("redis lock error")

	expiresAt := time.Now().Add(1 * time.Minute).Format(time.RFC3339)
	provider := &Record{
		ID:       210,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "fallback-token",
			"refresh_token": "refresh-token",
			"expires_at":    expiresAt,
		},
	}

	tokenSource := newOpenAITokenSourceContract(cache)
	_, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.NoError(t, err)

	metrics := tokenSource.SnapshotRuntimeMetrics()
	require.GreaterOrEqual(t, metrics.LockAcquireFailure, int64(1))
	require.GreaterOrEqual(t, metrics.RefreshRequests, int64(1))
}

// TestOpenAITokenProvider_NoRefreshTokenExpired_DisablesProvider forces a
// cache miss for known expired access-only credentials. It observes the
// requested runtime block, error-write message and one cache-delete invocation
// alongside the returned reauthorization error.
func TestOpenAITokenProvider_NoRefreshTokenExpired_DisablesProvider(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	repo := &openAITokenStateWriter{}

	expiresAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	provider := &Record{
		ID:       2881,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token": "expired-access-token",
			"expires_at":   expiresAt,
		},
	}

	cache.tokens[OpenAITokenCacheKey(provider)] = "stale-cached-token"
	cache.getErr = errors.New("simulated cache miss")
	tokenSource := newOpenAITokenSourceContract(cache)
	tokenSource.Repository = repo
	tokenSource.SetError = repo.SetError
	blocker := &openAITokenBlockRecorder{}
	tokenSource.Block = blocker.record

	token, err := tokenSource.GetAccessToken(context.Background(), provider)
	require.Error(t, err)
	require.Empty(t, token)
	require.Contains(t, err.Error(), "refresh_token is missing")
	require.Equal(t, 1, repo.setErrorCalls)
	require.Contains(t, repo.lastErrorMsg, "refresh_token is missing")
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.deleteCalled))
	require.Len(t, blocker.providers, 1)
	require.Equal(t, provider.ID, blocker.providers[0].ID)
	require.Equal(t, "missing_refresh_token", blocker.reasons[0])
}

// newOpenAITokenSourceContract builds the production token source with an
// injected cache, initialized metrics and OpenAI recovery policy.
// Authorization and repository dependencies remain unset, allowing cases to
// select stored credentials or the direct lease compatibility path.
func newOpenAITokenSourceContract(cache AccessTokenCache) *OpenAITokenSource {
	return &OpenAITokenSource{
		Cache: cache, Metrics: &OpenAITokenMetricsStore{}, Policy: OpenAIProviderRefreshPolicy(),
		Debug: slog.Debug, Warn: slog.Warn,
	}
}

// SetError records one requested error write and its latest message, then
// returns success. It ignores context and provider ID; this serial fixture
// does not persist a provider row.
func (w *openAITokenStateWriter) SetError(_ context.Context, _ int64, message string) error {
	w.setErrorCalls++
	w.lastErrorMsg = message
	return nil
}

// record appends the borrowed provider pointer and reason at matching indices
// for one runtime-block request. Calls are serialized by the test owner, and
// the supplied block deadline is discarded.
func (r *openAITokenBlockRecorder) record(value *Record, _ time.Time, reason string) {
	r.providers = append(r.providers, value)
	r.reasons = append(r.reasons, reason)
}

// newOpenAIRefreshSourceFixture binds the token source to configured
// repository and cache fixtures with preallocated metrics. A nonnil
// authorization stub installs the shared refresh coordinator and OpenAI
// adapter; nil authorization leaves the source's Refresh callback unset.
func newOpenAIRefreshSourceFixture(repo *openAIProviderRepoStub, cache *openAITokenCacheStub, authorization *openAIOAuthServiceStub) *OpenAITokenSource {
	source := &OpenAITokenSource{Repository: repo, Cache: cache, Metrics: &OpenAITokenMetricsStore{}, Policy: OpenAIProviderRefreshPolicy(), Debug: slog.Debug, Warn: slog.Warn}
	if authorization != nil {
		api := NewOAuthRefreshAPI(repo, cache, RefreshOptions{Platform: ProviderRefreshPlatformPolicy()})
		executor := &OpenAITokenRefresher{Authorization: authorization}
		source.Refresh = func(ctx context.Context, value *Record, window time.Duration) (*OAuthRefreshResult, error) {
			return api.RefreshIfNeeded(ctx, value, executor, window)
		}
	}
	return source
}

// UpdateOAuthCredentialsIfUnchanged compares the current configured record's
// CredentialVersion with the captured version before preparing a replacement.
// A missing row or mismatch returns false without calling Update.
//
// Matching input is copied with CloneRecord and its credentials replaced with
// CloneValues, then passed to the fixture's Update method. An injected update
// error retains the configured row and returns false with that error.
// Repository state has no internal lock, so callers serialize the comparison
// and replacement; this models the conditional writer decision rather than a
// database transaction.
func (r *openAIProviderRepoStub) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version CredentialVersion, credentials map[string]any) (bool, error) {
	if r.provider == nil || !reflect.DeepEqual(FailureVersion(r.provider).CredentialVersion, version) {
		return false, nil
	}
	// Prepare the matching replacement separately so an injected Update failure
	// retains the configured record and its existing credential containers for later observations.
	next := CloneRecord(r.provider)
	next.Credentials = CloneValues(credentials)
	err := r.Update(ctx, next)
	return err == nil, err
}

// TestOpenAITokenCacheKey supplies ordinary, zero and large provider IDs plus
// a record with credentials. Expected strings protect decimal provider-ID
// formatting and show that credential contents do not alter the OpenAI
// identity.
func TestOpenAITokenCacheKey(t *testing.T) {
	tests := []struct {
		name     string  // cache identity formatting case
		provider *Record // record supplying provider ID
		expected string  // expected platform provider identity
	}{
		{
			name: "basic_provider",
			provider: &Record{
				ID: 300,
			},
			expected: "openai:provider:300",
		},
		{
			name: "provider_with_credentials",
			provider: &Record{
				ID: 301,
				Credentials: map[string]any{
					"access_token": "test-token",
				},
			},
			expected: "openai:provider:301",
		},
		{
			name: "provider_id_zero",
			provider: &Record{
				ID: 0,
			},
			expected: "openai:provider:0",
		},
		{
			name: "large_provider_id",
			provider: &Record{
				ID: 9999999999,
			},
			expected: "openai:provider:9999999999",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := OpenAITokenCacheKey(tt.provider)
			require.Equal(t, tt.expected, result)
		})
	}
}

// TestOpenAITokenProvider_PersonalAccessTokenDoesNotExpireLikeOAuth supplies
// PAT mode with stale OAuth expiry metadata and no cache. Retrieval returns
// the PAT string successfully, protecting its exclusion from expiry-triggered
// refresh and missing-refresh-token rejection.
func TestOpenAITokenProvider_PersonalAccessTokenDoesNotExpireLikeOAuth(t *testing.T) {
	expiresAt := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	provider := &Record{
		LoadLocation: time.LoadLocation, ID: 4001,
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "at-expired-metadata",
			"auth_mode":          OpenAIAuthModePersonalAccessToken,
			"openai_auth_mode":   "personal_access_token",
			"expires_at":         expiresAt,
			"chatgpt_account_id": "acct-123",
		},
	}

	tokenSource := &OpenAITokenSource{Metrics: &OpenAITokenMetricsStore{}, Policy: OpenAIProviderRefreshPolicy(), Debug: slog.Debug, Warn: slog.Warn}
	token, err := tokenSource.GetAccessToken(context.Background(), provider)

	require.NoError(t, err)
	require.Equal(t, "at-expired-metadata", token)
}
