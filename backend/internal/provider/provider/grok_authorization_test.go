package provider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	xai "github.com/TokenFlux/TokenRouter/internal/upstream/grok"
)

type grokOAuthClientStub struct {
	refreshResponse     *xai.TokenResponse
	ssoResponse         *xai.TokenResponse
	loginResult         *providercore.GrokPasswordLoginResult
	loginEmail          string
	loginPassword       string
	exchangeCalls       int
	exchangeRedirectURI string
}

type grokTokenCacheForProviderTest struct {
	token        string
	setKey       string
	setToken     string
	setTTL       time.Duration
	lockResult   bool
	releaseCalls int
	deletedKeys  []string
	deleteErr    error
	getCalls     int
	mu           sync.Mutex
}

type grokCredentialRaceRepo struct {
	*tokenRefreshProviderRepo
	mu sync.RWMutex
}

func TestGrokOAuthServiceRefreshTokenPreservesOriginalRefreshTokenWhenNotRotated(t *testing.T) {
	svc := newGrokAuthorizationForTest(nil, &grokOAuthClientStub{
		refreshResponse: &xai.TokenResponse{
			AccessToken: "new-access-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		},
	})
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)

	info, err := svc.RefreshToken(context.Background(), "original-refresh-token", "", "client-id")
	require.NoError(t, err)
	require.Equal(t, "new-access-token", info.AccessToken)
	require.Equal(t, "original-refresh-token", info.RefreshToken)
	require.Equal(t, "client-id", info.ClientID)
}

func TestGrokOAuthServiceRefreshTokenRejectsEmptyUpstreamResponse(t *testing.T) {
	svc := newGrokAuthorizationForTest(nil, &grokOAuthClientStub{})
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)

	require.NotPanics(t, func() {
		info, err := svc.RefreshToken(context.Background(), "refresh-token", "", "client-id")
		require.Nil(t, info)
		require.Error(t, err)
		require.Contains(t, err.Error(), "GROK_OAUTH_INVALID_TOKEN_RESPONSE")
	})
}

func TestGrokOAuthServiceExchangeCodeConsumesOnlyAfterValidation(t *testing.T) {
	client := &grokOAuthClientStub{}
	svc := newGrokAuthorizationForTest(nil, client)
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)

	auth, err := svc.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)

	_, err = svc.ExchangeCode(context.Background(), &providercore.GrokExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "http://127.0.0.1:56121/callback?code=code-without-state",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_STATE_REQUIRED")
	require.Zero(t, client.exchangeCalls)

	_, err = svc.ExchangeCode(context.Background(), &providercore.GrokExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "code-with-state",
		State:     auth.State,
	})
	require.NoError(t, err)
	require.Equal(t, 1, client.exchangeCalls)

	_, err = svc.ExchangeCode(context.Background(), &providercore.GrokExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "replayed-code",
		State:     auth.State,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_SESSION_NOT_FOUND")
	require.Equal(t, 1, client.exchangeCalls)
}

func TestGrokOAuthServiceExchangeCodeRejectsMissingClientWithoutConsumingSession(t *testing.T) {
	svc := newGrokAuthorizationForTest(nil, nil)
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)
	auth, err := svc.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)

	_, err = svc.ExchangeCode(context.Background(), &providercore.GrokExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "code",
		State:     auth.State,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_CLIENT_NOT_CONFIGURED")
	_, ok := svc.Store.Get(auth.SessionID)
	require.True(t, ok)
}

func TestGrokOAuthServiceExchangeCodeRequiresStateForBareCode(t *testing.T) {
	client := &grokOAuthClientStub{}
	svc := newGrokAuthorizationForTest(nil, client)
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)
	auth, err := svc.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)

	_, err = svc.ExchangeCode(context.Background(), &providercore.GrokExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "bare-authorization-code",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_STATE_REQUIRED")
	require.Zero(t, client.exchangeCalls)
	_, ok := svc.Store.Get(auth.SessionID)
	require.True(t, ok)
}

