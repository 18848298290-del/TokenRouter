package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Vertex tokens can be shared by service-account identity across provider
// rows. Email and key ID are delimited before hashing, while unavailable
// parsed identity falls back to the provider ID for both token and
// refresh-lock namespaces.
//
// A cache hit avoids exchange. Lock contention waits briefly for a peer result
// and checks cancellation before rereading or exchanging; lock errors permit
// direct exchange, and an acquired lock is released after either success or
// failure. After the wait, another miss can still exchange without ownership,
// so this helper does not promise one exchange under all contention. Returned
// tokens remain usable when cache publication fails, and the supplied
// exchanger determines their TTL.

// VertexLockWaitTime gives a peer exchange time to publish before a lock
// contender rereads cached credentials.
const VertexLockWaitTime = 200 * time.Millisecond

// VertexTokenOptions binds service-account identity, cache coordination, and
// token exchange callbacks.
type VertexTokenOptions struct {
	ProviderID int64 // provider identifier for diagnostics
	// CacheKey identifies both token lookup and refresh-lock ownership. Keep it
	// stable throughout the call so release uses the same selected namespace.
	CacheKey string
	// Cache optionally stores tokens and coordinates refresh locks. A nil cache
	// sends misses directly to the injected exchange.
	Cache AccessTokenCache
	// Exchange is required on misses and returns token plus TTL. The caller
	// configures its authentication and cancellation behavior for this request.
	Exchange func(context.Context) (string, time.Duration, error)
	// Warn optionally reports lock errors. Missing logging leaves the same direct
	// exchange behavior available after that coordination failure.
	Warn func(string, ...any)
}

// VertexServiceAccountCacheKey names token and lock entries using email and
// keyID when hasKey is true. Those fields are joined with a NUL separator
// before their digest is shortened.
//
// The same parsed identity shares a namespace across provider IDs; hasKey
// false selects the decimal provider ID. Only the first eight digest bytes
// appear in the key, so it is an identity hint rather than proof of unique or
// valid credentials. Callers obtain the parsed identity before invoking cache
// coordination.
func VertexServiceAccountCacheKey(providerID int64, email, keyID string, hasKey bool) string {
	fingerprint := ""
	// Delimit email and key ID so their concatenated identities remain distinct
	// before shortening the digest into the shared token namespace.
	if hasKey {
		sum := sha256.Sum256([]byte(email + "\x00" + keyID))
		// Eight digest bytes keep the cache identity suffix compact.
		fingerprint = hex.EncodeToString(sum[:8])
	}
	// Without parsed key identity, isolate token entries by the local provider
	// identifier so the unavailable-account path has a stable lookup key.
	if fingerprint == "" {
		fingerprint = fmt.Sprintf("provider:%d", providerID)
	}
	return "vertex:service_account:" + fingerprint
}

// GetVertexServiceAccountAccessToken returns a cached token or the injected
// exchange result. Configure Exchange for cache misses; a nil cache exchanges
// directly, and a successful exchange supplies both the token and its storage
// TTL.
//
// An acquired thirty-second refresh lock is released on return. Rejected lock
// admission waits two hundred milliseconds for a peer token, with cancellation
// checked both during the wait and after the timer wins. Cache read and lock
// failures permit exchange, and a miss after contention can exchange without
// owning the lock. Cache-write errors are discarded so successful token
// acquisition remains available.
// Pass a nonnil context for cancellation-aware contention waits.
// @project-doc docs/interfaces/gemini_upstream.md#vertex_service_account_execution
func GetVertexServiceAccountAccessToken(ctx context.Context, options VertexTokenOptions) (string, error) {
	// A cache failure supplies no reusable token. Continue to lock or exchange
	// handling so cache availability is separate from service-account credential
	// acquisition.
	if options.Cache != nil {
		if token, err := options.Cache.GetAccessToken(ctx, options.CacheKey); err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
	}
	locked := false
	if options.Cache != nil {
		var lockErr error
		locked, lockErr = options.Cache.AcquireRefreshLock(ctx, options.CacheKey, 30*time.Second)
		if lockErr == nil && locked {
			// Release the owned lock after exchange or failure. Release errors are
			// discarded here, leaving its lease expiry to bound failed cleanup.
			defer func() { _ = options.Cache.ReleaseRefreshLock(ctx, options.CacheKey) }()
		} else if lockErr != nil {
			// Lock failure cannot coordinate a peer, so keep direct exchange available
			// and optionally record that degraded cache outcome.
			if options.Warn != nil {
				options.Warn("vertex_service_account_token_lock_failed", "provider_id", options.ProviderID, "error", lockErr)
			}
		} else {
			// Admission was not confirmed. Wait briefly for cached publication, with
			// caller cancellation providing an exit before another read or exchange
			// can run. A false cache result alone need not identify another holder.
			timer := time.NewTimer(VertexLockWaitTime)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
			// Timer readiness can race cancellation. Recheck the caller before cache
			// reread so that cancelled lock contenders cannot start another exchange.
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if token, err := options.Cache.GetAccessToken(ctx, options.CacheKey); err == nil && strings.TrimSpace(token) != "" {
				return token, nil
			}
		}
	}

	accessToken, ttl, err := options.Exchange(ctx)
	if err != nil {
		return "", err
	}
	if options.Cache != nil {
		// Token exchange already succeeded. Treat cache publication as best effort
		// so a storage error does not replace the acquired credential result.
		_ = options.Cache.SetAccessToken(ctx, options.CacheKey, accessToken, ttl)
	}
	return accessToken, nil
}
