package rediscache

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// Proxy probe snapshots are serialized as one JSON value per proxy ID and read
// in batches for administrator listings. MGet returns a sequence corresponding
// to requested keys, so decoding uses each response position to recover the
// matching proxy identity rather than depending on the snapshot payload to carry
// that ID.
//
// Missing, unexpected, and malformed individual values are skipped
// independently, preserving other usable observations in the batch. Duplicate
// requested IDs collapse into one result-map key, with later successfully
// decoded values replacing earlier ones. A Redis command error returns the empty
// map and its error to the listing caller. Stored observations have no Redis
// expiration; administrative probes replace them when new results are available.

// proxyLatencyKeyPrefix separates per-proxy probe snapshots from other Redis
// data when IDs are formatted into cache keys.
const proxyLatencyKeyPrefix = "proxy:latency:"

// proxyLatencyCache implements per-proxy observation storage and batched reads
// over an application Redis client.
type proxyLatencyCache struct {
	// rdb is retained for cache commands and remains open while the adapter is
	// used. The application owns closing this shared client after its consumers
	// stop.
	rdb *redis.Client // borrowed application Redis client
}

// proxyLatencyKey formats a persisted proxy identity into its Redis observation
// key.
func proxyLatencyKey(proxyID int64) string {
	return fmt.Sprintf("%s%d", proxyLatencyKeyPrefix, proxyID)
}

// NewProxyLatencyCache constructs an observation adapter using the supplied
// Redis client. The adapter borrows the client for later commands, and the
// application keeps it open while administrative probe and listing consumers
// run.
func NewProxyLatencyCache(rdb *redis.Client) egress.ProxyLatencyCache {
	return &proxyLatencyCache{rdb: rdb}
}

// GetProxyLatencies batches requested IDs and returns successfully decoded
// observations under their corresponding identities. Empty input returns an
// empty map, and Redis command failure returns that map with its error.
//
// Each raw value is matched to the ID at the same request position. Missing,
// unexpected, and malformed values are skipped independently, so one unusable
// cache entry does not discard other decoded results. Duplicate IDs share a map
// key, and later successfully decoded results replace earlier values for that
// identity.
func (c *proxyLatencyCache) GetProxyLatencies(ctx context.Context, proxyIDs []int64) (map[int64]*egress.ProxyLatencyInfo, error) {
	results := make(map[int64]*egress.ProxyLatencyInfo)
	if len(proxyIDs) == 0 {
		return results, nil
	}

	keys := make([]string, 0, len(proxyIDs))
	for _, id := range proxyIDs {
		keys = append(keys, proxyLatencyKey(id))
	}

	values, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return results, err
	}

	for i, raw := range values {
		if raw == nil {
			continue
		}
		// MGet values retain request ordering. Skip unusable individual payloads
		// while keeping each successfully decoded observation attached to its requested ID.
		var payload []byte
		switch v := raw.(type) {
		case string:
			payload = []byte(v)
		case []byte:
			payload = v
		default:
			continue
		}
		var info egress.ProxyLatencyInfo
		if err := json.Unmarshal(payload, &info); err != nil {
			continue
		}
		results[proxyIDs[i]] = &info
	}

	return results, nil
}

// SetProxyLatency serializes one probe observation under proxyID with no Redis
// expiration. Nil input does nothing; serialization and Redis errors return to
// the caller, which decides whether cache failure affects its administrative
// result.
func (c *proxyLatencyCache) SetProxyLatency(ctx context.Context, proxyID int64, info *egress.ProxyLatencyInfo) error {
	if info == nil {
		return nil
	}
	payload, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, proxyLatencyKey(proxyID), payload, 0).Err()
}