func TestGrokOAuthServiceExchangeCodeRejectsRedirectURIOverride(t *testing.T) {
	client := &grokOAuthClientStub{}
	svc := newGrokAuthorizationForTest(nil, client)
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)
	auth, err := svc.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)

	_, err = svc.ExchangeCode(context.Background(), &providercore.GrokExchangeCodeInput{
		SessionID:   auth.SessionID,
		Code:        "authorization-code",
		State:       auth.State,
		RedirectURI: "http://127.0.0.1:9999/callback",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_REDIRECT_URI_MISMATCH")
	require.Zero(t, client.exchangeCalls)

	_, err = svc.ExchangeCode(context.Background(), &providercore.GrokExchangeCodeInput{
		SessionID:   auth.SessionID,
		Code:        "authorization-code",
		State:       auth.State,
		RedirectURI: xai.DefaultRedirectURI,
	})
	require.NoError(t, err)
	require.Equal(t, xai.DefaultRedirectURI, client.exchangeRedirectURI)
}

func TestGrokOAuthServiceExternalFlowsRejectMissingClient(t *testing.T) {
	svc := newGrokAuthorizationForTest(nil, nil)
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)

	_, err := svc.RefreshToken(context.Background(), "refresh-token", "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_CLIENT_NOT_CONFIGURED")

	_, err = svc.ValidateSSOToken(context.Background(), "sso-token", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_CLIENT_NOT_CONFIGURED")
}

func TestGrokOAuthServiceBuildProviderCredentialsDefaultsToSubscriptionProxy(t *testing.T) {
	svc := newGrokAuthorizationForTest(nil, &grokOAuthClientStub{})
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)

	credentials := svc.BuildProviderCredentials(&providercore.GrokTokenInfo{
		AccessToken: "access-token",
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	})

	require.Equal(t, xai.DefaultCLIBaseURL, credentials["base_url"])
}

func TestGrokOAuthServiceConvertFromSSOExtractsBuildClaims(t *testing.T) {
	svc := newGrokAuthorizationForTest(nil, &grokOAuthClientStub{
		ssoResponse: &xai.TokenResponse{
			AccessToken:  makeGrokOAuthJWT(map[string]any{"sub": "user-sub", "team_id": "team-1", "tier": 5}),
			RefreshToken: "refresh-token",
			IDToken:      makeGrokOAuthJWT(map[string]any{"email": "user@example.com"}),
			ExpiresIn:    3600,
		},
	})
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)

	info, err := svc.ConvertFromSSO(context.Background(), "sso-token", nil)
	require.NoError(t, err)
	require.Equal(t, "user@example.com", info.Email)
	require.Equal(t, "user-sub", info.Subject)
	require.Equal(t, "team-1", info.TeamID)
	require.Equal(t, "supergrok_heavy", info.SubscriptionTier)

	credentials := svc.BuildProviderCredentials(info)
	require.Equal(t, "user@example.com", credentials["email"])
	require.Equal(t, "user-sub", credentials["sub"])
	require.Equal(t, "team-1", credentials["team_id"])
	require.Equal(t, "supergrok_heavy", credentials["subscription_tier"])
	require.NotContains(t, credentials, "sso_token")
}

func TestGrokOAuthServiceRefreshProviderTokenOverwritesStaleTierFromNewJWT(t *testing.T) {
	svc := newGrokAuthorizationForTest(nil, &grokOAuthClientStub{
		refreshResponse: &xai.TokenResponse{
			AccessToken: makeGrokOAuthJWT(map[string]any{"sub": "user-sub", "tier": 0}),
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		},
	})
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)

	provider := &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"refresh_token":     "refresh-token",
			"client_id":         "client-id",
			"subscription_tier": "supergrok_heavy",
		},
	}

	info, err := svc.RefreshProviderToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "free", info.SubscriptionTier)

	credentials := svc.BuildProviderCredentials(info)
	require.Equal(t, "free", credentials["subscription_tier"])
}

