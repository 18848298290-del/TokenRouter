package provider

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Antigravity requests accept OAuth records and prefer cached tokens before
// inspecting credential expiry. Near-expiry refresh receives an eight-second
// child budget; failure attempts a temporary scheduling pause before policy
// decides whether the current request keeps its stored token.
//
// Project backfill uses a per-row cooldown recorded before discovery, so
// failed discovery retains the cooldown. Checking and recording that cooldown
// are separate sync.Map operations and permit overlapping attempts. Successful
// discovery mutates the local credentials before best-effort persistence;
// callers therefore synchronize the record and supply its credential map.
// Cache identity is selected before refresh or backfill, and a repository
// version check can replace the returned token while suppressing publication
// of a known stale observation.

// Antigravity token constants select legacy project lookup, request refresh
// and cache margins, project retry spacing, and the child refresh budget.
const (
	antigravityProjectIDFallbackCredentialKey = "antigravity_project_id"

	antigravityTokenRefreshSkew = 3 * time.Minute
	antigravityTokenCacheSkew   = 5 * time.Minute
	antigravityBackfillCooldown = 5 * time.Minute
	// antigravityRequestRefreshTimeout bounds token refresh on the request path.
	// Refresh failures temporarily pause the provider; the configured policy
	// decides whether to return the error.
	// Background refresh can retry on a later cycle.
	antigravityRequestRefreshTimeout = 8 * time.Second
)

// AntigravityTokenState retains per-row project discovery cooldown
// observations across requests.
type AntigravityTokenState struct {
	backfillCooldown sync.Map // per-row discovery attempt timestamps
}

// AntigravityTokenSource shares project cooldown state with its configured
// credential consumers.
type AntigravityTokenSource struct {
	state AntigravityTokenState // retained project backfill cooldowns
	// Options copies callback values and borrows shared services. Configure it
	// before request use and retain its dependencies while lookups are active.
	Options AntigravityTokenOptions
}

// AntigravityTokenOptions supplies shared refresh, project persistence, pause,
// and cache dependencies.
type AntigravityTokenOptions struct {
	// Cache optionally supplies tokens and refresh admission. Nil skips cache
	// lookup and publication while refresh and the credential record remain
	// available.
	Cache AccessTokenCache
	// Repository optionally observes completed rotation before cache publication.
	// Missing or failed reads let CheckTokenVersion retain this request snapshot.
	Repository RefreshRepository
	// Policy chooses errors or stored tokens after refresh failure. Its peer
	// option performs one immediate cache reread when the coordinator reports
	// LockHeld.
	Policy ProviderRefreshPolicy
	// Refresh optionally invokes rotation under a child deadline. Success must
	// return a nonnil result and a usable record whenever LockHeld is false.
	Refresh func(context.Context, *Record, time.Duration) (*OAuthRefreshResult, error)
	// FillProject optionally repairs absent project_id using the selected token.
	// Discovery is attempted after recording a cooldown even when it later fails.
	FillProject func(context.Context, *Record, string) (string, error)
	// Persist is required after project repair. A failure leaves the local
	// credential assignment in place and requires Warn for its diagnostic.
	Persist func(context.Context, *Record, map[string]any) error
	// SetTempUnschedulable optionally persists request refresh failure as a pause.
	// Its absence skips pause handling; failure prevents the following cache
	// publication.
	SetTempUnschedulable func(context.Context, int64, time.Time, string) error
	// TempUnschedCache optionally publishes a successfully stored pause
	// synchronously.
	// A cache error is logged while the durable scheduling pause remains in place.
	TempUnschedCache TempUnschedCache
	// Warn reports persistence and pause failures; Debug reports stale-version
	// replacement. Both callbacks are required whenever their respective branch
	// is reached.
	// Warn: pause diagnostics; Debug: token version diagnostics.
	Warn, Debug func(string, ...any)
}

// ResolveAntigravityProjectID returns the first trimmed project from current
// credentials, fallback credentials, then fallback Extra. Nil or unresolved
// records return the supplied missing error, which may itself be nil.
//
// The compatibility key is checked in both credential and Extra
// representations after the current project_id. That ordering lets the current
// credential override retained fallback metadata, while a blank or absent
// value advances to the next representation. Callers provide the error
// appropriate to their execution path when no project is available.
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

// GetAccessToken delegates request credentials to the retained
// AntigravityTokenState. Its options and mutable record stay stable during
// lookup, and the shared state keeps backfill cooldowns across consumers.
func (s *AntigravityTokenSource) GetAccessToken(ctx context.Context, value *Record) (string, error) {
	return s.state.GetAccessToken(ctx, value, s.Options)
}

