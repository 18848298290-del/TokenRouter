package querycache

import (
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// cache.go stores expiring query snapshots and coordinates reads with
// replacement and shared loads.
// A query snapshot has a fixed lifetime from insertion and can be read
// independently of the caller that supplied its payload. Cache reads sample
// time before checking storage, then remove expired entries under the write
// lock after checking that a newer insertion has not replaced the expired
// value. Supported payload references are copied on insertion and return.
//
// Same-key synchronous loads share singleflight results, while context-aware
// loads use a separate waiter registry described in context_load.go. The two
// entry points can load independently. Both paths store successful results
// using Set, which fixes expiration at publication; callers choose stable keys
// and payloads accepted by Clone, whose unsupported references remain shared.

// Entry carries a query payload and the timestamp used by Cache expiration
// checks.
type Entry struct {
	// Payload follows Clone's copying restrictions. Cached values may retain
	// unexported fields or unsupported references, which callers treat as immutable.
	Payload   any
	ExpiresAt time.Time // snapshot expiration timestamp
}

// Cache stores expiring query snapshots and coalesces loads within each loading
// entry point. Construct it with NewCache before writes, and supply payloads
// that satisfy Clone's copying restrictions.
type Cache struct {
	// mu protects items while Get and Set read, expire, or publish entries.
	// Payload copying occurs outside read locks, and callers keep unsupported
	// shared references immutable while these entry values are in use.
	mu    sync.RWMutex     // snapshot storage access lock
	ttl   time.Duration    // inserted snapshot lifetime
	items map[string]Entry // stored snapshots by query key
	// sf merges synchronous GetOrLoad calls by key. Context-aware calls use loads
	// separately, so the two loading APIs can run concurrently for the same key.
	sf singleflight.Group
	// loadMu protects loads and each flight's waiter count and publication state.
	// A final departing waiter removes its unfinished flight before cancelling it,
	// allowing a later caller to register a replacement for the same key.
	loadMu sync.Mutex              // context-load registry access lock
	loads  map[string]*contextLoad // active context loads by key
}

// LoadResult transports a synchronous load result and its cache-hit observation
// through singleflight.
type LoadResult struct {
	Entry Entry // cached or newly loaded snapshot
	Hit   bool  // cache hit observed inside flight
}

// NewCache constructs empty snapshot storage using ttl as each insertion's
// lifetime. Nonpositive ttl selects thirty seconds, and expiration is checked
// by Get rather than a background eviction task.
func NewCache(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Cache{
		ttl:   ttl,
		items: make(map[string]Entry),
	}
}

// Get returns a cloned entry when key exists and is unexpired at its sampled
// time. Nil caches and empty keys miss, and unsupported references in the clone
// remain shared under Clone's rules.
//
// An expired read upgrades from a read lock to a write lock. Set may replace
// the entry during that interval, so deletion checks the currently stored
// expiration again before removing it. This call still reports a miss after
// observing its expired entry, leaving a replacement available to subsequent
// reads.
func (c *Cache) Get(key string) (Entry, bool) {
	if c == nil || key == "" {
		return Entry{}, false
	}
	now := time.Now()

	c.mu.RLock()
	entry, ok := c.items[key]
	c.mu.RUnlock()
	if !ok {
		return Entry{}, false
	}
	if now.After(entry.ExpiresAt) {
		c.mu.Lock()
		// Set can publish a newer value between the read and write locks. Remove
		// the current entry only if the same sampled time also finds it expired.
		if current, exists := c.items[key]; exists && now.After(current.ExpiresAt) {
			delete(c.items, key)
		}
		c.mu.Unlock()
		return Entry{}, false
	}
	return Clone(entry), true
}

// Set clones payload, assigns expiration from the insertion time, and returns a
// cloned entry. A nil cache returns an empty Entry; an empty key returns the
// prepared entry without storing it. Construct nonnil caches with NewCache
// before publishing values.
func (c *Cache) Set(key string, payload any) Entry {
	if c == nil {
		return Entry{}
	}
	entry := Entry{
		Payload:   Clone(payload),
		ExpiresAt: time.Now().Add(c.ttl),
	}
	if key == "" {
		return Clone(entry)
	}
	c.mu.Lock()
	// Publish the cloned payload and its expiration under one write lock so Get
	// observes both fields from the same insertion when reading this query key.
	c.items[key] = entry
	c.mu.Unlock()
	return Clone(entry)
}

// GetOrLoad returns a cached or synchronously loaded snapshot, with the bool
// identifying a cache observation. A nil loader returns an empty success result
// before checking the cache; loader errors remain uncached.
//
// Same-key calls share singleflight work and receive clones of its result. The
// flight checks the cache again before loading, so a value published during
// initial lookup can produce a hit. Nil caches or empty keys run load directly;
// the nil-cache Set path returns an empty entry. Loaders and Clone execute in
// the caller's synchronous path, and their panics can propagate.
func (c *Cache) GetOrLoad(key string, load func() (any, error)) (Entry, bool, error) {
	if load == nil {
		return Entry{}, false, nil
	}
	if entry, ok := c.Get(key); ok {
		return entry, true, nil
	}
	if c == nil || key == "" {
		payload, err := load()
		if err != nil {
			return Entry{}, false, err
		}
		return c.Set(key, payload), false, nil
	}

	// A peer can finish loading after the initial miss. Recheck inside singleflight
	// before querying so this shared flight can return that newly published snapshot.
	value, err, _ := c.sf.Do(key, func() (any, error) {
		if entry, ok := c.Get(key); ok {
			return LoadResult{Entry: entry, Hit: true}, nil
		}
		payload, err := load()
		if err != nil {
			return nil, err
		}
		return LoadResult{Entry: c.Set(key, payload), Hit: false}, nil
	})
	if err != nil {
		return Entry{}, false, err
	}
	result, ok := value.(LoadResult)
	if !ok {
		// An unexpected flight result cannot supply snapshot metadata. Return an
		// empty success value rather than asserting a payload type for the caller.
		return Entry{}, false, nil
	}
	return Clone(result.Entry), result.Hit, nil
}