func TestGrokOAuthServiceRefreshProviderTokenIgnoresIDTokenTierWhenAccessTokenHasNone(t *testing.T) {
	svc := newGrokAuthorizationForTest(nil, &grokOAuthClientStub{
		refreshResponse: &xai.TokenResponse{
			AccessToken: "opaque-access-token",
			IDToken:     makeGrokOAuthJWT(map[string]any{"tier": 5}),
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		},
	})
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)

	provider := &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"refresh_token":     "refresh-token",
			"subscription_tier": "supergrok_lite",
		},
	}

	info, err := svc.RefreshProviderToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "supergrok_lite", info.SubscriptionTier)
}

func TestGrokOAuthServiceRefreshProviderTokenKeepsStoredTierWhenJWTHasNoClaim(t *testing.T) {
	svc := newGrokAuthorizationForTest(nil, &grokOAuthClientStub{
		refreshResponse: &xai.TokenResponse{
			AccessToken: "opaque-access-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		},
	})
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)

	provider := &providercore.Record{
		Platform: capability.PlatformGrok,
		Type:     capability.ProviderTypeOAuth,
		Credentials: map[string]any{
			"refresh_token":     "refresh-token",
			"subscription_tier": "supergrok_lite",
		},
	}

	info, err := svc.RefreshProviderToken(context.Background(), provider)
	require.NoError(t, err)
	require.Equal(t, "supergrok_lite", info.SubscriptionTier)
}

func TestGrokOAuthServiceValidateSSOTokenReturnsOAuthTokensWithoutPersistingSSO(t *testing.T) {
	svc := newGrokAuthorizationForTest(nil, &grokOAuthClientStub{
		ssoResponse: &xai.TokenResponse{
			AccessToken:  "access-from-sso",
			RefreshToken: "refresh-from-sso",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
		},
	})
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)

	info, err := svc.ValidateSSOToken(context.Background(), "sso-token", nil)
	require.NoError(t, err)
	require.Equal(t, "access-from-sso", info.AccessToken)
	require.Equal(t, "refresh-from-sso", info.RefreshToken)

	creds := svc.BuildProviderCredentials(info)
	require.NotContains(t, creds, "sso_token")
	require.NotContains(t, creds, "password")
}

func TestGrokOAuthServiceAuthorizePasswordUsesLoginThenSSOAuthorize(t *testing.T) {
	client := &grokOAuthClientStub{
		loginResult: &providercore.GrokPasswordLoginResult{
			Email:    "user@example.com",
			SSOToken: "password-derived-sso",
		},
		ssoResponse: &xai.TokenResponse{
			AccessToken:  "access-from-password",
			RefreshToken: "refresh-from-password",
			ExpiresIn:    3600,
		},
	}
	enabled := true
	svc := newGrokAuthorizationForTest(nil, client, enabled)
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)

	require.True(t, svc.GetCapabilities().PasswordAuthEnabled)
	info, err := svc.AuthorizePassword(context.Background(), " user@example.com ", "  super-secret  ", nil)
	require.NoError(t, err)
	require.Equal(t, "user@example.com", info.Email)
	require.Equal(t, "access-from-password", info.AccessToken)
	creds := svc.BuildProviderCredentials(info)
	require.NotContains(t, creds, "password")
	require.NotContains(t, creds, "sso_token")
	require.Equal(t, "user@example.com", client.loginEmail)
	require.Equal(t, "  super-secret  ", client.loginPassword)
}

func TestGrokOAuthServiceAuthorizePasswordDisabledByDefault(t *testing.T) {
	client := &grokOAuthClientStub{}
	svc := newGrokAuthorizationForTest(nil, client)
	svc.Start()
	defer stopGrokAuthorizationForTest(t, svc)

	require.False(t, svc.GetCapabilities().PasswordAuthEnabled)
	_, err := svc.AuthorizePassword(context.Background(), "user@example.com", "secret", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "GROK_OAUTH_PASSWORD_AUTH_DISABLED")
	require.Empty(t, client.loginEmail)
}

