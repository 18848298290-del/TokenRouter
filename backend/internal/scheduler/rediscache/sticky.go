package rediscache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Provider stickiness is scoped to a group and session hash, while session
// ownership is scoped to a user, source and hash. Separate Redis prefixes let
// bindings be replaced and ownership claims be acquired with SETNX. Writes
// apply SET or SETNX expiry rules, while refresh uses whole-second EXPIRE, so
// renewal has different duration precision from initial storage.

// Sticky key prefixes separate provider routing bindings from first-writer
// session owner claims so their independent lifetimes and replacement rules
// can be applied.
const (
	stickySessionPrefix      = "sticky_session:"       // group-scoped provider binding namespace
	stickySessionOwnerPrefix = "sticky_session_owner:" // user-scoped session owner namespace
)

// StickyCache stores group provider bindings and user session-owner claims in
// Redis.
type StickyCache struct {
	// rdb is the caller's shared Redis client; each binding write and owner
	// operation supplies the lifetime applied to its Redis key.
	rdb *redis.Client // shared sticky storage client
}

// NewStickyCache constructs binding and owner-claim storage over the supplied
// Redis client. The caller retains the client and supplies the TTL for
// binding writes and owner refreshes.
func NewStickyCache(rdb *redis.Client) *StickyCache { return &StickyCache{rdb: rdb} }

// GetSessionProviderID reads the binding for this group and session hash.
// Missing keys return redis.Nil, allowing the sticky resolver to distinguish
// absence from a stored provider ID.
func (c *StickyCache) GetSessionProviderID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Get(ctx, key).Int64()
}

// SetSessionProviderID replaces this group's session binding with providerID
// using go-redis SET expiry rules; zero TTL creates a persistent binding. The
// supplied hash becomes part of the key directly, and hashing and validation
// belong to callers.
func (c *StickyCache) SetSessionProviderID(ctx context.Context, groupID int64, sessionHash string, providerID int64, ttl time.Duration) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Set(ctx, key, providerID, ttl).Err()
}

// RefreshSessionTTL applies whole-second EXPIRE to an existing provider
// binding. Positive subsecond TTLs become one second, longer fractional
// seconds are discarded, and nonpositive TTLs delete the binding.
//
// A missing key returns no error, so success alone cannot establish whether
// the binding remains present. This refresh has coarser duration precision
// than a fractional-second SetSessionProviderID write.
func (c *StickyCache) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Expire(ctx, key, ttl).Err()
}

// DeleteSessionProviderID removes this group's binding for the supplied
// session hash. Repeated deletion succeeds when the key is absent, allowing
// callers to discard invalid stickiness during selection.
func (c *StickyCache) DeleteSessionProviderID(ctx context.Context, groupID int64, sessionHash string) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Del(ctx, key).Err()
}

// SetSessionOwnerGroupID claims this user/source/session key for groupID
// using go-redis SETNX expiry rules; zero TTL creates a persistent claim. It
// returns false when a claim already exists, leaving that claim's group ID
// intact.
func (c *StickyCache) SetSessionOwnerGroupID(ctx context.Context, userID int64, source, sessionHash string, groupID int64, ttl time.Duration) (bool, error) {
	key := buildSessionOwnerKey(userID, source, sessionHash)
	return c.rdb.SetNX(ctx, key, groupID, ttl).Result()
}

// GetSessionOwnerGroupID reads the group stored for this user's source and
// session hash. Missing keys return redis.Nil so callers can decide whether a
// new claim is needed.
func (c *StickyCache) GetSessionOwnerGroupID(ctx context.Context, userID int64, source, sessionHash string) (int64, error) {
	key := buildSessionOwnerKey(userID, source, sessionHash)
	return c.rdb.Get(ctx, key).Int64()
}

// RefreshSessionOwnerTTL applies whole-second EXPIRE to the existing owner
// claim. Positive subsecond TTLs become one second, longer fractional
// seconds are discarded, and nonpositive TTLs delete the claim.
//
// A positive TTL retains the stored group ID. Missing keys report success
// with no new claim, so callers need a separate owner read to establish
// whether ownership is still stored.
func (c *StickyCache) RefreshSessionOwnerTTL(ctx context.Context, userID int64, source, sessionHash string, ttl time.Duration) error {
	key := buildSessionOwnerKey(userID, source, sessionHash)
	return c.rdb.Expire(ctx, key, ttl).Err()
}

// buildSessionKey joins group identity and the supplied hash into the
// provider-binding namespace.
func buildSessionKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", stickySessionPrefix, groupID, sessionHash)
}

// buildSessionOwnerKey joins user identity, source and supplied hash into the
// ownership namespace.
func buildSessionOwnerKey(userID int64, source, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s:%s", stickySessionOwnerPrefix, userID, source, sessionHash)
}
