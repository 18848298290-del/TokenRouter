package provider

import (
	"context"
	"errors"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// openai_token.go selects OpenAI access tokens through cache lookup, refresh
// coordination, and stored-credential recovery.
// An OpenAI OAuth request can use a cached token, refresh near expiry or
// continue with captured credentials according to its error policy. A nonempty
// cache hit wins before stored expiry checks. PAT mode skips expiry-triggered
// refresh, while an expired renewable-mode record without a refresh token
// returns the reauthorization error after any configured disable actions.
//
// Configured refresh results can replace the observed record or request
// contention polling. Without that callback, the compatibility path acquires
// or waits on the cache lease but performs no token exchange. Before caching
// captured credentials, a repository version observation can select a newer
// token and skip that cache write. Failed version reads permit the current
// snapshot; a later concurrent rotation can still occur. Cache lifetime
// combines refresh-failure policy and observed expiry, and independently
// loaded metrics describe logical source events and requested waits.

// Expiry margins separately govern request-side refresh admission and cache
// lifetime selection. Recovery failures choose their policy lifetime before
// these expiry rules are considered.
const (
	openAITokenRefreshSkew = 3 * time.Minute // expiry refresh admission margin
	openAITokenCacheSkew   = 5 * time.Minute // ordinary cache expiry margin
	// Contention polling uses bounded base backoff with jitter applied afterward;
	// attempt counts bound cache reads but do not bound their client-controlled I/O time.
	openAILockInitialWait = 20 * time.Millisecond  // first polling base wait
	openAILockMaxWait     = 120 * time.Millisecond // maximum polling base wait
	openAILockMaxAttempts = 5                      // planned cache polling attempts
	openAILockJitterRatio = 0.2                    // approximate relative jitter amplitude
	// Warning thresholds use accumulated requested wait milliseconds, excluding
	// Redis command duration and the scheduler delay before a timer is consumed.
	openAILockWarnThresholdMs = 250 // requested wait warning threshold
)

// OpenAITokenSource resolves OpenAI OAuth tokens through cache, refresh
// callbacks and captured credentials.
type OpenAITokenSource struct {
	// Cache is required when Refresh can report LockHeld and policy selects
	// polling. Other uncached paths skip token publication, version rereads and
	// direct lease acquisition; adapters control retries and I/O timing.
	Cache AccessTokenCache
	// Repository supplies token-version observations before cache publication and
	// also gates missing-refresh-token disable actions, even though SetError performs that write.
	Repository RefreshRepository
	// SetError is required when disable actions reach persistence. It receives
	// a background context after any runtime block and before cache deletion.
	SetError func(context.Context, int64, string) error
	// Metrics is initialized before concurrent requests or snapshots. Counter operations
	// are atomic, but EnsureMetrics publishes this optional pointer without synchronization.
	Metrics *OpenAITokenMetricsStore
	Policy  ProviderRefreshPolicy // request refresh recovery settings
	// Refresh is optional and owns coordinated token exchange. Successful results
	// are nonnil. Non-lock-held results supply a nonnil Provider for the same
	// credential owner because the cache key was selected before this callback ran.
	Refresh func(context.Context, *Record, time.Duration) (*OAuthRefreshResult, error)
	// Block is optional and runs before missing-refresh-token persistence. Its
	// runtime action remains available even when that subsequent store operation fails.
	Block func(*Record, time.Time, string)
	// Debug and Warn observe cache, refresh and lock decisions. Configure both
	// before valid token requests; Debug reports path selection and Warn reports failures.
	Debug, Warn func(string, ...any)
}

// OpenAITokenRuntimeMetrics contains separately observed counters for
// request-side refresh and cache contention.
type OpenAITokenRuntimeMetrics struct {
	// RefreshRequests, RefreshSuccess and RefreshFailure count logical source
	// observations. Callback entry and direct lease entry count as requests;
	// Refreshed counts as success, while immediately propagated errors bypass failure counting.
	RefreshRequests    int64 // entered refresh or lease paths
	RefreshSuccess     int64 // callback reported refreshed results
	RefreshFailure     int64 // errors permitting stored-token recovery
	LockAcquireFailure int64 // direct lease acquisition errors
	LockContention     int64 // entries choosing contention polling
	LockWaitSamples    int64 // completed requested polling waits
	// LockWaitTotalMs sums requested whole-millisecond timer durations after waits
	// complete. It excludes Redis command time and actual timer scheduling delay.
	LockWaitTotalMs    int64
	LockWaitHit        int64 // polling returned nonblank token
	LockWaitMiss       int64 // polling exhausted all attempts
	LastObservedUnixMs int64 // Unix milliseconds; 0 when unobserved
}

// OpenAITokenMetricsStore holds independent atomic counters shared by
// token-source calls. Keep its pointer shared and do not copy the store after
// first use; snapshots load each field separately.
type OpenAITokenMetricsStore struct {
	refreshRequests    atomic.Int64 // entered refresh or lease paths
	refreshSuccess     atomic.Int64 // callback reported refreshed results
	refreshFailure     atomic.Int64 // errors permitting stored-token recovery
	lockAcquireFailure atomic.Int64 // direct lease acquisition errors
	lockContention     atomic.Int64 // entries choosing contention polling
	lockWaitSamples    atomic.Int64 // completed requested polling waits
	// lockWaitTotalMs accumulates requested timer milliseconds after completed waits.
	// Redis I/O and timer scheduling delay remain outside this nominal wait sum.
	lockWaitTotalMs    atomic.Int64
	lockWaitHit        atomic.Int64 // polling returned nonblank token
	lockWaitMiss       atomic.Int64 // polling exhausted all attempts
	lastObservedUnixMs atomic.Int64 // last metric observation Unix milliseconds
}

// OpenAITokenCacheKey returns the OpenAI-prefixed decimal provider-ID cache
// identity. Supply a nonnil credential-owner record; the builder accepts zero
// and other supplied IDs and does not resolve shadow parents.
func OpenAITokenCacheKey(provider *Record) string {
	return "openai:provider:" + strconv.FormatInt(provider.ID, 10)
}

// snapshot loads a value copy of each metric, returning all zeros for a nil
// store. Fields are read independently, so concurrent operations can
// contribute observations from different points during one snapshot.
func (m *OpenAITokenMetricsStore) snapshot() OpenAITokenRuntimeMetrics {
	if m == nil {
		return OpenAITokenRuntimeMetrics{}
	}
	return OpenAITokenRuntimeMetrics{
		RefreshRequests:    m.refreshRequests.Load(),
		RefreshSuccess:     m.refreshSuccess.Load(),
		RefreshFailure:     m.refreshFailure.Load(),
		LockAcquireFailure: m.lockAcquireFailure.Load(),
		LockContention:     m.lockContention.Load(),
		LockWaitSamples:    m.lockWaitSamples.Load(),
		LockWaitTotalMs:    m.lockWaitTotalMs.Load(),
		LockWaitHit:        m.lockWaitHit.Load(),
		LockWaitMiss:       m.lockWaitMiss.Load(),
		LastObservedUnixMs: m.lastObservedUnixMs.Load(),
	}
}

// touchNow records the current Unix-millisecond time for a metric-bearing
// refresh or lock observation. Nil stores are skipped, and later updates
// replace this timestamp independently of the counters sampled by snapshot.
func (m *OpenAITokenMetricsStore) touchNow() {
	if m == nil {
		return
	}
	m.lastObservedUnixMs.Store(time.Now().UnixMilli())
}

// SnapshotRuntimeMetrics returns current metric observations, allocating the
// store when absent and returning zeros for a nil source. Callers initialize
// or synchronize Metrics before concurrent source use because EnsureMetrics
// does not synchronize pointer publication.
func (p *OpenAITokenSource) SnapshotRuntimeMetrics() OpenAITokenRuntimeMetrics {
	if p == nil {
		return OpenAITokenRuntimeMetrics{}
	}
	p.EnsureMetrics()
	return p.Metrics.snapshot()
}

// EnsureMetrics installs an empty metric store when a nonnil source has none.
// Initialize it before concurrent use or synchronize this call externally;
// individual metric updates are atomic, while this pointer assignment is not.
func (p *OpenAITokenSource) EnsureMetrics() {
	if p != nil && p.Metrics == nil {
		p.Metrics = &OpenAITokenMetricsStore{}
	}
}

// GetAccessToken returns an OpenAI OAuth token from cache, refresh results or
// stored credentials. Supply a nonnil context, initialized observers and a
// stable credential-owner record; callers resolve shadows first and initialize
// Metrics before concurrent calls.
//
// A nonblank cache hit returns before expiry validation. On a miss, PAT mode
// skips refresh, and other records request it when expiry is missing or within
// three minutes. Missing refresh credentials permit an unexpired or
// unknown-expiry stored token, but a known expired token returns the
// reauthorization error. Configured refresh errors either propagate or permit
// stored-token recovery; lock-held results poll when policy selects it and
// require a nonnil Cache. Successful non-lock-held results provide a nonnil
// replacement record for the credential owner used to construct the cache key.
//
// With no Refresh callback, the cache lease path can wait for another cache
// fill but performs no exchange. Captured tokens are required to be nonblank.
// An observed newer repository version supplies its token and skips cache
// publication; read failure permits the captured snapshot. Recovery policy
// selects cache lifetime before expiry rules are considered. Otherwise expiry
// selects a five-minute margin, the remaining positive lifetime or one minute
// for elapsed expiry; unknown expiry uses thirty minutes. PAT expiry metadata
// can still affect this cache lifetime, and cache write errors are logged
// while the token is returned.
func (p *OpenAITokenSource) GetAccessToken(ctx context.Context, provider *Record) (string, error) {
	p.EnsureMetrics()
	if provider == nil {
		return "", errors.New("provider is nil")
	}
	if provider.Platform != PlatformOpenAI || provider.Type != ProviderTypeOAuth {
		return "", errors.New("not an openai oauth provider")
	}

	cacheKey := OpenAITokenCacheKey(provider)

	// A cached token is returned before stored expiry inspection. Read failures
	// keep credential-based resolution available, so a cache error alone does not reject this lookup.
	if p.Cache != nil {
		if token, err := p.Cache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			p.Debug("openai_token_cache_hit", "provider_id", provider.ID)
			return token, nil
		} else if err != nil {
			p.Warn("openai_token_cache_get_failed", "provider_id", provider.ID, "error", err)
		}
	}

	p.Debug("openai_token_cache_miss", "provider_id", provider.ID)

	// Request-side admission treats unknown expiry as needing refresh, unlike the
	// background adapter's rate-limit-dependent rule. PAT mode bypasses that expiry admission decision.
	expiresAt := provider.GetCredentialAsTime("expires_at")
	needsRefresh := !provider.IsOpenAIPersonalAccessToken() && (expiresAt == nil || time.Until(*expiresAt) <= openAITokenRefreshSkew)
	if needsRefresh && strings.TrimSpace(provider.GetOpenAIRefreshToken()) == "" {
		if expiresAt != nil && !time.Now().Before(*expiresAt) {
			const reason = "openai access_token expired and refresh_token is missing"
			// A known expired token cannot be renewed from these credentials; request
			// any configured blocking and storage actions before returning the reauthorization-required error.
			p.DisableProviderMissingRefreshToken(provider, reason)
			return "", errors.New(reason)
		}
		needsRefresh = false
	}
	refreshFailed := false

	if needsRefresh && p.Refresh != nil {
		p.Metrics.refreshRequests.Add(1)
		p.Metrics.touchNow()

		result, err := p.Refresh(ctx, provider, openAITokenRefreshSkew)
		if err != nil {
			// Stored-token recovery retains this observation after exchange failure and
			// chooses the policy lifetime below. Returning policy propagates the exchange
			// error immediately before failure metrics or stored-token recovery are recorded.
			if p.Policy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
			p.Warn("openai_token_refresh_failed", "provider_id", provider.ID, "error", err)
			p.Metrics.refreshFailure.Add(1)
			refreshFailed = true
		} else if result.LockHeld {
			// A lock-held result can mean contention or an uncertain retried lease
			// result; selected policy polls for a published token before reusing captured credentials.
			if p.Policy.OnLockHeld == ProviderLockHeldWaitForCache {
				p.Metrics.lockContention.Add(1)
				p.Metrics.touchNow()
				token, waitErr := p.WaitForTokenAfterLockRace(ctx, cacheKey)
				if waitErr != nil {
					return "", waitErr
				}
				if strings.TrimSpace(token) != "" {
					p.Debug("openai_token_cache_hit_after_wait", "provider_id", provider.ID)
					return token, nil
				}
			}
		} else if result.Refreshed {
			p.Metrics.refreshSuccess.Add(1)
			provider = result.Provider
			expiresAt = provider.GetCredentialAsTime("expires_at")
		} else {
			provider = result.Provider
			expiresAt = provider.GetCredentialAsTime("expires_at")
		}
	} else if needsRefresh && p.Cache != nil {
		// This compatibility path coordinates cache access without exchanging a token;
		// false acquisition waits for publication, while lease errors still permit captured credentials.
		p.Metrics.refreshRequests.Add(1)
		p.Metrics.touchNow()
		// The thirty-second lease can expire before deferred release, allowing another
		// holder to acquire the same key while this call still resolves its token.
		locked, lockErr := p.Cache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second)
		if lockErr == nil && locked {
			// Release uses the request context after token resolution and suppresses its
			// error. Redis can already have applied deletion before an error or retry outcome.
			defer func() { _ = p.Cache.ReleaseRefreshLock(ctx, cacheKey) }()
		} else if lockErr != nil {
			p.Metrics.lockAcquireFailure.Add(1)
			p.Metrics.touchNow()
			p.Warn("openai_token_lock_failed", "provider_id", provider.ID, "error", lockErr)
		} else {
			p.Metrics.lockContention.Add(1)
			p.Metrics.touchNow()
			token, waitErr := p.WaitForTokenAfterLockRace(ctx, cacheKey)
			if waitErr != nil {
				return "", waitErr
			}
			if strings.TrimSpace(token) != "" {
				p.Debug("openai_token_cache_hit_after_wait", "provider_id", provider.ID)
				return token, nil
			}
		}
	}

	accessToken := provider.GetCredential("access_token")
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("access_token not found in credentials")
	}

	// A fresh version observation can reveal credentials rotated after this snapshot;
	// choose that observed token without publishing the older map's cache entry.
	if p.Cache != nil {
		latestProvider, isStale := CheckTokenVersion(ctx, provider, p.Repository)
		if isStale && latestProvider != nil {
			p.Debug("openai_token_version_stale_use_latest", "provider_id", provider.ID)
			accessToken = latestProvider.GetOpenAIAccessToken()
			if strings.TrimSpace(accessToken) == "" {
				return "", errors.New("access_token not found after version check")
			}
		} else {
			ttl := 30 * time.Minute
			// A failed exchange can leave expiry elapsed; choose the configured recovery
			// lifetime before ordinary expiry-based reuse so that policy governs subsequent cached requests.
			if refreshFailed {
				if p.Policy.FailureTTL > 0 {
					ttl = p.Policy.FailureTTL
				} else {
					ttl = time.Minute
				}
				p.Debug("openai_token_cache_short_ttl", "provider_id", provider.ID, "reason", "refresh_failed")
			} else if expiresAt != nil {
				until := time.Until(*expiresAt)
				// Keep the refresh margin only when positive lifetime exceeds it. Near-expiry
				// tokens use their remaining time, while elapsed expiry permits one minute of caching.
				switch {
				case until > openAITokenCacheSkew:
					ttl = until - openAITokenCacheSkew
				case until > 0:
					ttl = until
				default:
					ttl = time.Minute
				}
			}
			// Cache storage is optional to returning this resolved token; log failure
			// while leaving request authentication able to use the selected credential string.
			if err := p.Cache.SetAccessToken(ctx, cacheKey, accessToken, ttl); err != nil {
				p.Warn("openai_token_cache_set_failed", "provider_id", provider.ID, "error", err)
			}
		}
	}

	return accessToken, nil
}