func TestGrokTokenProviderRefreshesExpiredTokenOnRequestPath(t *testing.T) {
	t.Setenv(xai.EnvBaseURL, xai.DefaultCLIBaseURL)

	expiredAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	provider := &providercore.Record{
		ID:          54,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":  "expired-access-token",
			"refresh_token": "refresh-token",
			"expires_at":    expiredAt,
			"base_url":      xai.DefaultCLIBaseURL,
			"client_id":     "client-id",
		},
	}
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*providercore.Record{54: provider}
	cache := &grokTokenCacheForProviderTest{lockResult: true}
	oauthSvc := newGrokAuthorizationForTest(nil, &grokOAuthClientStub{
		refreshResponse: &xai.TokenResponse{
			AccessToken: "new-access-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		},
	})
	oauthSvc.Start()
	defer stopGrokAuthorizationForTest(t, oauthSvc)

	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newRefreshAPI(repo, cache), providercore.NewGrokTokenRefresher(oauthSvc))

	token, err := tokenSource.GetAccessToken(context.Background(), providercore.CloneRecord(provider))
	require.NoError(t, err)
	require.Equal(t, "new-access-token", token)
	require.Equal(t, 1, repo.updateCredentialsCalls)
	require.Equal(t, "new-access-token", repo.providersByID[54].GetGrokAccessToken())
	require.Equal(t, "refresh-token", repo.providersByID[54].GetGrokRefreshToken())
	require.Equal(t, xai.DefaultCLIBaseURL, GrokProviderBaseURL(repo.providersByID[54]))
	require.Equal(t, "grok:provider:54", cache.setKey)
	require.Equal(t, "new-access-token", cache.setToken)
	require.Greater(t, cache.setTTL, time.Duration(0))
	require.Equal(t, 1, cache.releaseCalls)
}

func TestGrokTokenProviderRefreshFailureUnschedulesWithRedactedReason(t *testing.T) {
	expiredAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	provider := &providercore.Record{
		ID:          55,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":  "expired-access-token",
			"refresh_token": "refresh-token",
			"expires_at":    expiredAt,
			"base_url":      xai.DefaultCLIBaseURL,
		},
	}
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*providercore.Record{55: provider}
	cache := &grokTokenCacheForProviderTest{lockResult: true}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newRefreshAPI(repo, cache), &tokenRefresherStub{
		err: errors.New("temporary refresh failure access_token=leaked-access refresh_token=leaked-refresh"),
	})

	token, err := tokenSource.GetAccessToken(context.Background(), providercore.CloneRecord(provider))
	require.Error(t, err)
	require.Empty(t, token)
	require.Equal(t, 0, repo.setTempUnschedCalls)
	require.Equal(t, 0, repo.setErrorCalls)
}

func TestGrokTokenProviderLockHeldWaitsForRefreshedCacheAndNeverUsesExpiredToken(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(56)
	baseRepo := &tokenRefreshProviderRepo{}
	baseRepo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	repo := &grokCredentialRaceRepo{tokenRefreshProviderRepo: baseRepo}
	cache := &grokTokenCacheForProviderTest{lockResult: false, token: "expired-access-token"}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newRefreshAPI(repo, cache), &tokenRefresherStub{})

	go func() {
		time.Sleep(40 * time.Millisecond)
		refreshed := *provider
		refreshed.Credentials = querycache.ShallowMap(provider.Credentials)
		refreshed.Credentials["access_token"] = "refreshed-after-lock"
		refreshed.Credentials["expires_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		refreshed.Credentials["_token_version"] = time.Now().UnixMilli()
		repo.setProvider(&refreshed)
		cache.mu.Lock()
		cache.token = "refreshed-after-lock"
		cache.mu.Unlock()
	}()

	startedAt := time.Now()
	token, err := tokenSource.GetAccessToken(context.Background(), providercore.CloneRecord(provider))
	require.NoError(t, err)
	require.Equal(t, "refreshed-after-lock", token)
	require.NotEqual(t, "expired-access-token", token)
	require.GreaterOrEqual(t, time.Since(startedAt), 25*time.Millisecond,
		"expired provider metadata must prevent returning the old cached token")
}

