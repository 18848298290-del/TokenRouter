package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const VertexLockWaitTime = 200 * time.Millisecond

// VertexTokenOptions supplies request identity, token caching, and the exchange callback.
type VertexTokenOptions struct {
	ProviderID int64
	CacheKey   string
	Cache      AccessTokenCache
	Exchange   func(context.Context) (string, time.Duration, error)
	Warn       func(string, ...any)
}

func VertexServiceAccountCacheKey(providerID int64, email, keyID string, hasKey bool) string {
	fingerprint := ""
	if hasKey {
		sum := sha256.Sum256([]byte(email + "\x00" + keyID))
		fingerprint = hex.EncodeToString(sum[:8])
	}
	if fingerprint == "" {
		fingerprint = fmt.Sprintf("provider:%d", providerID)
	}
	return "vertex:service_account:" + fingerprint
}

// GetVertexServiceAccountAccessToken reads or exchanges a token, using a refresh lock when available.
// Lock contention waits briefly and respects cancellation; cache failures still allow an exchange.
// @project-doc docs/interfaces/gemini_upstream.md#vertex_service_account_execution
func GetVertexServiceAccountAccessToken(ctx context.Context, options VertexTokenOptions) (string, error) {
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
			defer func() { _ = options.Cache.ReleaseRefreshLock(ctx, options.CacheKey) }()
		} else if lockErr != nil {
			if options.Warn != nil {
				options.Warn("vertex_service_account_token_lock_failed", "provider_id", options.ProviderID, "error", lockErr)
			}
		} else {
			timer := time.NewTimer(VertexLockWaitTime)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
			// Cancellation may be ready with the timer; check it before reading or exchanging again.
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
		_ = options.Cache.SetAccessToken(ctx, options.CacheKey, accessToken, ttl)
	}
	return accessToken, nil
}
