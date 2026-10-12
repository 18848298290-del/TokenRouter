package provider

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	GeminiTokenRefreshSkew = 3 * time.Minute
	GeminiTokenCacheSkew   = 5 * time.Minute
)

type GeminiTokenOptions struct {
	Debug, Warn  func(string, ...any)
	Cache        AccessTokenCache
	Repository   RefreshRepository
	Policy       ProviderRefreshPolicy
	Refresh      func(context.Context, *Record, time.Duration) (*OAuthRefreshResult, error)
	Vertex       func(context.Context, *Record) (string, error)
	Project      func(context.Context, string, string) (string, string, error)
	ResolveProxy func(context.Context, int64) (string, bool)
	Persist      func(context.Context, *Record, map[string]any) error
	Logf         func(string, ...any)
}

// GeminiTokenSource obtains request credentials and optionally discovers and persists a missing project.
type GeminiTokenSource struct {
	Options GeminiTokenOptions
}

func (s *GeminiTokenSource) GetAccessToken(ctx context.Context, value *Record) (string, error) {
	return GetGeminiAccessToken(ctx, value, s.Options)
}

func GeminiOAuthTokenCacheKey(provider *Record) string {
	projectID := strings.TrimSpace(provider.GetCredential("project_id"))
	if projectID != "" {
		return "gemini:" + projectID
	}
	return "gemini:provider:" + strconv.FormatInt(provider.ID, 10)
}

func GetGeminiAccessToken(ctx context.Context, provider *Record, options GeminiTokenOptions) (string, error) {
	if provider == nil {
		return "", errors.New("provider is nil")
	}
	if provider.Platform != PlatformGemini || (provider.Type != ProviderTypeOAuth && provider.Type != ProviderTypeServiceAccount) {
		return "", errors.New("not a gemini oauth or service account")
	}
	if provider.Type == ProviderTypeServiceAccount {
		return options.Vertex(ctx, provider)
	}

	cacheKey := GeminiOAuthTokenCacheKey(provider)

	if options.Cache != nil {
		if token, err := options.Cache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
	}

	// Refresh before expiry using the configured skew.
	expiresAt := provider.GetCredentialAsTime("expires_at")
	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= GeminiTokenRefreshSkew

	if needsRefresh && options.Refresh != nil {
		result, err := options.Refresh(ctx, provider, GeminiTokenRefreshSkew)
		if err != nil {
			if options.Policy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
		} else if result.LockHeld {
			if options.Policy.OnLockHeld == ProviderLockHeldWaitForCache && options.Cache != nil {
				if token, cacheErr := options.Cache.GetAccessToken(ctx, cacheKey); cacheErr == nil && strings.TrimSpace(token) != "" {
					return token, nil
				}
			}
			options.debug("gemini_token_lock_held_use_old", "provider_id", provider.ID)
		} else {
			provider = result.Provider
			expiresAt = provider.GetCredentialAsTime("expires_at")
		}
	} else if needsRefresh && options.Cache != nil {
		// With no refresh callback, retain the cache lock coordination path.
		locked, lockErr := options.Cache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second)
		if lockErr == nil && locked {
			defer func() { _ = options.Cache.ReleaseRefreshLock(ctx, cacheKey) }()
		} else if lockErr != nil {
			options.warn("gemini_token_lock_failed", "provider_id", provider.ID, "error", lockErr)
		}
	}

	accessToken := provider.GetCredential("access_token")
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("access_token not found in credentials")
	}

	// Project discovery is opt-in. A missing project still permits returning the access token.
	projectID := strings.TrimSpace(provider.GetCredential("project_id"))
	autoDetectProjectID := provider.GetCredential("auto_detect_project_id") == "true"

	if projectID == "" && autoDetectProjectID {
		if options.Project == nil {
			return accessToken, nil
		}

		var proxyURL string
		if provider.ProxyID != nil && options.ResolveProxy != nil {
			if resolved, ok := options.ResolveProxy(ctx, *provider.ProxyID); ok {
				proxyURL = resolved
			}
		}
		detected, tierID, err := options.Project(ctx, accessToken, proxyURL)
		if err != nil {
			options.Logf("[GeminiTokenProvider] Auto-detect project_id failed: %v, fallback to AI Studio API mode", err)
			return accessToken, nil
		}
		detected = strings.TrimSpace(detected)
		tierID = strings.TrimSpace(tierID)
		if detected != "" {
			if provider.Credentials == nil {
				provider.Credentials = make(map[string]any)
			}
			provider.Credentials["project_id"] = detected
			if tierID != "" {
				provider.Credentials["tier_id"] = tierID
			}
			_ = options.Persist(ctx, provider, provider.Credentials)
		}
	}

	// Check the stored token version before publishing to the cache.
	if options.Cache != nil {
		latestProvider, isStale := CheckTokenVersion(ctx, provider, options.Repository, options.Debug)
		if isStale && latestProvider != nil {
			options.debug("gemini_token_version_stale_use_latest", "provider_id", provider.ID)
			accessToken = latestProvider.GetCredential("access_token")
			if strings.TrimSpace(accessToken) == "" {
				return "", errors.New("access_token not found after version check")
			}
		} else {
			ttl := 30 * time.Minute
			if expiresAt != nil {
				until := time.Until(*expiresAt)
				switch {
				case until > GeminiTokenCacheSkew:
					ttl = until - GeminiTokenCacheSkew
				case until > 0:
					ttl = until
				default:
					ttl = time.Minute
				}
			}
			_ = options.Cache.SetAccessToken(ctx, cacheKey, accessToken, ttl)
		}
	}

	return accessToken, nil
}

func (o GeminiTokenOptions) debug(message string, args ...any) {
	if o.Debug != nil {
		o.Debug(message, args...)
	}
}

func (o GeminiTokenOptions) warn(message string, args ...any) {
	if o.Warn != nil {
		o.Warn(message, args...)
	}
}
