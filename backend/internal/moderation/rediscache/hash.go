package rediscache

import (
	"context"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/moderation"
)

// Flagged-input hashes share one Redis set across recording, request checks,
// and administrator removal. Set operations trim members but do not validate
// hexadecimal form; the service validates administrator deletion input
// separately. Individual commands follow the application client retry policy,
// so write errors can follow mutations and a remove reply can describe a retry.
//
// Clearing first reads cardinality and then deletes the key, leaving a window
// for concurrent additions or removals. Its returned number is the earlier
// cardinality, rather than an atomic deletion count. The production client also
// leaves ContextTimeoutEnabled false: command contexts still participate in
// pool, dial, and retry waits, while socket I/O uses client timeout policy.

// contentModerationFlaggedHashSetKey names the persistent Redis set shared by
// risk-hash checks, recording, and administrator removal.
const contentModerationFlaggedHashSetKey = "content_moderation:flagged_hashes"

// contentModerationHashCache operates the shared flagged-input set through the
// application-owned Redis client.
type contentModerationHashCache struct {
	rdb *redis.Client // application-owned shared Redis client
}

// NewContentModerationHashCache binds the supplied Redis client for
// flagged-hash set operations. It performs no readiness check and leaves client
// closing to the application; nil clients give the methods their empty or
// successful no-op results.
func NewContentModerationHashCache(rdb *redis.Client) moderation.ContentModerationHashCache {
	return &contentModerationHashCache{rdb: rdb}
}

// RecordFlaggedInputHash adds a trimmed nonblank member to the flagged-input
// set. Nil receivers or clients and blank members return nil; errors report
// command failure while a lost reply can still follow an applied write.
func (c *contentModerationHashCache) RecordFlaggedInputHash(ctx context.Context, inputHash string) error {
	inputHash = strings.TrimSpace(inputHash)
	if c == nil || c.rdb == nil || inputHash == "" {
		return nil
	}
	return c.rdb.SAdd(ctx, contentModerationFlaggedHashSetKey, inputHash).Err()
}

// HasFlaggedInputHash checks the trimmed member and returns the Redis reply or
// its error. Missing clients and blank members return false, nil; Check logs a
// read error and continues its remaining audit path.
func (c *contentModerationHashCache) HasFlaggedInputHash(ctx context.Context, inputHash string) (bool, error) {
	inputHash = strings.TrimSpace(inputHash)
	if c == nil || c.rdb == nil || inputHash == "" {
		return false, nil
	}
	return c.rdb.SIsMember(ctx, contentModerationFlaggedHashSetKey, inputHash).Result()
}

// DeleteFlaggedInputHash removes a trimmed member and reports whether the
// received SREM reply removed one. Blank input and missing clients return
// false, nil. Client retries can return false after an earlier uncertain
// removal, so the boolean describes the observed command reply.
func (c *contentModerationHashCache) DeleteFlaggedInputHash(ctx context.Context, inputHash string) (bool, error) {
	inputHash = strings.TrimSpace(inputHash)
	if c == nil || c.rdb == nil || inputHash == "" {
		return false, nil
	}
	deleted, err := c.rdb.SRem(ctx, contentModerationFlaggedHashSetKey, inputHash).Result()
	if err != nil {
		return false, err
	}
	return deleted > 0, nil
}

// ClearFlaggedInputHashes samples the set cardinality and deletes a nonempty
// set in a separate command. Missing clients and an observed empty set return
// zero, nil. Concurrent membership changes between SCARD and DEL make the
// returned number an earlier count, and either command can return an error.
//
// Production go-redis retry policy can repeat a command after an uncertain
// reply. A failed delete therefore does not establish that the set survived,
// and this adapter does not coordinate additions with the clear operation. The
// caller treats the count as the sampled administrator result.
func (c *contentModerationHashCache) ClearFlaggedInputHashes(ctx context.Context) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, nil
	}
	deleted, err := c.rdb.SCard(ctx, contentModerationFlaggedHashSetKey).Result()
	if err != nil {
		return 0, err
	}
	if deleted == 0 {
		return 0, nil
	}
	// The count and delete are separate operations; additions after the sample
	// can be removed by this delete even though the returned count excludes them.
	if err := c.rdb.Del(ctx, contentModerationFlaggedHashSetKey).Err(); err != nil {
		return 0, err
	}
	return deleted, nil
}

// CountFlaggedInputHashes returns the cardinality observed by one Redis
// command. Missing receivers or clients return zero, nil; GetStatus reports
// zero and logs a diagnostic when an available client returns an error.
func (c *contentModerationHashCache) CountFlaggedInputHashes(ctx context.Context) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, nil
	}
	return c.rdb.SCard(ctx, contentModerationFlaggedHashSetKey).Result()
}