func TestGrokTokenProviderLockHeldTimeoutDoesNotReturnExpiredToken(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(57)
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	cache := &grokTokenCacheForProviderTest{lockResult: false}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newRefreshAPI(repo, cache), &tokenRefresherStub{})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	token, err := tokenSource.GetAccessToken(ctx, providercore.CloneRecord(provider))
	require.Error(t, err)
	require.Empty(t, token)
}

func TestGrokTokenProviderLockHeldRejectsChangedTokenWithoutExpiry(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(58)
	baseRepo := &tokenRefreshProviderRepo{}
	baseRepo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	repo := &grokCredentialRaceRepo{tokenRefreshProviderRepo: baseRepo}
	cache := &grokTokenCacheForProviderTest{lockResult: false, token: "expired-access-token"}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newRefreshAPI(repo, cache), &tokenRefresherStub{})

	go func() {
		time.Sleep(30 * time.Millisecond)
		refreshed := *provider
		refreshed.Credentials = querycache.ShallowMap(provider.Credentials)
		refreshed.Credentials["access_token"] = "changed-without-expiry"
		delete(refreshed.Credentials, "expires_at")
		refreshed.Credentials["_token_version"] = time.Now().UnixMilli()
		repo.setProvider(&refreshed)
		cache.mu.Lock()
		cache.token = "changed-without-expiry"
		cache.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	token, err := tokenSource.GetAccessToken(ctx, providercore.CloneRecord(provider))

	require.Error(t, err)
	require.Empty(t, token, "an unbounded credential must not win the lock-held race")
}

func TestGrokTokenProviderLockHeldUsesVersionedDBTokenAndRepairsStaleCache(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(60)
	baseRepo := &tokenRefreshProviderRepo{}
	baseRepo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	repo := &grokCredentialRaceRepo{tokenRefreshProviderRepo: baseRepo}
	cache := &grokTokenCacheForProviderTest{lockResult: false, token: "expired-access-token"}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newRefreshAPI(repo, cache), &tokenRefresherStub{})

	go func() {
		time.Sleep(30 * time.Millisecond)
		refreshed := *provider
		refreshed.Credentials = querycache.ShallowMap(provider.Credentials)
		refreshed.Credentials["access_token"] = "db-authoritative-token"
		refreshed.Credentials["expires_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
		refreshed.Credentials["_token_version"] = time.Now().UnixMilli()
		repo.setProvider(&refreshed)
	}()

	token, err := tokenSource.GetAccessToken(context.Background(), providercore.CloneRecord(provider))

	require.NoError(t, err)
	require.Equal(t, "db-authoritative-token", token)
	require.Equal(t, "db-authoritative-token", cache.setToken)
	require.Greater(t, cache.setTTL, time.Duration(0))
}

func TestGrokTokenProviderRejectsStaleDBTokenWithoutExpiry(t *testing.T) {
	expiresAt := time.Now().Add(2 * providercore.GrokTokenRefreshSkew).UTC().Format(time.RFC3339)
	provider := &providercore.Record{
		ID:          59,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":  "old-access-token",
			"refresh_token": "refresh-token",
			"expires_at":    expiresAt,
		},
	}
	latest := *provider
	latest.Credentials = querycache.ShallowMap(provider.Credentials)
	latest.Credentials["access_token"] = "new-access-token-without-expiry"
	latest.Credentials["_token_version"] = time.Now().UnixMilli()
	delete(latest.Credentials, "expires_at")
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: &latest}
	cache := &grokTokenCacheForProviderTest{}
	tokenSource := newGrokTokenSourceForTest(repo, cache)

	token, err := tokenSource.GetAccessToken(context.Background(), providercore.CloneRecord(provider))

	require.ErrorIs(t, err, providercore.ErrGrokOAuthAccessTokenExpired)
	require.Empty(t, token)
}

