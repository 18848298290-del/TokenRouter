package rediscache

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	windowCostKeyPrefix = "window_cost:provider:"
	windowCostCacheTTL  = 30 * time.Second
)

type WindowCostCache struct{ rdb *redis.Client }

func NewWindowCostCache(rdb *redis.Client) *WindowCostCache { return &WindowCostCache{rdb: rdb} }
func windowCostKey(id int64) string                         { return fmt.Sprintf("%s%d", windowCostKeyPrefix, id) }

// Provider five-hour cost-window cache.

// GetWindowCost returns the cached cost, a hit flag, and any Redis or decoding error.
func (c *WindowCostCache) GetWindowCost(ctx context.Context, providerID int64) (float64, bool, error) {
	key := windowCostKey(providerID)
	val, err := c.rdb.Get(ctx, key).Float64()
	if err == redis.Nil {
		return 0, false, nil // Cache miss.
	}
	if err != nil {
		return 0, false, err
	}
	return val, true, nil
}

// SetWindowCost caches a provider's window cost for 30 seconds.
func (c *WindowCostCache) SetWindowCost(ctx context.Context, providerID int64, cost float64) error {
	key := windowCostKey(providerID)
	return c.rdb.Set(ctx, key, cost, windowCostCacheTTL).Err()
}

// GetWindowCostBatch returns cached costs, omitting missing or unparseable values.
func (c *WindowCostCache) GetWindowCostBatch(ctx context.Context, providerIDs []int64) (map[int64]float64, error) {
	if len(providerIDs) == 0 {
		return make(map[int64]float64), nil
	}

	// Keep key order aligned with providerIDs for MGET results.
	keys := make([]string, len(providerIDs))
	for i, providerID := range providerIDs {
		keys[i] = windowCostKey(providerID)
	}

	// Fetch the requested keys in one Redis call.
	vals, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}

	results := make(map[int64]float64, len(providerIDs))
	for i, val := range vals {
		if val == nil {
			continue // Cache miss.
		}
		// Accept Redis string values and numeric test-client values.
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
