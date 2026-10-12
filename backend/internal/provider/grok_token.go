package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

const (
	GrokTokenCacheSkew          = 5 * time.Minute
	GrokRequestRefreshTimeout   = 8 * time.Second
	GrokRefreshLockWaitTimeout  = 2 * time.Second
	GrokRefreshLockPollInterval = 25 * time.Millisecond
)

var (
	ErrGrokOAuthRefreshNotConfigured = errors.New("grok oauth refresh is not configured")
	ErrGrokOAuthRefreshTokenMissing  = errors.New("grok oauth refresh token is missing")
	ErrGrokOAuthAccessTokenMissing   = errors.New("grok oauth access token is missing")
	ErrGrokOAuthAccessTokenExpired   = errors.New("grok oauth access token is expired")
	ErrGrokOAuthConfiguredProxyMiss  = errors.New("grok oauth configured proxy is missing")
)

type GrokTokenSource struct {
	Cache      AccessTokenCache
	Repository RefreshRepository
	Policy     ProviderRefreshPolicy
	Refresh    func(context.Context, *Record, time.Duration) (*OAuthRefreshResult, error)
}

// GrokStoredAccessToken reads the stored token or API key appropriate to the provider type.
func GrokStoredAccessToken(value *Record) (string, error) {
	switch value.Type {
	case capability.ProviderTypeOAuth:
		token := value.GetGrokAccessToken()
		if token == "" {
			return "", errors.New("grok access_token not found in credentials")
		}
		return token, nil
	case capability.ProviderTypeSetupToken:
		token := value.GetCredential("access_token")
		if token == "" {
			return "", errors.New("access_token not found in credentials")
		}
		return token, nil
	case capability.ProviderTypeAPIKey:
		token := value.GetCredential("api_key")
		if token == "" {
			return "", errors.New("api_key not found in credentials")
		}
		return token, nil
	case capability.ProviderTypeBedrock:
		return "", nil
	case capability.ProviderTypeServiceAccount:
		return "", fmt.Errorf("unsupported service account platform: %s", value.Platform)
	default:
		return "", fmt.Errorf("unsupported provider type: %s", value.Type)
	}
}

