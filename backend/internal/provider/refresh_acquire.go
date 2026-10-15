package provider

import (
	"context"
	"fmt"
)

// refresh_acquire.go acquires local and distributed refresh locks and returns
// their ordered release callback.
// The refresh key first serializes exchanges inside this process, then
// optionally attempts cross-process exclusion through the cache. A failed cache
// acquisition leaves local protection in place, while distributed contention
// returns an unperformed result before any provider reread.
//
// After the local wait succeeds, the stop barrier is checked again because
// cancellation can lag behind shutdown state. Every skipped or failed
// acquisition returns its local token, and a successful caller owns a release
// function that attempts distributed cleanup before releasing local capacity.
// The distributed lease has its configured finite lifetime, so
// conditional persistence remains necessary if another process proceeds after
// lease expiry.

// acquireRefreshLock returns a release callback after obtaining local and
// available distributed protection for cacheKey. Callers supply a nonnil
// context; local wait errors pass through, and the stopped activity barrier is
// rechecked before a cache attempt.
//
// A cache error is logged and retains the local lock, while a reported false
// result returns held=true with no callback and releases local capacity. Redis
// retries can apply an earlier SETNX before a final false or error result. Success returns a
// caller-owned callback invoked exactly once after all protected work. It
// passes a separate deadline context to distributed release when acquired, then
// returns the local token after that cache call, including an error. Redis socket
// I/O follows its configured client timeouts rather than this context deadline
// when ContextTimeoutEnabled is false. ProviderID supplies diagnostics,
// and the cache key needs to match other paths for the same provider.
func (api *OAuthRefreshAPI) acquireRefreshLock(ctx context.Context, providerID int64, cacheKey string) (func(), bool, error) {
	// Hold this key locally before attempting distributed exclusion so concurrent
	// callers in the process cannot exchange the same selected credentials
	// together.
	distributed := false
	localMu := api.getLocalLock(cacheKey)
	if err := localMu.Lock(ctx); err != nil {
		return nil, false, fmt.Errorf("oauth refresh local lock: %w", err)
	}

	// A stopped owner may not have delivered cancellation to this waiter yet.
	// Return its newly obtained token before allowing it to reach distributed
	// acquisition.
	if err := api.checkRefreshActive(ctx); err != nil {
		localMu.Unlock()
		return nil, false, err
	}

	// The cache lease coordinates other processes; errors degrade to the already
	// held local lock, and conditional identity writes still protect stale
	// results.
	if api.tokenCache != nil {
		acquired, lockErr := api.tokenCache.AcquireRefreshLock(ctx, cacheKey, api.lockTTL)
		if lockErr != nil {
			// Cache failure leaves this local owner intact so
			// identity-checked refreshing can continue. An earlier
			// retried SETNX may have applied, so distributed lease
			// ownership remains unconfirmed by this error.
			api.options.Warn("oauth_refresh_lock_failed_degraded",
				"provider_id", providerID,
				"cache_key", cacheKey,
				"error", lockErr,
			)
		} else if !acquired {
			// Treat the returned false result as contention,
			// including a retry after this call installed the lease
			// and lost its reply. Return local capacity before
			// reporting the skip so queued callers can proceed.
			localMu.Unlock()
			return nil, true, nil
		} else {
			distributed = true
		}
	}

	// Keep local exclusion through distributed cleanup, then let queued callers
	// proceed after the cache call returns. A cleanup error is logged and the
	// caller's single release invocation still returns this local token.
	return func() {
		if distributed {
			api.releaseRefreshLock(ctx, cacheKey)
		}
		localMu.Unlock()
	}, false, nil
}