// DisableProviderMissingRefreshToken installs an optional runtime block, asks
// SetError to persist reason and then deletes the provider's cached token
// after a successful write. Nil source, record or repository returns before
// any action; callable SetError and observers are required on the active path.
//
// The runtime block precedes persistence and remains installed when SetError
// fails. A background context lets both storage steps proceed independently of
// request cancellation, with I/O timing controlled by their implementations.
// Persistence failure logs and skips cache deletion; cache deletion failure
// logs after the provider error was stored. This method returns no outcome to
// its caller, which still returns the missing-refresh-token error.
func (p *OpenAITokenSource) DisableProviderMissingRefreshToken(provider *Record, reason string) {
	if p == nil || p.Repository == nil || provider == nil {
		return
	}
	// Runtime blocking precedes the durable error write so a failed persistence
	// attempt can still leave the configured scheduling block in place for this provider.
	if p.Block != nil {
		p.Block(provider, time.Time{}, "missing_refresh_token")
	}
	// Persist independently of request cancellation, then clear cached credentials
	// only after SetError succeeds. The implementations, rather than this context, govern I/O completion.
	bgCtx := context.Background()
	if err := p.SetError(bgCtx, provider.ID, reason); err != nil {
		p.Warn("openai_token_provider.set_error_failed",
			"provider_id", provider.ID,
			"error", err,
		)
		return
	}
	if p.Cache != nil {
		if err := p.Cache.DeleteAccessToken(bgCtx, OpenAITokenCacheKey(provider)); err != nil {
			p.Warn("openai_token_provider.cache_delete_failed",
				"provider_id", provider.ID,
				"error", err,
			)
		}
	}
	p.Warn("openai_token_provider.provider_disabled_missing_refresh_token",
		"provider_id", provider.ID,
		"reason", reason,
	)
}