// GetAccessToken returns cached or selected Antigravity OAuth credentials. Nil
// records, other platforms, and other types fail; successful refresh requires
// a nonnil result and replacement record when no peer holds the lock.
//
// Refresh failure first attempts a detached scheduling pause, then policy
// selects an error or stored token. Missing-project backfill records its
// cooldown before discovery and mutates the supplied credential map before
// persistence, whose failure is logged. Backfill admission is not an atomic
// claim. A newer stored token version supersedes the selected token and
// prevents stale cache publication; otherwise expiry determines lifetime under
// the entry key captured before repair. Callers synchronize record mutation
// and provide reached callbacks.
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

	// A populated cache token bypasses expiry and project checks. Read errors
	// and blank values instead permit refresh or the stored-credential path below.
	if options.Cache != nil {
		if token, err := options.Cache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
	}

	// Missing expiry also triggers optional refresh here. The request path
	// therefore differs from background checks that require a parseable expiry
	// value.
	expiresAt := provider.GetCredentialAsTime("expires_at")
	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= antigravityTokenRefreshSkew
	if needsRefresh && options.Refresh != nil {
		// Give coordinator work a child deadline before entering exchange. Timeout
		// cancellation still depends on the callback returning, and later scheduling
		// receives a pause.
		refreshCtx, cancel := context.WithTimeout(ctx, antigravityRequestRefreshTimeout)
		defer cancel()
		result, err := options.Refresh(refreshCtx, provider, antigravityTokenRefreshSkew)
		if err != nil {
			// Write the pause before choosing this request result.
			// Strict policy returns the refresh error while
			// tolerant policy continues with the stored token.
			p.markTempUnschedulable(provider, err, options)
			if options.Policy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
		} else if result.LockHeld {
			// LockHeld reports unconfirmed refresh admission. The wait policy performs
			// one cache reread here without sleeping; a miss leaves existing
			// credentials usable.
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
		// Without a refresh callback, optional cache admission still guards lookup
		// and publication. Granted locks are released on exit; admission errors are
		// ignored.
		locked, err := options.Cache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second)
		if err == nil && locked {
			defer func() { _ = options.Cache.ReleaseRefreshLock(ctx, cacheKey) }()
		}
	}

	accessToken := provider.GetCredential("access_token")
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("access_token not found in credentials")
	}

	// Record cooldown before project discovery so an error still delays the next
	// attempt. The separate eligibility check and store can admit simultaneous
	// requests.
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

	// A repository observation newer than this snapshot replaces the selected
	// token and suppresses publication; a later rotation can still race with a
	// cache write.
	if options.Cache != nil {
		latestProvider, isStale := CheckTokenVersion(ctx, provider, options.Repository, options.Debug)
		if isStale && latestProvider != nil {
			options.Debug("antigravity_token_version_stale_use_latest", "provider_id", provider.ID)
			accessToken = latestProvider.GetCredential("access_token")
			if strings.TrimSpace(accessToken) == "" {
				return "", errors.New("access_token not found after version check")
			}
		} else {
			// Unknown expiry uses the default lifetime. Known expiry reserves the
			// cache margin when possible, then permits positive remaining time or a
			// short expired fallback.
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
			// Cache storage is best effort after selecting a usable token. Failure
			// leaves this request able to proceed with its returned credentials.
			_ = options.Cache.SetAccessToken(ctx, cacheKey, accessToken, ttl)
		}
	}

	return accessToken, nil
}

// shouldAttemptBackfill reports whether the row lacks a usable cooldown or its
// elapsed duration exceeds the configured interval. This observation reserves
// nothing; concurrent callers can both proceed before recording their
// attempts.
func (p *AntigravityTokenState) shouldAttemptBackfill(providerID int64) bool {
	if v, ok := p.backfillCooldown.Load(providerID); ok {
		if lastAttempt, ok := v.(time.Time); ok {
			return time.Since(lastAttempt) > antigravityBackfillCooldown
		}
	}
	return true
}

// markTempUnschedulable writes a temporary pause after request refresh
// failure. Missing provider or writer skips work, and a nonnil refresh error
// plus reached Warn are required.
//
// Persistence uses an unbounded background context because the request budget
// may already be exhausted. A failed write is logged and prevents cache
// publication; success optionally updates the pause cache synchronously. Cache
// failure leaves the durable pause in place and is logged, so later scheduling
// visibility depends on its own storage and cache read path.
func (p *AntigravityTokenState) markTempUnschedulable(provider *Record, refreshErr error, options AntigravityTokenOptions) {
	if options.SetTempUnschedulable == nil || provider == nil {
		return
	}
	now := time.Now()
	until := now.Add(TokenRefreshTempUnschedDuration)
	reason := "token refresh failed on request path: " + refreshErr.Error()
	// Detach pause persistence from the expired request so later scheduling
	// can observe refresh failure even when this request no longer has a budget.
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
	// Publish cache state only after durable pause success. A failed cache
	// update retains that stored pause and leaves later readers to their normal
	// fallback.
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

// markBackfillAttempted stores the row discovery time in the shared cooldown
// map. Callers invoke it before discovery, so failures consume the interval;
// it does not combine the preceding eligibility check atomically.
func (p *AntigravityTokenState) markBackfillAttempted(providerID int64) {
	p.backfillCooldown.Store(providerID, time.Now())
}

// AntigravityTokenCacheKey selects a trimmed project key or row-ID fallback.
// Pass a nonnil record whose credentials stay stable; rows sharing a project
// address the same Antigravity token cache entry.
func AntigravityTokenCacheKey(provider *Record) string {
	projectID := strings.TrimSpace(provider.GetCredential("project_id"))
	if projectID != "" {
		return "ag:" + projectID
	}
	return "ag:provider:" + strconv.FormatInt(provider.ID, 10)
}
