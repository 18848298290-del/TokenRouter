package provider

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	antigravityProjectIDFallbackCredentialKey = "antigravity_project_id"

	antigravityTokenRefreshSkew = 3 * time.Minute
	antigravityTokenCacheSkew   = 5 * time.Minute
	antigravityBackfillCooldown = 5 * time.Minute
	// antigravityRequestRefreshTimeout bounds token refresh on the request path.
	// Refresh failures temporarily pause the provider; the configured policy decides whether to return the error.
	// Background refresh can retry on a later cycle.
	antigravityRequestRefreshTimeout = 8 * time.Second
)

type AntigravityTokenState struct{ backfillCooldown sync.Map }

// AntigravityTokenSource owns project backfill cooldowns and is shared by its consumers.
type AntigravityTokenSource struct {
	state   AntigravityTokenState
	Options AntigravityTokenOptions
}

type AntigravityTokenOptions struct {
	Cache                AccessTokenCache
	Repository           RefreshRepository
	Policy               ProviderRefreshPolicy
	Refresh              func(context.Context, *Record, time.Duration) (*OAuthRefreshResult, error)
	FillProject          func(context.Context, *Record, string) (string, error)
	Persist              func(context.Context, *Record, map[string]any) error
	SetTempUnschedulable func(context.Context, int64, time.Time, string) error
	TempUnschedCache     TempUnschedCache
	Warn, Debug          func(string, ...any)
}

func ResolveAntigravityProjectID(provider *Record, missing error) (string, error) {
	if provider == nil {
		return "", missing
	}
	if projectID := strings.TrimSpace(provider.GetCredential("project_id")); projectID != "" {
		return projectID, nil
	}
	if projectID := strings.TrimSpace(provider.GetCredential(antigravityProjectIDFallbackCredentialKey)); projectID != "" {
		return projectID, nil
	}
	if projectID := strings.TrimSpace(provider.GetExtraString(antigravityProjectIDFallbackCredentialKey)); projectID != "" {
		return projectID, nil
	}
	return "", missing
}

func (s *AntigravityTokenSource) GetAccessToken(ctx context.Context, value *Record) (string, error) {
	return s.state.GetAccessToken(ctx, value, s.Options)
}

