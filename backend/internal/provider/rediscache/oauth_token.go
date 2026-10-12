package rediscache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// OAuth token values and refresh leases occupy separate Redis key families,
// both extended by the platform-specific cache identity supplied by callers. A
// lease is a SETNX value with caller-selected expiry, and releasing it deletes
// that key without checking an ownership token. Callers therefore release
// acquired leases while accounting for their expiry and other possible
// holders.
//
// The borrowed go-redis client controls retries and I/O timing. Production
// options retain three eligible retries and disable context-based socket
// deadlines: pool waits and retry delays receive ctx, while reads and writes
// use client timeouts. A lost reply can precede retrying a mutation, so false
// SETNX results and write errors cannot establish whether an earlier attempt
// applied. Adapter invocations and Redis executions are different counts.

// Redis prefixes separate reusable token values from refresh leases while
// preserving the platform-specific identities appended by the coordinator and token sources.
const (
	oauthTokenKeyPrefix       = "oauth:token:"        // shared access token prefix
	oauthRefreshLockKeyPrefix = "oauth:refresh_lock:" // shared refresh lease prefix
)

// OAuthTokenCache adapts a borrowed Redis client to shared OAuth token and
// lease operations.
type OAuthTokenCache struct {
	// rdb is borrowed and remains open while callers use this cache. Its
	// configured retries and timeout policy govern every command issued here.
	rdb *redis.Client
}

// NewOAuthTokenCache retains rdb for subsequent token and lease commands
// without performing a readiness check. Supply a nonnil client and manage its
// lifetime outside this adapter; all platform identities share that client.
func NewOAuthTokenCache(rdb *redis.Client) *OAuthTokenCache {
	return &OAuthTokenCache{rdb: rdb}
}

// GetAccessToken returns the stored token for the caller's cache identity,
// propagating redis.Nil when it is absent. The borrowed client's retry and
// timeout options govern command execution; ctx alone need not bound socket
// I/O.
func (c *OAuthTokenCache) GetAccessToken(ctx context.Context, cacheKey string) (string, error) {
	key := fmt.Sprintf("%s%s", oauthTokenKeyPrefix, cacheKey)
	return c.rdb.Get(ctx, key).Result()
}

// SetAccessToken stores token under the caller's identity with go-redis's
// interpretation of ttl; zero stores an entry without expiry. A returned error
// can follow an applied write, and client retries can repeat that write.
func (c *OAuthTokenCache) SetAccessToken(ctx context.Context, cacheKey string, token string, ttl time.Duration) error {
	key := fmt.Sprintf("%s%s", oauthTokenKeyPrefix, cacheKey)
	return c.rdb.Set(ctx, key, token, ttl).Err()
}

// DeleteAccessToken deletes the caller's token entry and treats an absent
// entry as success. Errors do not prove the key remains, because a command may
// apply before a lost reply and a retry.
func (c *OAuthTokenCache) DeleteAccessToken(ctx context.Context, cacheKey string) error {
	key := fmt.Sprintf("%s%s", oauthTokenKeyPrefix, cacheKey)
	return c.rdb.Del(ctx, key).Err()
}

// AcquireRefreshLock attempts SETNX for the caller's lease identity using ttl,
// returning its observed acquisition result. Zero ttl creates a lease without
// expiry under go-redis. Retrying after an applied command loses its reply can
// report false, so that result alone does not identify another owner.
func (c *OAuthTokenCache) AcquireRefreshLock(ctx context.Context, cacheKey string, ttl time.Duration) (bool, error) {
	key := fmt.Sprintf("%s%s", oauthRefreshLockKeyPrefix, cacheKey)
	return c.rdb.SetNX(ctx, key, 1, ttl).Result()
}

// ReleaseRefreshLock deletes the caller's lease key regardless of its current
// value or owner. Release only according to the coordinator's acquired-lease
// policy; expiry and a replacement holder can occur before this unconditional
// deletion. Redis errors may follow an applied deletion.
func (c *OAuthTokenCache) ReleaseRefreshLock(ctx context.Context, cacheKey string) error {
	key := fmt.Sprintf("%s%s", oauthRefreshLockKeyPrefix, cacheKey)
	return c.rdb.Del(ctx, key).Err()
}
