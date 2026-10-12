package rediscache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// Refresh credentials are stored by digest, while separate user and family sets
// provide candidates for bulk revocation. SessionService stores a credential before
// adding those indexes and logs indexing failures, so the sets can omit a stored
// credential. Consuming or deleting one record also leaves its indexed digest
// until the relevant set is cleaned up or expires.
//
// Consumption delegates the winning removal to Redis DEL after service-level
// validation. Bulk revocation reads a set and then pipelines deletions, so it covers
// that observed membership and can miss concurrent additions. Index insertion
// and expiry refresh also use ordinary pipelines. Their errors can follow partial
// work; these adapters report failures while callers decide how to handle them.

// These prefixes distinguish credential JSON from user and session-family
// revocation sets, whose members are the same refresh-token digests.
const (
	refreshTokenKeyPrefix   = "refresh_token:"
	userRefreshTokensPrefix = "user_refresh_tokens:"
	tokenFamilyPrefix       = "token_family:"
)

// refreshTokenCache implements digest-keyed credential storage and Redis revocation indexes.
type refreshTokenCache struct {
	// rdb is the caller-owned client shared by record and index operations.
	// Close it after session services finish creating, consuming, and revoking tokens.
	rdb *redis.Client
}

// refreshTokenKey builds the Redis credential-record key from the supplied token digest.
func refreshTokenKey(tokenHash string) string {
	return refreshTokenKeyPrefix + tokenHash
}

// userRefreshTokensKey builds the Redis revocation-index key for one user identifier.
func userRefreshTokensKey(userID int64) string {
	return fmt.Sprintf("%s%d", userRefreshTokensPrefix, userID)
}

// tokenFamilyKey builds the Redis revocation-index key for one session family.
func tokenFamilyKey(familyID string) string {
	return tokenFamilyPrefix + familyID
}

// NewRefreshTokenCache binds credential records and revocation indexes to the
// supplied Redis client. Its caller owns that retained client and keeps it available
// through token creation, rotation, and revocation.
func NewRefreshTokenCache(rdb *redis.Client) identity.RefreshTokenCache {
	return &refreshTokenCache{rdb: rdb}
}

// StoreRefreshToken encodes token metadata and writes it under the supplied
// digest. Positive TTLs expire the JSON; zero and negative durations store it
// without expiry except redis.KeepTTL, which retains a previous expiry. User and
// family indexes are separate operations that SessionService creates after the
// credential record is stored.
func (c *refreshTokenCache) StoreRefreshToken(ctx context.Context, tokenHash string, data *identity.RefreshTokenData, ttl time.Duration) error {
	key := refreshTokenKey(tokenHash)
	val, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal refresh token data: %w", err)
	}
	return c.rdb.Set(ctx, key, val, ttl).Err()
}

// GetRefreshToken decodes a new token-state value from the digest's Redis record.
// Missing keys become ErrRefreshTokenNotFound; other storage and JSON errors remain
// errors for SessionService to handle before claiming consumption.
func (c *refreshTokenCache) GetRefreshToken(ctx context.Context, tokenHash string) (*identity.RefreshTokenData, error) {
	key := refreshTokenKey(tokenHash)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			// Normalize Redis absence to the identity sentinel so session refresh can
			// reject a missing credential without depending on the storage client error.
			return nil, identity.ErrRefreshTokenNotFound
		}
		return nil, err
	}
	var data identity.RefreshTokenData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return nil, fmt.Errorf("unmarshal refresh token data: %w", err)
	}
	return &data, nil
}

// DeleteRefreshToken removes the digest's credential record through Redis DEL.
// An absent record succeeds, and user or family index membership remains until
// separate bulk cleanup or index expiry removes it.
func (c *refreshTokenCache) DeleteRefreshToken(ctx context.Context, tokenHash string) error {
	key := refreshTokenKey(tokenHash)
	return c.rdb.Del(ctx, key).Err()
}

// ConsumeRefreshToken reports whether Redis DEL removed this digest's credential
// record. SessionService claims it after validation; a false result or any error
// provides no permission to issue a successor token.
func (c *refreshTokenCache) ConsumeRefreshToken(ctx context.Context, tokenHash string) (bool, error) {
	deleted, err := c.rdb.Del(ctx, refreshTokenKey(tokenHash)).Result()
	return deleted == 1, err
}

// DeleteUserRefreshTokens removes credential records named by the user's current
// index and then removes that index. An empty index succeeds, while an index read
// failure stops revocation before deletion commands are submitted.
//
// The index is read before a regular Redis pipeline sends one DEL per record and
// for the index itself. Concurrent additions can escape this snapshot, and pipeline
// failures can leave partial cleanup. A digest added before the final index DEL
// can retain its credential while losing its membership with the deleted set.
// Family indexes retain their members until family cleanup or expiry, so an
// indexed digest may already lack its record.
func (c *refreshTokenCache) DeleteUserRefreshTokens(ctx context.Context, userID int64) error {
	tokenHashes, err := c.GetUserTokenHashes(ctx, userID)
	if err != nil && err != redis.Nil {
		return fmt.Errorf("get user token hashes: %w", err)
	}

	if len(tokenHashes) == 0 {
		return nil
	}

	keys := make([]string, 0, len(tokenHashes)+1)
	for _, hash := range tokenHashes {
		keys = append(keys, refreshTokenKey(hash))
	}
	keys = append(keys, userRefreshTokensKey(userID))

	// Delete the observed records before their index entry. This regular pipeline
	// batches commands; a failure can leave some observed credentials already removed.
	pipe := c.rdb.Pipeline()
	for _, key := range keys {
		pipe.Del(ctx, key)
	}
	_, err = pipe.Exec(ctx)
	return err
}