// @project-doc docs/interfaces/grok_upstream.md#grok_account_contract
func (p *GrokTokenSource) GetAccessToken(ctx context.Context, provider *Record) (string, error) {
	if provider == nil {
		return "", errors.New("provider is nil")
	}
	if provider.Platform != PlatformGrok || provider.Type != ProviderTypeOAuth {
		return "", errors.New("not a grok oauth provider")
	}
	selectedProxyID := CloneGrokProxyID(provider.ProxyID)
	if eligibilityErr := GrokOAuthRequestProviderEligibilityError(provider); eligibilityErr != nil {
		return "", WithGrokCredentialFailureSnapshot(eligibilityErr, provider)
	}

	expiresAt := provider.GetCredentialAsTime("expires_at")
	providerAccessToken := strings.TrimSpace(provider.GetGrokAccessToken())
	if providerAccessToken == "" {
		return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthAccessTokenMissing, provider)
	}
	if strings.TrimSpace(provider.GetGrokRefreshToken()) == "" {
		return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthRefreshTokenMissing, provider)
	}
	cacheKey := GrokTokenCacheKey(provider)
	if p.Cache != nil {
		if token, err := p.Cache.GetAccessToken(ctx, cacheKey); err == nil {
			cachedToken := strings.TrimSpace(token)
			if cachedToken != "" && providerAccessToken != "" && cachedToken == providerAccessToken &&
				expiresAt != nil && time.Until(*expiresAt) > GrokTokenRefreshSkew {
				return cachedToken, nil
			}
		}
	}

	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= GrokTokenRefreshSkew
	if needsRefresh {
		if p.Refresh == nil {
			return "", ErrGrokOAuthRefreshNotConfigured
		}
		refreshCtx, cancel := context.WithTimeout(ctx, GrokRequestRefreshTimeout)
		defer cancel()
		result, err := p.Refresh(WithRefreshRequestPath(refreshCtx), provider, GrokTokenRefreshSkew)
		if err != nil {
			if p.Policy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
		} else if result != nil && result.LockHeld {
			if p.Policy.OnLockHeld == ProviderLockHeldWaitForCache {
				token, waitErr := p.WaitForRefreshedToken(refreshCtx, provider, cacheKey)
				return token, WithGrokCredentialFailureSnapshot(waitErr, provider)
			}
			if expiresAt == nil || !time.Now().Before(*expiresAt) {
				return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthAccessTokenExpired, provider)
			}
		} else if result != nil && result.Provider != nil {
			if eligibilityErr := GrokOAuthRequestProviderEligibilityError(result.Provider); eligibilityErr != nil {
				return "", WithGrokCredentialFailureSnapshot(eligibilityErr, result.Provider)
			}
			if !GrokCredentialProxyIDsEqual(result.Provider.ProxyID, selectedProxyID) {
				return "", WithGrokCredentialFailureSnapshot(ErrRefreshProviderStateChanged, result.Provider)
			}
			provider = result.Provider
			expiresAt = provider.GetCredentialAsTime("expires_at")
		}
	}

	accessToken := provider.GetGrokAccessToken()
	if strings.TrimSpace(accessToken) == "" {
		return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthAccessTokenMissing, provider)
	}
	if expiresAt != nil && !time.Now().Before(*expiresAt) {
		return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthAccessTokenExpired, provider)
	}

	if p.Cache != nil {
		latestProvider, isStale := CheckTokenVersion(ctx, provider, p.Repository)
		if isStale && latestProvider != nil {
			if eligibilityErr := GrokOAuthRequestProviderEligibilityError(latestProvider); eligibilityErr != nil {
				return "", WithGrokCredentialFailureSnapshot(eligibilityErr, latestProvider)
			}
			if !GrokCredentialProxyIDsEqual(latestProvider.ProxyID, selectedProxyID) {
				return "", WithGrokCredentialFailureSnapshot(ErrRefreshProviderStateChanged, latestProvider)
			}
			accessToken = latestProvider.GetGrokAccessToken()
			if strings.TrimSpace(accessToken) == "" {
				return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthAccessTokenMissing, latestProvider)
			}
			latestExpiry := latestProvider.GetCredentialAsTime("expires_at")
			if latestExpiry == nil || !time.Now().Before(*latestExpiry) {
				return "", WithGrokCredentialFailureSnapshot(ErrGrokOAuthAccessTokenExpired, latestProvider)
			}
		} else {
			ttl := 30 * time.Minute
			if expiresAt != nil {
				until := time.Until(*expiresAt)
				switch {
				case until > GrokTokenCacheSkew:
					ttl = until - GrokTokenCacheSkew
				case until > 0:
					ttl = until
				default:
					ttl = time.Minute
				}
			}
			_ = p.Cache.SetAccessToken(ctx, cacheKey, accessToken, ttl)
		}
	}

	return accessToken, nil
}

// GetAccessTokenForManualTest obtains credentials for an administrator's connection test.
// It skips request scheduling gates such as manual pauses, rate limits, and overload,
// so administrators can probe providers while diagnosing those states (#4598).
//
// Credential checks, configured-proxy validation, refresh locking, and provider rereads still apply.
// RefreshIfNeeded rejects rotation for inactive providers; a still-valid token can be used
// to probe them as configured.
func (p *GrokTokenSource) GetAccessTokenForManualTest(ctx context.Context, provider *Record) (string, error) {
	if provider == nil {
		return "", errors.New("provider is nil")
	}
	if provider.Platform != PlatformGrok || provider.Type != ProviderTypeOAuth {
		return "", errors.New("not a grok oauth provider")
	}
	if provider.ProxyID != nil && provider.Proxy == nil {
		return "", ErrGrokOAuthConfiguredProxyMiss
	}
	if strings.TrimSpace(provider.GetGrokRefreshToken()) == "" {
		return "", ErrGrokOAuthRefreshTokenMissing
	}

	accessToken := strings.TrimSpace(provider.GetGrokAccessToken())
	expiresAt := provider.GetCredentialAsTime("expires_at")
	tokenValid := accessToken != "" && expiresAt != nil && time.Now().Before(*expiresAt)
	if accessToken != "" && expiresAt != nil && time.Until(*expiresAt) > GrokTokenRefreshSkew {
		return accessToken, nil
	}

	if p.Refresh == nil {
		if tokenValid {
			return accessToken, nil
		}
		return "", ErrGrokOAuthRefreshNotConfigured
	}

	// Keep this refresh outside request-path classification so RefreshIfNeeded does not
	// reapply the scheduling gates that manual tests are intended to bypass.
	refreshCtx, cancel := context.WithTimeout(ctx, GrokRequestRefreshTimeout)
	defer cancel()
	result, err := p.Refresh(refreshCtx, provider, GrokTokenRefreshSkew)
	if err != nil {
		if tokenValid {
			return accessToken, nil
		}
		return "", err
	}
	if result != nil && result.LockHeld {
		if tokenValid {
			return accessToken, nil
		}
		return "", errors.New("token refresh is already in progress on another worker; retry in a few seconds")
	}
	if result != nil && result.Provider != nil {
		provider = result.Provider
	}

	accessToken = strings.TrimSpace(provider.GetGrokAccessToken())
	if accessToken == "" {
		return "", ErrGrokOAuthAccessTokenMissing
	}
	if latestExpiry := provider.GetCredentialAsTime("expires_at"); latestExpiry != nil && !time.Now().Before(*latestExpiry) {
		return "", ErrGrokOAuthAccessTokenExpired
	}
	return accessToken, nil
}

