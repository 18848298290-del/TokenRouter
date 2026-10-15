package rediscache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// runtime.go implements Redis ownership leases and string storage for Ops
// background jobs.
// Ops lease keys store an instance owner so a delayed release leaves a lease
// acquired by a different owner after expiry intact. Reusing the same owner
// string does not distinguish successive acquisitions. Claim uses Redis SETNX
// with the caller-selected lifetime, and jobs choose whether to skip or attempt
// another lock when a command fails. A successful claim has a finite lifetime
// when its supplied TTL is positive.
//
// Release executes the owner comparison and deletion in one script, preventing
// another Redis command from acquiring the key between those two operations.
// String reads and replacements use the exact keys selected by alert, report,
// aggregation, and cleanup callers. The wrapper borrows the application client,
// whose connection pool remains owned by app.

// releaseOwner compares the stored lease owner and removes the matching key
// within one Redis script. An expired lease acquired by another instance
// therefore survives a delayed release from the previous owner.
var releaseOwner = redis.NewScript(`if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) end return 0`)

// Runtime uses the injected Redis client for Ops leases and task strings.
type Runtime struct {
	// client is the application-owned Redis connection pool. Runtime borrows it
	// for commands, and application shutdown remains responsible for closing the
	// client.
	client *redis.Client
}

// NewRuntime wraps a caller-owned Redis client for Ops task coordination and
// string state. A nil client returns a nil interface, allowing each background
// job to apply its configured missing-cache policy.
func NewRuntime(c *redis.Client) ops.RuntimeCache {
	if c == nil {
		return nil
	}
	return &Runtime{c}
}

// Claim stores owner under the supplied key when it is absent, using ttl as the
// lease lifetime. It reports contention separately from command errors and
// performs no lease renewal.
func (r *Runtime) Claim(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	return r.client.SetNX(ctx, key, owner, ttl).Result()
}

// Release runs the owner-checking deletion script for the supplied lease key. A
// missing key or different owner leaves the key unchanged and returns nil;
// Redis execution errors propagate.
func (r *Runtime) Release(ctx context.Context, key, owner string) error {
	return releaseOwner.Run(ctx, r.client, []string{key}, owner).Err()
}

// Get returns the string stored under key using the supplied context. Redis
// reports a missing key as redis.Nil, so the Ops caller decides whether to use
// defaults or report failure.
func (r *Runtime) Get(ctx context.Context, key string) (string, error) {
	return r.client.Get(ctx, key).Result()
}

// Put replaces the string stored under key using ttl for expiration. Zero ttl
// keeps the value indefinitely under Redis SET behavior, and the supplied
// context governs command execution.
func (r *Runtime) Put(ctx context.Context, key, value string, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}
