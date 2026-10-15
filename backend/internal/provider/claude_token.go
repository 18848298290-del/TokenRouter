package provider

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// claude_token.go selects Claude request credentials from cache, refresh
// results, and current repository versions.
// Claude request credentials prefer a populated access-token cache over the
// expiry stored in the request record. OAuth refresh policy decides whether
// failure returns an error or the record token, while service-account records
// delegate directly to Vertex exchange.
//
// Contended refresh uses a fixed sleep before rereading the cache, including
// cancellation that arrives during that sleep. A newer repository token
// version wins over the request snapshot and suppresses cache publication from
// that stale snapshot. Other cache writes use expiry-based lifetimes, with a
// separate short lifetime after tolerated refresh failure. These version reads
// and cache writes are separate operations; callers keep mutable records
// stable during lookup and let the refresh coordinator own persistent
// rotation.

// ClaudeTokenRefreshSkew, ClaudeTokenCacheSkew, and ClaudeLockWaitTime govern
// request refresh admission, cache lifetime margins, and one peer-publication
// wait.
const (
	ClaudeTokenRefreshSkew = 3 * time.Minute
	ClaudeTokenCacheSkew   = 5 * time.Minute
	ClaudeLockWaitTime     = 200 * time.Millisecond
)

// AccessTokenCache supplies token storage and refresh admission to credential
// sources.
type AccessTokenCache interface {
	// GetAccessToken reads a token for the supplied cache key. Credential sources
	// treat errors and whitespace-only tokens as misses and continue through their
	// platform refresh or stored-credential path.
	GetAccessToken(context.Context, string) (string, error)
	// SetAccessToken publishes the supplied token with its selected lifetime.
	// Request sources tolerate storage failure and still return the token;
	// production cache implementations apply the provided duration to expiration.
	SetAccessToken(context.Context, string, string, time.Duration) error
	// DeleteAccessToken removes the token associated with the selected credential
	// key.
	DeleteAccessToken(context.Context, string) error
	// AcquireRefreshLock reports whether refresh admission was obtained for this
	// key. A true result makes the caller responsible for ReleaseRefreshLock;
	// false lets platform policy choose a cache reread or stored token.
	// Retried uncertain writes can report false after an earlier acquisition,
	// so the result alone need not identify another holder.
	AcquireRefreshLock(context.Context, string, time.Duration) (bool, error)
	// ReleaseRefreshLock relinquishes admission previously acquired for this key.
	// Token sources release after their guarded lookup path ends and tolerate
	// release errors, leaving expiration to the underlying lock implementation.
	ReleaseRefreshLock(context.Context, string) error
}

// ClaudeTokenOptions binds shared token dependencies and request refresh
// policy.
type ClaudeTokenOptions struct {
	// Debug and Warn are optional diagnostics for token selection and cache or
	// refresh failures. Their nil-safe wrappers let credential lookup proceed
	// when either logging callback is absent.
	// Debug: token lookup diagnostics; Warn: cache and refresh warnings
	Debug, Warn func(string, ...any)
	// Cache optionally supplies tokens and peer refresh admission. Nil skips
	// caching and leaves configured refresh or record credentials available.
	Cache AccessTokenCache
	// Repository optionally supplies a fresh credential version before cache
	// publication. A failed read leaves the request record usable through
	// CheckTokenVersion.
	Repository RefreshRepository
	// Policy selects retained credentials or errors after refresh failure and
	// chooses a peer wait; its positive FailureTTL overrides the short failure
	// default.
	Policy ProviderRefreshPolicy
	// Refresh optionally invokes coordinated rotation. Success supplies a nonnil
	// result; replacement records remain stable while this lookup consumes their
	// credentials.
	Refresh func(context.Context, *Record, time.Duration) (*OAuthRefreshResult, error)
	// Vertex is required for service-account records and owns their token
	// exchange.
	// That branch returns directly before OAuth cache or refresh processing.
	Vertex func(context.Context, *Record) (string, error)
}

// ClaudeTokenSource obtains Claude request credentials through shared refresh
// and cache dependencies.
type ClaudeTokenSource struct {
	// Options retains borrowed service references and copied callback values.
	// Configure it before concurrent use and keep its policy stable during
	// lookups.
	Options ClaudeTokenOptions
}

// GetAccessToken delegates credential selection to GetClaudeAccessToken with
// captured options. Supply a stable record during lookup; its cache, refresh
// policy, Vertex dispatch, and persistence-version rules are defined by that
// operation.
func (s *ClaudeTokenSource) GetAccessToken(ctx context.Context, value *Record) (string, error) {
	return GetClaudeAccessToken(ctx, value, s.Options)
}

// ClaudeTokenCacheKey formats a nonnil record ID into the Claude token
// namespace. All Claude records with the same row ID use the same key
// regardless of credential contents.
func ClaudeTokenCacheKey(value *Record) string {
	return "claude:provider:" + strconv.FormatInt(value.ID, 10)
}

