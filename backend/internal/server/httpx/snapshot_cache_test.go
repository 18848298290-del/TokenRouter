package httpx

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSnapshotCache_SetAndGet stores a map and reads it back under the same key.
// The stored result must have payload data and a nonempty tag, and the lookup
// must succeed with the same tag for that fixture.
func TestSnapshotCache_SetAndGet(t *testing.T) {
	c := NewSnapshotCache(5 * time.Second)

	entry := c.Set("key1", map[string]string{"hello": "world"})
	require.NotEmpty(t, entry.ETag)
	require.NotNil(t, entry.Payload)

	got, ok := c.Get("key1")
	require.True(t, ok)
	require.Equal(t, entry.ETag, got.ETag)
}

// TestSnapshotCache_Expiration stores a value with a one-millisecond lifetime.
// After the longer fixture delay, lookup must report a miss, protecting the
// observable expiration behavior of its querycache-backed entry.
func TestSnapshotCache_Expiration(t *testing.T) {
	c := NewSnapshotCache(1 * time.Millisecond)

	c.Set("key1", "value")
	// Wait beyond the configured one-millisecond expiry before observing the miss.
	time.Sleep(5 * time.Millisecond)

	_, ok := c.Get("key1")
	require.False(t, ok, "expired entry should not be returned")
}

// TestSnapshotCache_GetEmptyKey asks initialized storage for an empty key.
// Lookup must report a miss, protecting the special key result consumed by
// handlers that cannot use an empty cache identity for stored data.
func TestSnapshotCache_GetEmptyKey(t *testing.T) {
	c := NewSnapshotCache(5 * time.Second)
	_, ok := c.Get("")
	require.False(t, ok)
}

// TestSnapshotCache_GetMiss reads a key that has never been stored.
// Lookup must report a miss, leaving consumers free to load that snapshot
// through their normal cache-miss path for the request.
func TestSnapshotCache_GetMiss(t *testing.T) {
	c := NewSnapshotCache(5 * time.Second)
	_, ok := c.Get("nonexistent")
	require.False(t, ok)
}

// TestSnapshotCache_NilReceiver
// invokes lookup and storage through a nil facade pointer.
// Lookup must report false, and Set must return an entry with no tag,
// establishing those specific nil-receiver results without constructing query storage.
func TestSnapshotCache_NilReceiver(t *testing.T) {
	var c *SnapshotCache
	_, ok := c.Get("key")
	require.False(t, ok)

	entry := c.Set("key", "value")
	require.Empty(t, entry.ETag)
}

// TestSnapshotCache_SetEmptyKey writes a payload using an empty key.
// Set must still return a nonempty derived tag, while the subsequent empty-key
// lookup reports a miss for that uncached result.
func TestSnapshotCache_SetEmptyKey(t *testing.T) {
	c := NewSnapshotCache(5 * time.Second)

	// An empty key produces an entry without storing it.
	entry := c.Set("", "value")
	require.NotEmpty(t, entry.ETag)

	_, ok := c.Get("")
	require.False(t, ok)
}

// TestSnapshotCache_DefaultTTL constructs storage with zero and negative lifetimes.
// Each returned expiry must fall between the before and after observations
// shifted by thirty seconds, establishing the default lifetime for those inputs.
func TestSnapshotCache_DefaultTTL(t *testing.T) {
	// Inspect the returned entry's expiry to check the default TTL.
	for _, ttl := range []time.Duration{0, -time.Second} {
		c := NewSnapshotCache(ttl)
		before := time.Now()
		entry := c.Set("default", "value")
		after := time.Now()
		require.False(t, entry.ExpiresAt.Before(before.Add(30*time.Second)))
		require.False(t, entry.ExpiresAt.After(after.Add(30*time.Second)))
	}
}

// TestSnapshotCache_ETagDeterministic stores the same ordinary map under two keys.
// Both returned tags must match, establishing equal JSON-derived identities for
// that map value independently of the cache key used to store it.
func TestSnapshotCache_ETagDeterministic(t *testing.T) {
	c := NewSnapshotCache(5 * time.Second)
	payload := map[string]int{"a": 1, "b": 2}

	entry1 := c.Set("k1", payload)
	entry2 := c.Set("k2", payload)
	require.Equal(t, entry1.ETag, entry2.ETag, "same payload should produce same ETag")
}

// TestSnapshotCache_ETagFormat stores a string and examines its returned tag.
// The tag must have content between opening and closing quote bytes, protecting
// the quoted representation asserted by this fixture.
func TestSnapshotCache_ETagFormat(t *testing.T) {
	c := NewSnapshotCache(5 * time.Second)
	entry := c.Set("k", "test")
	// HTTP entity tags include surrounding quotes.
	require.True(t, len(entry.ETag) > 2)
	require.Equal(t, byte('"'), entry.ETag[0])
	require.Equal(t, byte('"'), entry.ETag[len(entry.ETag)-1])
}

// TestBuildETagFromAny_UnmarshalablePayload passes a channel that JSON cannot encode.
// Tag creation must return an empty string, protecting the encoding-failure
// result that conditional-response callers treat as an unavailable tag.
func TestBuildETagFromAny_UnmarshalablePayload(t *testing.T) {
	// Channels cannot be marshaled to JSON.
	etag := BuildETagFromAny(make(chan int))
	require.Empty(t, etag)
}

