package httpx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/querycache"
)

type SnapshotCacheEntry struct {
	ETag      string
	Payload   any
	ExpiresAt time.Time
}

type SnapshotCache struct{ cache *querycache.Cache }

func NewSnapshotCache(ttl time.Duration) *SnapshotCache {
	return &SnapshotCache{cache: querycache.NewCache(ttl)}
}

func snapshotEntry(e querycache.Entry) SnapshotCacheEntry {
	return SnapshotCacheEntry{ETag: BuildETagFromAny(e.Payload), Payload: e.Payload, ExpiresAt: e.ExpiresAt}
}

func (c *SnapshotCache) Get(key string) (SnapshotCacheEntry, bool) {
	if c == nil {
		return SnapshotCacheEntry{}, false
	}
	e, ok := c.cache.Get(key)
	if !ok {
		return SnapshotCacheEntry{}, false
	}
	return snapshotEntry(e), true
}

func (c *SnapshotCache) Set(key string, payload any) SnapshotCacheEntry {
	if c == nil {
		return SnapshotCacheEntry{}
	}
	return snapshotEntry(c.cache.Set(key, payload))
}

func (c *SnapshotCache) GetOrLoad(key string, load func() (any, error)) (SnapshotCacheEntry, bool, error) {
	if load == nil {
		return SnapshotCacheEntry{}, false, nil
	}
	if c == nil {
		_, e := load()
		return SnapshotCacheEntry{}, false, e
	}
	entry, hit, e := c.cache.GetOrLoad(key, load)
	if e != nil {
		return SnapshotCacheEntry{}, hit, e
	}
	return snapshotEntry(entry), hit, nil
}

func BuildETagFromAny(payload any) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "\"" + hex.EncodeToString(sum[:]) + "\""
}

// GetOrLoadContext builds a snapshot and ETag from a shared load whose waiters cancel independently.
func (c *SnapshotCache) GetOrLoadContext(ctx context.Context, key string, load func(context.Context) (any, error)) (SnapshotCacheEntry, bool, error) {
	if load == nil {
		return SnapshotCacheEntry{}, false, nil
	}
	if c == nil {
		_, err := load(ctx)
		return SnapshotCacheEntry{}, false, err
	}
	entry, hit, err := c.cache.GetOrLoadContext(ctx, key, load)
	if err != nil {
		return SnapshotCacheEntry{}, hit, err
	}
	return snapshotEntry(entry), hit, nil
}

func ParseBoolQueryWithDefault(raw string, def bool) bool {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return def
	}
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

// IfNoneMatchMatched matches a comma-separated condition list, including wildcard and weak tags.
func IfNoneMatchMatched(ifNoneMatch, etag string) bool {
	if etag == "" || ifNoneMatch == "" {
		return false
	}
	for token := range strings.SplitSeq(ifNoneMatch, ",") {
		candidate := strings.TrimSpace(token)
		if candidate == "*" {
			return true
		}
		if candidate == etag {
			return true
		}
		if strings.HasPrefix(candidate, "W/") && strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