// TestGrokTokenProviderManualTestBypassesSchedulingGate covers administrative probes of disabled,
// rate-limited, overloaded, or cooling providers while production requests reject them (#4598).
func TestGrokTokenProviderManualTestBypassesSchedulingGate(t *testing.T) {
	future := time.Now().Add(time.Hour)
	tests := []struct {
		name   string
		mutate func(*providercore.Record)
	}{
		{name: "not schedulable", mutate: func(provider *providercore.Record) { provider.Schedulable = false }},
		{name: "temporarily unschedulable", mutate: func(provider *providercore.Record) { provider.TempUnschedulableUntil = &future }},
		{name: "rate limited", mutate: func(provider *providercore.Record) { provider.RateLimitResetAt = &future }},
		{name: "overloaded", mutate: func(provider *providercore.Record) { provider.OverloadUntil = &future }},
		{name: "disabled by error", mutate: func(provider *providercore.Record) { provider.Status = providercore.StatusError }},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := expiredGrokOAuthProviderForCredentialTest(int64(120 + index))
			provider.Credentials["access_token"] = "still-valid-token"
			provider.Credentials["expires_at"] = time.Now().Add(2 * providercore.GrokTokenRefreshSkew).UTC().Format(time.RFC3339)
			tt.mutate(provider)
			tokenSource := newGrokTokenSourceForTest(&tokenRefreshProviderRepo{}, &grokTokenCacheForProviderTest{})

			_, requestErr := tokenSource.GetAccessToken(context.Background(), providercore.CloneRecord(provider))
			require.ErrorIs(t, requestErr, providercore.ErrRefreshProviderStateChanged)

			token, err := tokenSource.GetAccessTokenForManualTest(context.Background(), providercore.CloneRecord(provider))
			require.NoError(t, err)
			require.Equal(t, "still-valid-token", token)
		})
	}
}

func TestGrokTokenProviderManualTestRefreshesExpiredTokenWhileUnschedulable(t *testing.T) {
	t.Setenv(xai.EnvBaseURL, xai.DefaultCLIBaseURL)

	expiredAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	provider := &providercore.Record{
		ID:          130,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: false,
		Credentials: map[string]any{
			"access_token":  "expired-access-token",
			"refresh_token": "refresh-token",
			"expires_at":    expiredAt,
			"base_url":      xai.DefaultCLIBaseURL,
			"client_id":     "client-id",
		},
	}
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*providercore.Record{130: provider}
	cache := &grokTokenCacheForProviderTest{lockResult: true}
	oauthSvc := newGrokAuthorizationForTest(nil, &grokOAuthClientStub{
		refreshResponse: &xai.TokenResponse{
			AccessToken: "manual-test-refreshed-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		},
	})
	oauthSvc.Start()
	defer stopGrokAuthorizationForTest(t, oauthSvc)

	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newRefreshAPI(repo, cache), providercore.NewGrokTokenRefresher(oauthSvc))

	token, err := tokenSource.GetAccessTokenForManualTest(context.Background(), providercore.CloneRecord(provider))
	require.NoError(t, err)
	require.Equal(t, "manual-test-refreshed-token", token)
	require.Equal(t, 1, repo.updateCredentialsCalls)
}

