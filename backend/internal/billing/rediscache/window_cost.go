package rediscache

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// window_cost.go caches provider cost observations and maps batch reads to
// provider identities.
// Provider selection can reuse a short-lived standard-cost observation by
// provider ID before reading usage storage. Single reads distinguish a miss
// from a usable zero amount, while batch reads omit missing or unsupported
// values so the guard can refill those providers.
//
// MGET results have positional identity rather than their own provider IDs.
// The batch builds keys in input order and maps each returned value back to
// that same index, keeping cost observations attached to the requested
// provider. Numeric strings are decoded independently; malformed entries are
// omitted, while float64 test values are accepted directly. Each stored
// observation expires after thirty seconds.

const (
	// Provider cost keys identify the standard-cost observation by provider ID,
	// and the thirty-second lease limits how long the guard can reuse it.
	windowCostKeyPrefix = "window_cost:provider:"
	windowCostCacheTTL  = 30 * time.Second
)

// WindowCostCache stores provider standard-cost observations through a
// caller-owned Redis client.
type WindowCostCache struct {
	// rdb is retained from the caller for cost reads and writes. The owning
	// application closes it after guard consumers stop using cached observations.
	rdb *redis.Client // shared provider cost Redis client
}

// NewWindowCostCache binds a caller-owned Redis client for provider cost
// observations. Construction performs no read or warming; callers keep the
// client usable and close it after cost-guard consumers stop.
func NewWindowCostCache(rdb *redis.Client) *WindowCostCache { return &WindowCostCache{rdb: rdb} }

// windowCostKey returns the standard-cost namespace plus the supplied decimal
// provider ID.
func windowCostKey(id int64) string { return fmt.Sprintf("%s%d", windowCostKeyPrefix, id) }

// GetWindowCost returns a decoded provider observation with hit=true,
// including zero. redis.Nil becomes a miss with no error; other Redis or
// decoding failures return hit=false with the error so the guard can choose
// storage recovery.
func (c *WindowCostCache) GetWindowCost(ctx context.Context, providerID int64) (float64, bool, error) {
	key := windowCostKey(providerID)
	val, err := c.rdb.Get(ctx, key).Float64()
	// A missing provider key supplies no current-window observation. Return a
	// miss so the guard can query storage while retaining zero as a valid hit.
	if err == redis.Nil {
		return 0, false, nil // Cache miss.
	}
	if err != nil {
		return 0, false, err
	}
	return val, true, nil
}

// SetWindowCost replaces the provider's standard-cost observation with a
// thirty-second expiry. The caller supplies a finite cost associated with the
// current window; the cache key contains provider ID, so window identity is
// governed by the guard and expiry.
func (c *WindowCostCache) SetWindowCost(ctx context.Context, providerID int64, cost float64) error {
	key := windowCostKey(providerID)
	return c.rdb.Set(ctx, key, cost, windowCostCacheTTL).Err()
}

// GetWindowCostBatch returns usable observations indexed by requested provider
// ID, with an allocated empty map for no IDs. Redis command errors fail the
// batch; missing, malformed or unsupported entries are omitted individually.
//
// Keys and MGET results share input ordering, so each accepted result is
// stored under the provider at that index. String values use ParseFloat, and
// float64 values are retained for compatible test clients. No finite-value
// validation is performed here; callers supply and consume trusted
// current-window observations.
func (c *WindowCostCache) GetWindowCostBatch(ctx context.Context, providerIDs []int64) (map[int64]float64, error) {
	if len(providerIDs) == 0 {
		return make(map[int64]float64), nil
	}

	// MGET values identify their keys by position; retain provider input order
	// here so each later numeric observation is assigned to the matching provider
	// ID.
	keys := make([]string, len(providerIDs))
	for i, providerID := range providerIDs {
		keys[i] = windowCostKey(providerID)
	}

	vals, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}

	results := make(map[int64]float64, len(providerIDs))
	for i, val := range vals {
		if val == nil {
			continue // Cache miss.
		}
		// Decode each observation independently, keeping missing or malformed
		// entries absent so the guard can query those providers while reusing
		// successful batch values.
		switch v := val.(type) {
		case string:
			if cost, err := strconv.ParseFloat(v, 64); err == nil {
				results[providerIDs[i]] = cost
			}
		case float64:
			results[providerIDs[i]] = v
		}
	}

	return results, nil
}