// WaitForTokenAfterLockRace returns a nonblank cache token after a jittered
// wait, or an empty string and nil when five polling attempts are exhausted.
// Supply a nonnil context, cache and initialized Metrics; cancellation
// selected during a timer wait returns ctx.Err.
//
// The base wait doubles from twenty milliseconds to a capped 120 milliseconds
// before jitter is applied, so a jittered wait can exceed that cap. Cache read
// errors are suppressed and polling continues. Metrics and warning thresholds
// sum requested whole-millisecond waits, excluding cache I/O and scheduling
// delays. The cache implementation controls command retries and socket timing,
// so these waits and ctx alone do not establish a wall-clock polling bound.
func (p *OpenAITokenSource) WaitForTokenAfterLockRace(ctx context.Context, cacheKey string) (string, error) {
	wait := openAILockInitialWait
	totalWaitMs := int64(0)
	for i := range openAILockMaxAttempts {
		actualWait := JitterOpenAILockWait(wait)
		timer := time.NewTimer(actualWait)
		// The cancellation branch returns before counting this wait as a completed
		// sample. Stop or drain its timer when available before leaving this polling attempt.
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return "", ctx.Err()
		case <-timer.C:
		}

		// Count requested whole milliseconds; actual scheduling and subsequent Redis
		// latency remain outside the wait metrics and their warning threshold.
		waitMs := max(actualWait.Milliseconds(), 0)
		totalWaitMs += waitMs
		p.Metrics.lockWaitSamples.Add(1)
		p.Metrics.lockWaitTotalMs.Add(waitMs)
		p.Metrics.touchNow()

		// A failed read supplies no usable token for this attempt; keep polling
		// because token publication can still appear on a later cache observation.
		token, err := p.Cache.GetAccessToken(ctx, cacheKey)
		if err == nil && strings.TrimSpace(token) != "" {
			p.Metrics.lockWaitHit.Add(1)
			if totalWaitMs >= openAILockWarnThresholdMs {
				p.Warn("openai_token_lock_wait_high", "wait_ms", totalWaitMs, "attempts", i+1)
			}
			return token, nil
		}

		// Cap the next base before applying jitter, retaining increasing wait spacing
		// while allowing the final jittered duration to exceed the nominal base cap.
		if wait < openAILockMaxWait {
			wait *= 2
			if wait > openAILockMaxWait {
				wait = openAILockMaxWait
			}
		}
	}

	p.Metrics.lockWaitMiss.Add(1)
	if totalWaitMs >= openAILockWarnThresholdMs {
		p.Warn("openai_token_lock_wait_high", "wait_ms", totalWaitMs, "attempts", openAILockMaxAttempts)
	}
	return "", nil
}

// JitterOpenAILockWait applies a random factor approximately twenty percent
// around a positive base. Nonpositive input returns zero, and conversion
// truncates fractional nanoseconds. Callers keep scaled values within
// time.Duration's range, as the polling bases do.
func JitterOpenAILockWait(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	minFactor := 1 - openAILockJitterRatio
	maxFactor := 1 + openAILockJitterRatio
	factor := minFactor + rand.Float64()*(maxFactor-minFactor)
	// Polling timers consume whole nanoseconds; discard the fractional scaled
	// remainder when converting the randomized duration.
	return time.Duration(float64(base) * factor)
}