func TestGrokTokenProviderManualTestFallsBackToValidTokenOnRefreshFailure(t *testing.T) {
	provider := &providercore.Record{
		ID:          131,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: false,
		Credentials: map[string]any{
			"access_token":  "near-expiry-token",
			"refresh_token": "refresh-token",

			"expires_at": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339),
		},
	}
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*providercore.Record{131: provider}
	cache := &grokTokenCacheForProviderTest{lockResult: true}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newRefreshAPI(repo, cache), &tokenRefresherStub{
		err: errors.New("upstream refresh unavailable"),
	})

	token, err := tokenSource.GetAccessTokenForManualTest(context.Background(), providercore.CloneRecord(provider))
	require.NoError(t, err)
	require.Equal(t, "near-expiry-token", token)
}

func TestGrokTokenProviderManualTestReportsRefreshFailureWhenTokenExpired(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(132)
	provider.Schedulable = false
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	cache := &grokTokenCacheForProviderTest{lockResult: true}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newRefreshAPI(repo, cache), &tokenRefresherStub{
		err: errors.New("invalid_client: client credentials rejected"),
	})

	token, err := tokenSource.GetAccessTokenForManualTest(context.Background(), providercore.CloneRecord(provider))
	require.Error(t, err)
	require.Empty(t, token)
	require.Contains(t, err.Error(), "invalid_client")
}

func TestGrokTokenProviderManualTestLockHeldWithExpiredTokenReturnsSpecificError(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(133)
	repo := &tokenRefreshProviderRepo{}
	repo.providersByID = map[int64]*providercore.Record{provider.ID: provider}
	cache := &grokTokenCacheForProviderTest{lockResult: false}
	tokenSource := newGrokTokenSourceForTest(repo, cache)
	bindGrokRefreshForTest(tokenSource, newRefreshAPI(repo, cache), &tokenRefresherStub{})

	token, err := tokenSource.GetAccessTokenForManualTest(context.Background(), providercore.CloneRecord(provider))
	require.Error(t, err)
	require.Empty(t, token)
	require.Contains(t, err.Error(), "refresh is already in progress")
}

func TestGrokTokenProviderManualTestRequiresRefreshToken(t *testing.T) {
	provider := expiredGrokOAuthProviderForCredentialTest(134)
	delete(provider.Credentials, "refresh_token")
	tokenSource := newGrokTokenSourceForTest(&tokenRefreshProviderRepo{}, &grokTokenCacheForProviderTest{})

	token, err := tokenSource.GetAccessTokenForManualTest(context.Background(), providercore.CloneRecord(provider))
	require.ErrorIs(t, err, providercore.ErrGrokOAuthRefreshTokenMissing)
	require.Empty(t, token)
}

func TestGrokTokenProviderRejectsIneligibleSelectedProviderBeforeWarmCache(t *testing.T) {
	future := time.Now().Add(time.Hour)
	tests := []struct {
		name   string
		mutate func(*providercore.Record)
	}{
		{name: "disabled", mutate: func(provider *providercore.Record) { provider.Status = billing.StatusDisabled }},
		{name: "not schedulable", mutate: func(provider *providercore.Record) { provider.Schedulable = false }},
		{name: "temporarily unschedulable", mutate: func(provider *providercore.Record) { provider.TempUnschedulableUntil = &future }},
		{name: "rate limited", mutate: func(provider *providercore.Record) { provider.RateLimitResetAt = &future }},
		{name: "overloaded", mutate: func(provider *providercore.Record) { provider.OverloadUntil = &future }},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := expiredGrokOAuthProviderForCredentialTest(int64(90 + index))
			provider.Credentials["access_token"] = "warm-cache-token"
			provider.Credentials["expires_at"] = time.Now().Add(2 * providercore.GrokTokenRefreshSkew).UTC().Format(time.RFC3339)
			tt.mutate(provider)
			cache := &grokTokenCacheForProviderTest{token: "warm-cache-token"}
			tokenSource := newGrokTokenSourceForTest(&tokenRefreshProviderRepo{}, cache)

			token, err := tokenSource.GetAccessToken(context.Background(), providercore.CloneRecord(provider))

			require.ErrorIs(t, err, providercore.ErrRefreshProviderStateChanged)
			require.Empty(t, token)
			require.Zero(t, cache.getCalls, "an ineligible selected provider must be rejected before cache lookup")
		})
	}
}