// DeleteTokenFamily removes credential records named by the family's current
// index and then removes that index. An empty family succeeds, while an index read
// failure prevents submitting the deletion commands.
//
// The snapshot and subsequent regular Redis pipeline are separate operations.
// Concurrent token creation can add records after the snapshot, and a pipeline
// error may follow partial deletion. A digest added before the final index DEL
// can retain its credential while losing its membership with the deleted set.
// User indexes retain their digest members until user cleanup or expiry;
// membership does not prove a credential record remains available for renewal.
func (c *refreshTokenCache) DeleteTokenFamily(ctx context.Context, familyID string) error {
	tokenHashes, err := c.GetFamilyTokenHashes(ctx, familyID)
	if err != nil && err != redis.Nil {
		return fmt.Errorf("get family token hashes: %w", err)
	}

	if len(tokenHashes) == 0 {
		return nil
	}

	keys := make([]string, 0, len(tokenHashes)+1)
	for _, hash := range tokenHashes {
		keys = append(keys, refreshTokenKey(hash))
	}
	keys = append(keys, tokenFamilyKey(familyID))

	// Delete the observed records before their index entry. This regular pipeline
	// batches commands; a failure can leave some observed credentials already removed.
	pipe := c.rdb.Pipeline()
	for _, key := range keys {
		pipe.Del(ctx, key)
	}
	_, err = pipe.Exec(ctx)
	return err
}

// AddToUserTokenSet adds a digest to the user's revocation index and applies ttl
// to the whole set. SessionService calls it after saving the credential record;
// refresh rotation can leave previously consumed digests in this index.
//
// A regular pipeline queues SADD before EXPIRE. The new expiry replaces the
// previous index lifetime rather than comparing individual credential expiries,
// and an execution failure can leave membership with an earlier expiry or none.
// Positive durations expire the index using go-redis's whole-second conversion;
// nonpositive durations delete it. The returned error exposes incomplete indexing.
func (c *refreshTokenCache) AddToUserTokenSet(ctx context.Context, userID int64, tokenHash string, ttl time.Duration) error {
	key := userRefreshTokensKey(userID)
	// Keep membership insertion before expiry so a newly created set receives a
	// lifetime; failure between commands can leave the added digest with incomplete expiry.
	pipe := c.rdb.Pipeline()
	pipe.SAdd(ctx, key, tokenHash)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// AddToFamilyTokenSet adds a digest to a session family's revocation index and
// applies ttl to that set. Membership supplies bulk deletion candidates, including
// digests whose credential records have already been consumed during rotation.
//
// SADD precedes EXPIRE in a regular Redis pipeline. Each call applies its supplied
// lifetime to the whole index, so it does not compute the longest remaining
// credential lifetime. Pipeline failure can leave the insertion or expiry partly
// applied; SessionService handles that indexing error separately from record storage.
// Positive durations expire the index using go-redis's whole-second conversion;
// nonpositive durations delete it.
func (c *refreshTokenCache) AddToFamilyTokenSet(ctx context.Context, familyID string, tokenHash string, ttl time.Duration) error {
	key := tokenFamilyKey(familyID)
	// Keep membership insertion before expiry so a newly created set receives a
	// lifetime; failure between commands can leave the added digest with incomplete expiry.
	pipe := c.rdb.Pipeline()
	pipe.SAdd(ctx, key, tokenHash)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// GetUserTokenHashes returns the digest members currently indexed for the user.
// Redis returns an empty collection for an absent set; members can outlive consumed
// credential records and are candidates for user-wide deletion.
func (c *refreshTokenCache) GetUserTokenHashes(ctx context.Context, userID int64) ([]string, error) {
	key := userRefreshTokensKey(userID)
	return c.rdb.SMembers(ctx, key).Result()
}

// GetFamilyTokenHashes returns the digest members currently indexed for the
// session family. An absent set yields an empty collection; membership can remain
// after the corresponding credential record has been consumed or deleted.
func (c *refreshTokenCache) GetFamilyTokenHashes(ctx context.Context, familyID string) ([]string, error) {
	key := tokenFamilyKey(familyID)
	return c.rdb.SMembers(ctx, key).Result()
}

// IsTokenInFamily reports whether the family index currently contains the digest.
// An absent index returns false; true describes set membership and does not establish
// that the separately stored refresh credential is still valid.
func (c *refreshTokenCache) IsTokenInFamily(ctx context.Context, familyID string, tokenHash string) (bool, error) {
	key := tokenFamilyKey(familyID)
	return c.rdb.SIsMember(ctx, key, tokenHash).Result()
}
