package provider

import (
	"context"
	"fmt"
)

// acquireRefreshLock acquires local and distributed locks; ctx bounds waits and Redis errors retain local protection.
func (api *OAuthRefreshAPI) acquireRefreshLock(ctx context.Context, providerID int64, cacheKey string) (func(), bool, error) {
	// Serialize exchanges for this key within the process.
	distributed := false
	localMu := api.getLocalLock(cacheKey)
	if err := localMu.Lock(ctx); err != nil {
		return nil, false, fmt.Errorf("oauth refresh local lock: %w", err)
	}

	if err := api.checkRefreshActive(ctx); err != nil {
		localMu.Unlock()
		return nil, false, err
	}

	// Attempt cross-process locking when a cache is configured.
	if api.tokenCache != nil {
		acquired, lockErr := api.tokenCache.AcquireRefreshLock(ctx, cacheKey, api.lockTTL)
		if lockErr != nil {
			// Continue under the local lock when Redis is unavailable.
			api.options.Warn("oauth_refresh_lock_failed_degraded",
				"provider_id", providerID,
				"cache_key", cacheKey,
				"error", lockErr,
			)
		} else if !acquired {
			// Another worker holds the distributed lock; release the local lock and report contention.
			localMu.Unlock()
			return nil, true, nil
		} else {
			distributed = true
		}
	}

	return func() {
		if distributed {
			api.releaseRefreshLock(ctx, cacheKey)
		}
		localMu.Unlock()
	}, false, nil
}