func (s *grokOAuthClientStub) ExchangeCode(_ context.Context, _, _, redirectURI, _, _ string) (*xai.TokenResponse, error) {
	s.exchangeCalls++
	s.exchangeRedirectURI = redirectURI
	return &xai.TokenResponse{AccessToken: "access-token"}, nil
}

func (s *grokOAuthClientStub) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return s.refreshResponse, nil
}

func (s *grokOAuthClientStub) LoginWithPassword(_ context.Context, email, password, _ string) (*providercore.GrokPasswordLoginResult, error) {
	s.loginEmail = email
	s.loginPassword = password
	return s.loginResult, nil
}

func (s *grokOAuthClientStub) ConvertSSOToBuild(context.Context, string, string) (*xai.TokenResponse, error) {
	return s.ssoResponse, nil
}

// newGrokAuthorizationForTest configures the authorization client and password-login switch.
func newGrokAuthorizationForTest(proxies egress.ProxyRepository, client providercore.GrokAuthorizationClient, enabled ...bool) *providercore.GrokAuthorization {
	options := GrokAuthorizationOptions(proxies, func() bool { return len(enabled) > 0 && enabled[0] })
	return providercore.NewGrokAuthorization(client, options)
}

func stopGrokAuthorizationForTest(t *testing.T, authorization *providercore.GrokAuthorization) {
	t.Helper()
	require.NoError(t, authorization.StopContext(context.Background()))
}

func (r *grokCredentialRaceRepo) GetByID(ctx context.Context, id int64) (*providercore.Record, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tokenRefreshProviderRepo.GetByID(ctx, id)
}

func (r *grokCredentialRaceRepo) setProvider(provider *providercore.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providersByID[provider.ID] = provider
}

func (c *grokTokenCacheForProviderTest) GetAccessToken(context.Context, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getCalls++
	if c.token == "" {
		return "", errors.New("not cached")
	}
	return c.token, nil
}

func (c *grokTokenCacheForProviderTest) SetAccessToken(_ context.Context, key string, token string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setKey = key
	c.setToken = token
	c.setTTL = ttl
	return nil
}

func (c *grokTokenCacheForProviderTest) DeleteAccessToken(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deletedKeys = append(c.deletedKeys, key)
	return c.deleteErr
}

func (c *grokTokenCacheForProviderTest) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return c.lockResult, nil
}

func (c *grokTokenCacheForProviderTest) ReleaseRefreshLock(context.Context, string) error {
	c.releaseCalls++
	return nil
}

// newGrokTokenSourceForTest binds provider reads, token caching, and refresh policy.
func newGrokTokenSourceForTest(repo providercore.RefreshRepository, cache providercore.AccessTokenCache) *providercore.GrokTokenSource {
	return &providercore.GrokTokenSource{Repository: repo, Cache: cache, Policy: providercore.GrokProviderRefreshPolicy()}
}

// bindGrokRefreshForTest delegates locking and persistence to the shared refresh coordinator.
func bindGrokRefreshForTest(source *providercore.GrokTokenSource, refresh *providercore.OAuthRefreshAPI, executor providercore.OAuthRefreshExecutor) {
	source.Refresh = func(ctx context.Context, value *providercore.Record, window time.Duration) (*providercore.OAuthRefreshResult, error) {
		return refresh.RefreshIfNeeded(ctx, value, executor, window)
	}
}

func expiredGrokOAuthProviderForCredentialTest(id int64) *providercore.Record {
	return &providercore.Record{
		ID:          id,
		Platform:    capability.PlatformGrok,
		Type:        capability.ProviderTypeOAuth,
		Status:      billing.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token":  "expired-access-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
			"base_url":      xai.DefaultCLIBaseURL,
		},
	}
}