// TestSnapshotCache_GetOrLoad_MissThenHit loads a missing key and then reads it again.
// The first result must report a miss and a tag, while the second reports a
// hit with the same tag and leaves the loader count at one.
func TestSnapshotCache_GetOrLoad_MissThenHit(t *testing.T) {
	c := NewSnapshotCache(5 * time.Second)
	var loads atomic.Int32

	// Both callbacks share the invocation count, so a later cache miss would
	// increase the same total. Assertions detect a second callback execution.
	entry, hit, err := c.GetOrLoad("key1", func() (any, error) {
		loads.Add(1)
		return map[string]string{"hello": "world"}, nil
	})
	require.NoError(t, err)
	require.False(t, hit)
	require.NotEmpty(t, entry.ETag)
	require.Equal(t, int32(1), loads.Load())

	entry2, hit, err := c.GetOrLoad("key1", func() (any, error) {
		loads.Add(1)
		return map[string]string{"unexpected": "value"}, nil
	})
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, entry.ETag, entry2.ETag)
	require.Equal(t, int32(1), loads.Load())
}

// TestSnapshotCache_GetOrLoad_ConcurrentSingleflight
// launches same-key callers behind a shared start gate.
// Every caller must finish without error and the loader count must be one;
// cache hits as well as joined loads can satisfy that observed count.
func TestSnapshotCache_GetOrLoad_ConcurrentSingleflight(t *testing.T) {
	c := NewSnapshotCache(5 * time.Second)
	var loads atomic.Int32
	// Hold callers behind one gate until the launch loop finishes. Opening it
	// admits the same-key operations to exercise shared loading or subsequent cache hits.
	start := make(chan struct{})
	const callers = 8
	// Allocate one result slot per caller so publication cannot block before
	// the parent has waited for all goroutines and starts inspecting their errors.
	errCh := make(chan error, callers)

	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		// Completion follows result publication for each caller, letting the parent
		// close the result stream after Wait without racing a remaining sender.
		go func() {
			defer wg.Done()
			<-start
			_, _, err := c.GetOrLoad("shared", func() (any, error) {
				// Count atomically so the asserted total remains observable if a
				// coalescing defect admits callbacks concurrently instead of sharing one load.
				loads.Add(1)
				// Keep the first callback active briefly to offer later callers a
				// shared-load window; scheduling can also let them consume its cached result.
				time.Sleep(20 * time.Millisecond)
				return "value", nil
			})
			errCh <- err
		}()
	}
	// Release requests, wait for their buffered publications, then close results.
	// This order keeps the range below finite after all response errors are available.
	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	require.Equal(t, int32(1), loads.Load())
}

// TestParseBoolQueryWithDefault
// supplies recognized spellings, whitespace, and unknown text.
// Each result must match its configured default or parsed boolean, covering
// both default values and case-insensitive accepted true and false tokens.
func TestParseBoolQueryWithDefault(t *testing.T) {
	tests := []struct {
		name string // boolean parsing scenario name
		raw  string // supplied raw query spelling
		def  bool   // default for unknown or empty text
		want bool   // expected parsed boolean result
	}{
		{"empty returns default true", "", true, true},
		{"empty returns default false", "", false, false},
		{"1", "1", false, true},
		{"true", "true", false, true},
		{"TRUE", "TRUE", false, true},
		{"yes", "yes", false, true},
		{"on", "on", false, true},
		{"0", "0", true, false},
		{"false", "false", true, false},
		{"FALSE", "FALSE", true, false},
		{"no", "no", true, false},
		{"off", "off", true, false},
		{"whitespace trimmed", "  true  ", false, true},
		{"unknown returns default true", "maybe", true, true},
		{"unknown returns default false", "maybe", false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseBoolQueryWithDefault(tc.raw, tc.def)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestSnapshotContextCacheKeepsETagAndIsolation
// changes a returned map before a cached read.
// The next result must retain the original map value and tag, and an already
// cancelled context must return its cancellation error even for the cached key.
// This fixture exercises cancellation before lookup, not concurrent waiter departure.
func TestSnapshotContextCacheKeepsETagAndIsolation(t *testing.T) {
	cache := NewSnapshotCache(time.Minute)
	first, hit, err := cache.GetOrLoadContext(context.Background(), "snapshot", func(context.Context) (any, error) { return map[string]int{"requests": 17}, nil })
	require.NoError(t, err)
	require.False(t, hit)
	require.NotEmpty(t, first.ETag)
	firstPayload, typed := first.Payload.(map[string]int)
	require.True(t, typed)
	firstPayload["requests"] = 99
	second, hit, err := cache.GetOrLoadContext(context.Background(), "snapshot", func(context.Context) (any, error) { t.Fatal("缓存命中不应回源"); return nil, nil })
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, first.ETag, second.ETag)
	secondPayload, typed := second.Payload.(map[string]int)
	require.True(t, typed)
	require.Equal(t, 17, secondPayload["requests"])
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = cache.GetOrLoadContext(canceled, "snapshot", func(context.Context) (any, error) { return nil, nil })
	require.ErrorIs(t, err, context.Canceled)
}