// GetClaudeAccessToken returns cached, refreshed, or stored credentials
// according to policy. Nil, other platforms, and unsupported types fail;
// service-account records require Vertex, and successful refresh callbacks
// return a nonnil result with a nonnil replacement record when LockHeld is
// false.
//
// A contended lock may yield a peer cache token after an unconditional fixed
// wait. Tolerated refresh failure retains the stored token and selects a short
// cache lifetime. A newer repository version replaces that token and skips
// publication, while failed version reads permit the request snapshot. Callers
// synchronize mutable record access and retain the shared cache throughout
// lookup.
func GetClaudeAccessToken(ctx context.Context, provider *Record, options ClaudeTokenOptions) (string, error) {
	if provider == nil {
		return "", errors.New("provider is nil")
	}
	if provider.Platform != PlatformAnthropic || (provider.Type != ProviderTypeOAuth && provider.Type != ProviderTypeServiceAccount) {
		return "", errors.New("not an anthropic oauth or service account")
	}
	if provider.Type == ProviderTypeServiceAccount {
		return options.Vertex(ctx, provider)
	}

	cacheKey := ClaudeTokenCacheKey(provider)

	// Cache errors or blank values permit stored-credential lookup. A populated
	// entry returns before expiry inspection so a peer refresh can supply the
	// token.
	if options.Cache != nil {
		if token, err := options.Cache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			options.debug("claude_token_cache_hit", "provider_id", provider.ID)
			return token, nil
		} else if err != nil {
			options.warn("claude_token_cache_get_failed", "provider_id", provider.ID, "error", err)
		}
	}

	options.debug("claude_token_cache_miss", "provider_id", provider.ID)

	// Missing expiry also requests refresh here, unlike background eligibility.
	// The injected policy chooses whether an exchange error retains the request
	// token.
	expiresAt := provider.GetCredentialAsTime("expires_at")
	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= ClaudeTokenRefreshSkew
	refreshFailed := false

	if needsRefresh && options.Refresh != nil {
		result, err := options.Refresh(ctx, provider, ClaudeTokenRefreshSkew)
		if err != nil {
			if options.Policy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
			options.warn("claude_token_refresh_failed", "provider_id", provider.ID, "error", err)
			refreshFailed = true
		} else if result.LockHeld {
			if options.Policy.OnLockHeld == ProviderLockHeldWaitForCache && options.Cache != nil {
				// Give a peer a publication interval before one
				// reread. This sleep ignores cancellation, and
				// an unavailable peer token leaves the
				// stored-token path open.
				time.Sleep(ClaudeLockWaitTime)
				if token, cacheErr := options.Cache.GetAccessToken(ctx, cacheKey); cacheErr == nil && strings.TrimSpace(token) != "" {
					options.debug("claude_token_cache_hit_after_wait", "provider_id", provider.ID)
					return token, nil
				}
			}
		} else {
			provider = result.Provider
			expiresAt = provider.GetCredentialAsTime("expires_at")
		}
	} else if needsRefresh && options.Cache != nil {
		// With no exchange callback, take optional cache admission around token
		// lookup and publication. A granted lock is released even after an early
		// error.
		locked, lockErr := options.Cache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second)
		if lockErr == nil && locked {
			defer func() { _ = options.Cache.ReleaseRefreshLock(ctx, cacheKey) }()
		} else if lockErr != nil {
			options.warn("claude_token_lock_failed", "provider_id", provider.ID, "error", lockErr)
		} else {
			// A peer may publish during this fixed wait. Cancellation does not wake
			// the sleep, and a missed reread still permits the credential token below.
			time.Sleep(ClaudeLockWaitTime)
			if token, err := options.Cache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
				options.debug("claude_token_cache_hit_after_wait", "provider_id", provider.ID)
				return token, nil
			}
		}
	}

	accessToken := provider.GetCredential("access_token")
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("access_token not found in credentials")
	}

	// Observe repository rotation before publication so an already newer version
	// supplies its token; the separate cache write can still race with later
	// rotations.
	if options.Cache != nil {
		latestProvider, isStale := CheckTokenVersion(ctx, provider, options.Repository, options.Debug)
		if isStale && latestProvider != nil {
			options.debug("claude_token_version_stale_use_latest", "provider_id", provider.ID)
			accessToken = latestProvider.GetCredential("access_token")
			if strings.TrimSpace(accessToken) == "" {
				return "", errors.New("access_token not found after version check")
			}
		} else {
			// Unknown expiry uses a bounded default; failed refresh shortens that
			// lifetime so tolerated stale credentials receive another refresh
			// opportunity sooner.
			ttl := 30 * time.Minute
			if refreshFailed {
				if options.Policy.FailureTTL > 0 {
					ttl = options.Policy.FailureTTL
				} else {
					ttl = time.Minute
				}
				options.debug("claude_token_cache_short_ttl", "provider_id", provider.ID, "reason", "refresh_failed")
			} else if expiresAt != nil {
				until := time.Until(*expiresAt)
				switch {
				case until > ClaudeTokenCacheSkew:
					ttl = until - ClaudeTokenCacheSkew
				case until > 0:
					ttl = until
				default:
					ttl = time.Minute
				}
			}
			// Cache publication is best effort: its failure does not discard the
			// usable credential token already selected for this upstream request.
			if err := options.Cache.SetAccessToken(ctx, cacheKey, accessToken, ttl); err != nil {
				options.warn("claude_token_cache_set_failed", "provider_id", provider.ID, "error", err)
			}
		}
	}

	return accessToken, nil
}

// debug forwards token diagnostics when its optional callback is configured.
// Missing Debug discards these observations, so token selection and refresh
// policy continue without requiring a logger.
func (o ClaudeTokenOptions) debug(message string, args ...any) {
	if o.Debug != nil {
		o.Debug(message, args...)
	}
}

// warn forwards token-cache and refresh failures to the optional warning
// callback. Missing Warn discards the diagnostic while the caller retains its
// selected token or returned error.
func (o ClaudeTokenOptions) warn(message string, args ...any) {
	if o.Warn != nil {
		o.Warn(message, args...)
	}
}