// GetAccessToken reads a cached token or refreshes near expiry according to the configured policy.
// @project-doc docs/interfaces/antigravity_upstream.md#antigravity_account_contract
func (p *AntigravityTokenState) GetAccessToken(ctx context.Context, provider *Record, options AntigravityTokenOptions) (string, error) {
	if provider == nil {
		return "", errors.New("provider is nil")
	}
	if provider.Platform != PlatformAntigravity {
		return "", errors.New("not an antigravity provider")
	}

	if provider.Type != ProviderTypeOAuth {
		return "", errors.New("not an antigravity oauth provider")
	}

	cacheKey := AntigravityTokenCacheKey(provider)

	// Use the cache before attempting refresh.
	if options.Cache != nil {
		if token, err := options.Cache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
	}

	// Refresh within the pre-expiry window.
	expiresAt := provider.GetCredentialAsTime("expires_at")
	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= antigravityTokenRefreshSkew
	if needsRefresh && options.Refresh != nil {
		// Bound request-time refresh latency; background refresh can retry later.
		refreshCtx, cancel := context.WithTimeout(ctx, antigravityRequestRefreshTimeout)
		defer cancel()
		result, err := options.Refresh(refreshCtx, provider, antigravityTokenRefreshSkew)
		if err != nil {
			// Pause the provider so subsequent scheduling skips it.
			p.markTempUnschedulable(provider, err, options)
			if options.Policy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
		} else if result.LockHeld {
			if options.Policy.OnLockHeld == ProviderLockHeldWaitForCache && options.Cache != nil {
				if token, cacheErr := options.Cache.GetAccessToken(ctx, cacheKey); cacheErr == nil && strings.TrimSpace(token) != "" {
					return token, nil
				}
			}
			// The default policy continues with the existing token.
		} else {
			provider = result.Provider
			expiresAt = provider.GetCredentialAsTime("expires_at")
		}
	} else if needsRefresh && options.Cache != nil {
		// Acquire the cache lock when no refresh coordinator is injected.
		locked, err := options.Cache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second)
		if err == nil && locked {
			defer func() { _ = options.Cache.ReleaseRefreshLock(ctx, cacheKey) }()
		}
	}

	accessToken := provider.GetCredential("access_token")
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("access_token not found in credentials")
	}

	// Backfill a missing project_id with a per-provider cooldown.
	if strings.TrimSpace(provider.GetCredential("project_id")) == "" && options.FillProject != nil {
		if p.shouldAttemptBackfill(provider.ID) {
			p.markBackfillAttempted(provider.ID)
			if projectID, err := options.FillProject(ctx, provider, accessToken); err == nil && projectID != "" {
				provider.Credentials["project_id"] = projectID
				if updateErr := options.Persist(ctx, provider, provider.Credentials); updateErr != nil {
					options.Warn("antigravity_project_id_backfill_persist_failed",
						"provider_id", provider.ID,
						"error", updateErr,
					)
				}
			}
		}
	}

	// Recheck credential version before caching the token.
	if options.Cache != nil {
		latestProvider, isStale := CheckTokenVersion(ctx, provider, options.Repository, options.Debug)
		if isStale && latestProvider != nil {
			options.Debug("antigravity_token_version_stale_use_latest", "provider_id", provider.ID)
			accessToken = latestProvider.GetCredential("access_token")
			if strings.TrimSpace(accessToken) == "" {
				return "", errors.New("access_token not found after version check")
			}
		} else {
			ttl := 30 * time.Minute
			if expiresAt != nil {
				until := time.Until(*expiresAt)
				switch {
				case until > antigravityTokenCacheSkew:
					ttl = until - antigravityTokenCacheSkew
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

// shouldAttemptBackfill reports whether the provider's backfill cooldown has elapsed.
func (p *AntigravityTokenState) shouldAttemptBackfill(providerID int64) bool {
	if v, ok := p.backfillCooldown.Load(providerID); ok {
		if lastAttempt, ok := v.(time.Time); ok {
			return time.Since(lastAttempt) > antigravityBackfillCooldown
		}
	}
	return true
}

// markTempUnschedulable pauses a provider after request-time refresh failure.
// It writes the scheduling state through the configured store and cache.
// Use a background context because the request may already have timed out.
func (p *AntigravityTokenState) markTempUnschedulable(provider *Record, refreshErr error, options AntigravityTokenOptions) {
	if options.SetTempUnschedulable == nil || provider == nil {
		return
	}
	now := time.Now()
	until := now.Add(TokenRefreshTempUnschedDuration)
	reason := "token refresh failed on request path: " + refreshErr.Error()
	bgCtx := context.Background()
	if err := options.SetTempUnschedulable(bgCtx, provider.ID, until, reason); err != nil {
		options.Warn("antigravity_token_provider.set_temp_unschedulable_failed",
			"provider_id", provider.ID,
			"error", err,
		)
		return
	}
	options.Warn("antigravity_token_provider.temp_unschedulable_set",
		"provider_id", provider.ID,
		"until", until.Format(time.RFC3339),
		"reason", reason,
	)
	// Update the cache synchronously so scheduling can observe the pause.
	if options.TempUnschedCache != nil {
		state := &TempUnschedState{
			UntilUnix:       until.Unix(),
			TriggeredAtUnix: now.Unix(),
			ErrorMessage:    reason,
		}
		if err := options.TempUnschedCache.SetTempUnsched(bgCtx, provider.ID, state); err != nil {
			options.Warn("antigravity_token_provider.temp_unsched_cache_set_failed",
				"provider_id", provider.ID,
				"error", err,
			)
		}
	}
}

func (p *AntigravityTokenState) markBackfillAttempted(providerID int64) {
	p.backfillCooldown.Store(providerID, time.Now())
}

func AntigravityTokenCacheKey(provider *Record) string {
	projectID := strings.TrimSpace(provider.GetCredential("project_id"))
	if projectID != "" {
		return "ag:" + projectID
	}
	return "ag:provider:" + strconv.FormatInt(provider.ID, 10)
}
