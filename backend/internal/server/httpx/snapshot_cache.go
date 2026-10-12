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

// Conditional HTTP responses need an ETag derived from the payload being served.
// Cache operations wrap querycache's returned snapshot and compute its JSON
// hash afterward, so each result carries its payload, tag, and storage expiry.
// Load sharing, cancellation, and snapshot cloning belong to querycache.

// SnapshotCacheEntry pairs a query snapshot with its JSON tag and expiration.
type SnapshotCacheEntry struct {
	// ETag and Payload come from the same returned query entry. Keep the payload
	// and its JSON encoding stable when serving this tag; marshal failures produce
	// an empty tag that conditional matching treats as unavailable.
	ETag string // quoted payload-JSON SHA-256 hash
	// Payload uses querycache.Clone's supported copying rules. Inputs need to be
	// acyclic; map keys, unexported fields, and unsupported references can remain
	// shared, and named pointer types can change. Treat shared positions as immutable.
	Payload   any       // returned query snapshot value
	ExpiresAt time.Time // query entry expiry time
}

// SnapshotCache adds HTTP entity tags to snapshots managed by querycache.
type SnapshotCache struct {
	// cache receives storage and an entry lifetime from NewSnapshotCache.
	// A zero field delegates to nil-cache paths that return no stored hits;
	// method comments describe their distinct write and load results.
	cache *querycache.Cache
}

// NewSnapshotCache constructs query storage with the supplied entry lifetime.
// Nonpositive ttl selects querycache's thirty-second default. Use this constructor
// for initialized storage; payloads follow Clone's restrictions on cycles,
// unsupported references, and named pointer representations.
func NewSnapshotCache(ttl time.Duration) *SnapshotCache {
	return &SnapshotCache{cache: querycache.NewCache(ttl)}
}

// snapshotEntry adds a JSON-derived tag to the supplied query result.
// Payload and expiration pass through from that result, whose copying is owned
// by querycache. Unmarshalable payloads carry an empty tag for callers to handle.
func snapshotEntry(e querycache.Entry) SnapshotCacheEntry {
	return SnapshotCacheEntry{ETag: BuildETagFromAny(e.Payload), Payload: e.Payload, ExpiresAt: e.ExpiresAt}
}

// Get returns the stored snapshot and its derived tag when key is unexpired.
// Nil receivers, empty keys, missing entries, and expired entries report false.
// Successful payload copies follow querycache.Clone's restrictions; keep their
// JSON representation stable while using the returned tag.
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

// Set stores a cloned payload in initialized storage and returns its tag and
// expiry. Empty keys return a tagged, expiring entry without retaining it;
// other keys replace the stored entry under querycache's cloning rules.
//
// Nil receivers return an empty entry. A nonnil facade with a nil backing
// cache returns nil payload, zero expiry, and the tag for JSON null because
// querycache.Set produces an empty query entry on that path.
func (c *SnapshotCache) Set(key string, payload any) SnapshotCacheEntry {
	if c == nil {
		return SnapshotCacheEntry{}
	}
	return snapshotEntry(c.cache.Set(key, payload))
}

// GetOrLoad returns a cached or loaded snapshot with its JSON-derived tag.
// The bool reports a cache hit; callers sharing a new load receive false.
// Initialized storage coalesces loads and leaves loader errors uncached.
//
// Nil loaders produce an empty result. Nil receivers invoke load, discard its
// payload, and return its error. A nonnil facade with a nil backing cache also
// discards a successful payload, but tags its empty query entry as JSON null
// with zero expiry. Loader failures return an empty entry and their error.
func (c *SnapshotCache) GetOrLoad(key string, load func() (any, error)) (SnapshotCacheEntry, bool, error) {
	if load == nil {
		return SnapshotCacheEntry{}, false, nil
	}
	if c == nil {
		// Preserve the loader's effect and error when no facade receiver exists.
		// Its successful payload is discarded because this path produces no snapshot entry.
		_, e := load()
		return SnapshotCacheEntry{}, false, e
	}
	entry, hit, e := c.cache.GetOrLoad(key, load)
	if e != nil {
		return SnapshotCacheEntry{}, hit, e
	}
	return snapshotEntry(entry), hit, nil
}

// BuildETagFromAny hashes this call's JSON encoding into a quoted hexadecimal SHA-256 tag.
// Encoding failures return an empty string. Callers keep the payload and any
// custom marshaler output stable while pairing the returned tag with a response.
func BuildETagFromAny(payload any) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		// JSON cannot supply a representation to hash. An empty tag lets the
		// conditional helper report a miss while the owner handles payload encoding.
		return ""
	}
	sum := sha256.Sum256(raw)
	return "\"" + hex.EncodeToString(sum[:]) + "\""
}

// GetOrLoadContext returns a context-aware snapshot and tag through querycache.
// Supply a nonnil ctx and a loader that honors its supplied context. Initialized
// storage checks cancellation before hits and lets nonempty-key waiters cancel
// independently; its final departing waiter cancels unfinished shared work.
//
// Empty keys invoke the loader directly and return an uncached entry. A nonnil
// facade with a nil backing cache checks cancellation before and after loading,
// then retains a cloned payload and its tag with zero expiry on success.
// Nil loaders return an empty result. Nil receivers invoke load directly with
// ctx, discard its payload, and return its error.
func (c *SnapshotCache) GetOrLoadContext(ctx context.Context, key string, load func(context.Context) (any, error)) (SnapshotCacheEntry, bool, error) {
	if load == nil {
		return SnapshotCacheEntry{}, false, nil
	}
	if c == nil {
		// This direct path preserves the callback and its supplied context without
		// shared-load coordination, then discards the payload because no entry is returned.
		_, err := load(ctx)
		return SnapshotCacheEntry{}, false, err
	}
	entry, hit, err := c.cache.GetOrLoadContext(ctx, key, load)
	if err != nil {
		return SnapshotCacheEntry{}, hit, err
	}
	return snapshotEntry(entry), hit, nil
}

// ParseBoolQueryWithDefault interprets trimmed, case-insensitive boolean query spellings.
// The spellings 1, true, yes, and on enable it; 0, false, no, and off disable it.
// Empty or unrecognized text returns the supplied default for the caller's query policy.
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

// IfNoneMatchMatched compares a condition list with the supplied nonempty strong tag.
// Empty inputs return false. Trimmed tokens match the wildcard, exact tag, or
// the tag after removing an uppercase W/ request marker; the supplied etag
// itself is not normalized by this helper.
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
		// Incoming weak markers can qualify the generated strong tag for cache
		// validation. Ignore that request marker while retaining its quoted payload identity.
		if strings.HasPrefix(candidate, "W/") && strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