func (p *GrokTokenSource) WaitForRefreshedToken(ctx context.Context, provider *Record, cacheKey string) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, GrokRefreshLockWaitTimeout)
	defer cancel()

	initialToken := strings.TrimSpace(provider.GetGrokAccessToken())
	initialVersion := provider.GetCredentialAsInt64("_token_version")
	selectedProxyID := CloneGrokProxyID(provider.ProxyID)
	sawAuthoritativeState := false
	var lastProviderReadErr error
	ticker := time.NewTicker(GrokRefreshLockPollInterval)
	defer ticker.Stop()

	for {
		cachedToken := ""
		if p.Cache != nil {
			if token, err := p.Cache.GetAccessToken(waitCtx, cacheKey); err == nil {
				cachedToken = strings.TrimSpace(token)
			}
		}

		if p.Repository != nil {
			latest, err := p.Repository.GetByID(waitCtx, provider.ID)
			if err != nil {
				lastProviderReadErr = err
			} else if latest == nil {
				return "", ErrRefreshProviderStateChanged
			} else {
				sawAuthoritativeState = true
				if eligibilityErr := GrokOAuthRequestProviderEligibilityError(latest); eligibilityErr != nil {
					return "", WithGrokCredentialFailureSnapshot(eligibilityErr, latest)
				}
				if !GrokCredentialProxyIDsEqual(latest.ProxyID, selectedProxyID) {
					return "", WithGrokCredentialFailureSnapshot(ErrRefreshProviderStateChanged, latest)
				}
				token := strings.TrimSpace(latest.GetGrokAccessToken())
				version := latest.GetCredentialAsInt64("_token_version")
				expiresAt := latest.GetCredentialAsTime("expires_at")
				changed := token != initialToken || (version > 0 && version > initialVersion)
				valid := expiresAt != nil && time.Now().Before(*expiresAt)
				if token != "" && changed && valid {
					// Use the newer valid database token and update a conflicting cached token.
					if cachedToken != "" && cachedToken != token {
						ttl := time.Until(*expiresAt)
						if ttl > GrokTokenCacheSkew {
							ttl -= GrokTokenCacheSkew
						}
						_ = p.Cache.SetAccessToken(waitCtx, cacheKey, token, ttl)
					}
					return token, nil
				}
			}
		}

		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if !sawAuthoritativeState {
				if lastProviderReadErr == nil {
					lastProviderReadErr = waitCtx.Err()
				}
				return "", fmt.Errorf("%w: %v", ErrRefreshProviderRereadFailed, lastProviderReadErr)
			}
			// The refresh owner may commit just after this bounded wait ends.
			// Report changed state so callers avoid isolating credentials still being refreshed.
			return "", ErrRefreshProviderStateChanged
		case <-ticker.C:
		}
	}
}

func GrokOAuthRequestProviderEligibilityError(provider *Record) error {
	if provider == nil || !provider.IsGrokOAuth() || !provider.IsSchedulable() {
		return ErrRefreshProviderStateChanged
	}
	if provider.ProxyID != nil && provider.Proxy == nil {
		return ErrGrokOAuthConfiguredProxyMiss
	}
	return nil
}

func CloneGrokProxyID(proxyID *int64) *int64 {
	if proxyID == nil {
		return nil
	}
	value := *proxyID
	return &value
}

func (p *GrokTokenSource) InvalidateToken(ctx context.Context, provider *Record) error {
	if p == nil || p.Cache == nil || provider == nil {
		return nil
	}
	return p.Cache.DeleteAccessToken(ctx, GrokTokenCacheKey(provider))
}

func GrokTokenCacheKey(provider *Record) string {
	if provider == nil {
		return "grok:provider:0"
	}
	return "grok:provider:" + strconv.FormatInt(provider.ID, 10)
}
