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

// openAITokenCacheStub records token-cache and refresh-lock calls.
type openAITokenCacheStub struct {
	mu               sync.Mutex
	tokens           map[string]string
	getErr           error
	setErr           error
	deleteErr        error
	lockAcquired     bool
	lockErr          error
	releaseLockErr   error
	getCalled        int32
	setCalled        int32
	deleteCalled     int32
	lockCalled       int32
	unlockCalled     int32
	simulateLockRace bool
}

// openAIProviderRepoStub models persistence needed by the OpenAI token source.
type openAIProviderRepoStub struct {
	provider     *Record
	getErr       error
	updateErr    error
	getCalled    int32
	updateCalled int32
}

// openAIOAuthServiceStub returns a configured token-exchange result or error.
type openAIOAuthServiceStub struct {
	tokenInfo     *OpenAITokenInfo
	refreshErr    error
	refreshCalled int32
}

type openAITokenStateWriter struct {
	RefreshRepository
	setErrorCalls int
	lastErrorMsg  string
}

type openAITokenBlockRecorder struct {
	providers []*Record
	reasons   []string
}

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

func newOpenAITokenCacheStub() *openAITokenCacheStub {
	return &openAITokenCacheStub{
		tokens:       make(map[string]string),
		lockAcquired: true,
	}
}

func (s *openAITokenCacheStub) GetAccessToken(ctx context.Context, cacheKey string) (string, error) {
	atomic.AddInt32(&s.getCalled, 1)
	if s.getErr != nil {
		return "", s.getErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[cacheKey], nil
}

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

func (s *openAITokenCacheStub) ReleaseRefreshLock(ctx context.Context, cacheKey string) error {
	atomic.AddInt32(&s.unlockCalled, 1)
	return s.releaseLockErr
}

func (r *openAIProviderRepoStub) GetByID(ctx context.Context, id int64) (*Record, error) {
	atomic.AddInt32(&r.getCalled, 1)
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.provider, nil
}

func (r *openAIProviderRepoStub) Update(ctx context.Context, provider *Record) error {
	atomic.AddInt32(&r.updateCalled, 1)
	if r.updateErr != nil {
		return r.updateErr
	}
	r.provider = provider
	return nil
}

func (s *openAIOAuthServiceStub) RefreshProviderToken(ctx context.Context, provider *Record) (*OpenAITokenInfo, error) {
	atomic.AddInt32(&s.refreshCalled, 1)
	if s.refreshErr != nil {
		return nil, s.refreshErr
	}
	return s.tokenInfo, nil
}

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

func TestOpenAITokenProvider_LockRaceCondition(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cache.simulateLockRace = true
	providerRepo := &openAIProviderRepoStub{}

	// The token is still usable and has no refresh token.
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

	// Model a concurrent cache fill by another worker.
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

func TestOpenAITokenProvider_OAuthServiceNotConfigured(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	providerRepo := &openAIProviderRepoStub{}

	// The token remains valid despite having no refresh token.
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

	// Fill the cache asynchronously to exercise concurrent access.
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

func TestOpenAITokenProvider_NilProvider(t *testing.T) {
	tokenSource := newOpenAITokenSourceContract(nil)

	token, err := tokenSource.GetAccessToken(context.Background(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider is nil")
	require.Empty(t, token)
}

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

func TestOpenAITokenProvider_TTLCalculation(t *testing.T) {
	tests := []struct {
		name      string
		expiresIn time.Duration
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

// TestOpenAITokenProvider_Real_LockFailedWait exercises waiting for a competing refresh.
func TestOpenAITokenProvider_Real_LockFailedWait(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cache.lockAcquired = false

	// The token has a refresh token and expires within the lead time, triggering lock acquisition.
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

	// Model another worker filling the cache while this call waits.
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
	// Fill the cache asynchronously; this fixture does not force lock waiting.
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

func TestOpenAITokenProvider_Real_WhitespaceToken(t *testing.T) {
	cache := newOpenAITokenCacheStub()
	cacheKey := "openai:provider:203"
	cache.tokens[cacheKey] = "   " // Whitespace only - should be treated as empty

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

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tokenSource := newOpenAITokenSourceContract(cache)
	start := time.Now()
	token, err := tokenSource.GetAccessToken(ctx, provider)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, token)
	require.Less(t, time.Since(start), 50*time.Millisecond)
}

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

// newOpenAITokenSourceContract builds the production token source with test cache and logging dependencies.
func newOpenAITokenSourceContract(cache AccessTokenCache) *OpenAITokenSource {
	return &OpenAITokenSource{
		Cache: cache, Metrics: &OpenAITokenMetricsStore{}, Policy: OpenAIProviderRefreshPolicy(),
		Debug: slog.Debug, Warn: slog.Warn,
	}
}

func (w *openAITokenStateWriter) SetError(_ context.Context, _ int64, message string) error {
	w.setErrorCalls++
	w.lastErrorMsg = message
	return nil
}

func (r *openAITokenBlockRecorder) record(value *Record, _ time.Time, reason string) {
	r.providers = append(r.providers, value)
	r.reasons = append(r.reasons, reason)
}

// newOpenAIRefreshSourceFixture combines the token source, refresh coordinator, and exchange stub.
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

// UpdateOAuthCredentialsIfUnchanged models conditional credential writes and injected persistence failures.
func (r *openAIProviderRepoStub) UpdateOAuthCredentialsIfUnchanged(ctx context.Context, version CredentialVersion, credentials map[string]any) (bool, error) {
	if r.provider == nil || !reflect.DeepEqual(FailureVersion(r.provider).CredentialVersion, version) {
		return false, nil
	}
	next := CloneRecord(r.provider)
	next.Credentials = CloneValues(credentials)
	err := r.Update(ctx, next)
	return err == nil, err
}

func TestOpenAITokenCacheKey(t *testing.T) {
	tests := []struct {
		name     string
		provider *Record
		expected string
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
