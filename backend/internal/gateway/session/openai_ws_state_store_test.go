package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// openAIWSStateStoreTimeoutProbeCache records deadlines during serial
// shared-cache calls. It returns one fixed provider, fails writes, and accepts
// deletion, so state-store tests can distinguish local publication from remote
// write success.
type openAIWSStateStoreTimeoutProbeCache struct {
	// setHasDeadline and setDeadlineDelta record whether Set received a
	// deadline and its remaining time. getHasDeadline/getDeadlineDelta and
	// deleteHasDeadline/delDeadlineDelta record the corresponding Get/Delete
	// observations. A call without a deadline retains the prior values.
	setHasDeadline    bool          // set operation observed deadline
	getHasDeadline    bool          // get operation observed deadline
	deleteHasDeadline bool          // delete operation observed deadline
	setDeadlineDelta  time.Duration // remaining set deadline duration
	getDeadlineDelta  time.Duration // remaining get deadline duration
	delDeadlineDelta  time.Duration // remaining delete deadline duration
}

// stubGatewayCache supplies serial hash-keyed storage for WS state fixtures.
// sessionBindings holds identifiers and deletedSessions counts recorded removal
// requests, with both maps accessed directly without copying or locking. The
// fixture ignores group IDs, cancellation, and TTL expiration; group-isolation
// tests construct a store without this shared fixture.
type stubGatewayCache struct {
	sessionBindings map[string]int64 // supplied hash to stored identifier
	deletedSessions map[string]int   // supplied hash to deletion count
}

// TestOpenAIWSStateStoreInvalidEncryptedContentLineage uses separate stores
// for parallel rejection-set scenarios. Sequential marks merge digests, copied
// reads allow independent mutation, another group misses, expired evidence is
// unavailable, and excess distinct input is bounded by the configured
// per-session capacity.
func TestOpenAIWSStateStoreInvalidEncryptedContentLineage(t *testing.T) {
	t.Parallel()

	t.Run("mark_get_roundtrip_with_merge", func(t *testing.T) {
		t.Parallel()
		store := NewOpenAIWSStateStore(nil)
		require.False(t, store.HasAnySessionInvalidEncryptedContent())
		require.Nil(t, store.GetSessionInvalidEncryptedContentDigests(1, "session-a"))

		store.MarkSessionInvalidEncryptedContent(1, "session-a", []string{"d1", "d2"}, time.Minute)
		store.MarkSessionInvalidEncryptedContent(1, "session-a", []string{"d2", "d3"}, time.Minute)
		require.True(t, store.HasAnySessionInvalidEncryptedContent())

		digests := store.GetSessionInvalidEncryptedContentDigests(1, "session-a")
		require.Len(t, digests, 3)
		for _, digest := range []string{"d1", "d2", "d3"} {
			require.Contains(t, digests, digest)
		}
		// The same session hash in another group remains isolated.
		require.Nil(t, store.GetSessionInvalidEncryptedContentDigests(2, "session-a"))
		// Mutating the returned copy must not alter stored digests.
		digests["d4"] = struct{}{}
		require.Len(t, store.GetSessionInvalidEncryptedContentDigests(1, "session-a"), 3)
	})

	t.Run("expired_binding_not_returned", func(t *testing.T) {
		t.Parallel()
		raw := NewOpenAIWSStateStore(nil)
		raw.MarkSessionInvalidEncryptedContent(1, "session-b", []string{"d1"}, time.Minute)
		store, ok := raw.(*defaultOpenAIWSStateStore)
		require.True(t, ok)
		// Move the stored deadline behind the current clock under its owning
		// lock so this read-time-expiration case needs no wall-clock wait or cleanup sweep.
		store.sessionInvalidEncryptedMu.Lock()
		binding := store.sessionInvalidEncrypted["1:session-b"]
		binding.expiresAt = time.Now().Add(-time.Second)
		store.sessionInvalidEncrypted["1:session-b"] = binding
		store.sessionInvalidEncryptedMu.Unlock()
		require.Nil(t, store.GetSessionInvalidEncryptedContentDigests(1, "session-b"))
	})

	t.Run("per_session_capacity_degrades_gracefully", func(t *testing.T) {
		t.Parallel()
		store := NewOpenAIWSStateStore(nil)
		// Supply ten more distinct digests than capacity so the assertion covers
		// retained set size without depending on observer output or later upstream recovery.
		oversized := make([]string, 0, openAIWSInvalidEncryptedDigestsPerSession+10)
		for i := range openAIWSInvalidEncryptedDigestsPerSession + 10 {
			oversized = append(oversized, fmt.Sprintf("digest-%d", i))
		}
		store.MarkSessionInvalidEncryptedContent(1, "session-c", oversized, time.Minute)
		digests := store.GetSessionInvalidEncryptedContentDigests(1, "session-c")
		require.Len(t, digests, openAIWSInvalidEncryptedDigestsPerSession)
	})
}

// TestOpenAIWSStateStore_BindGetDeleteResponseProvider binds one response
// through local state and the shared fixture. Lookup returns its provider,
// then deletion removes usable evidence and a later lookup returns zero with
// nil error.
func TestOpenAIWSStateStore_BindGetDeleteResponseProvider(t *testing.T) {
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	ctx := context.Background()
	groupID := int64(7)

	require.NoError(t, store.BindResponseProvider(ctx, groupID, "resp_abc", 101, time.Minute))

	providerID, err := store.GetResponseProvider(ctx, groupID, "resp_abc")
	require.NoError(t, err)
	require.Equal(t, int64(101), providerID)

	require.NoError(t, store.DeleteResponseProvider(ctx, groupID, "resp_abc"))
	providerID, err = store.GetResponseProvider(ctx, groupID, "resp_abc")
	require.NoError(t, err)
	require.Zero(t, providerID)
}

// TestOpenAIWSStateStore_ResponseProviderLocalCacheIsGroupScoped reuses one
// response ID under two groups with no shared cache. Each group retains its
// own provider, and deleting the first group's binding leaves the second
// group's value readable.
func TestOpenAIWSStateStore_ResponseProviderLocalCacheIsGroupScoped(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	ctx := context.Background()

	require.NoError(t, store.BindResponseProvider(ctx, 7, "resp_shared", 101, time.Minute))

	providerID, err := store.GetResponseProvider(ctx, 7, "resp_shared")
	require.NoError(t, err)
	require.Equal(t, int64(101), providerID)

	providerID, err = store.GetResponseProvider(ctx, 8, "resp_shared")
	require.NoError(t, err)
	require.Zero(t, providerID, "本地 response 绑定必须按 group 隔离，避免跨组命中")

	require.NoError(t, store.BindResponseProvider(ctx, 8, "resp_shared", 202, time.Minute))
	providerID, err = store.GetResponseProvider(ctx, 8, "resp_shared")
	require.NoError(t, err)
	require.Equal(t, int64(202), providerID)

	require.NoError(t, store.DeleteResponseProvider(ctx, 7, "resp_shared"))
	providerID, err = store.GetResponseProvider(ctx, 8, "resp_shared")
	require.NoError(t, err)
	require.Equal(t, int64(202), providerID, "删除某个 group 的绑定不应影响其它 group")
}

// TestOpenAIWSStateStore_HTTPResponseOwnerPersistsAcrossStoreInstances writes
// one positive owner pair through a shared fixture. The writer's local lookup
// and a fresh store's shared lookup both recover the supplied user and API
// Key.
func TestOpenAIWSStateStore_HTTPResponseOwnerPersistsAcrossStoreInstances(t *testing.T) {
	cache := &stubGatewayCache{}
	ctx := context.Background()
	groupID := int64(8)
	writer := NewOpenAIWSStateStore(cache)

	require.NoError(t, writer.BindHTTPResponseOwner(ctx, groupID, "resp_owned", 201, 301, time.Minute))
	userID, apiKeyID, found, err := writer.GetHTTPResponseOwner(ctx, groupID, "resp_owned")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(201), userID)
	require.Equal(t, int64(301), apiKeyID)

	reader := NewOpenAIWSStateStore(cache)
	userID, apiKeyID, found, err = reader.GetHTTPResponseOwner(ctx, groupID, "resp_owned")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(201), userID)
	require.Equal(t, int64(301), apiKeyID)
}

// TestOpenAIWSStateStore_ResponseConnTTL binds one preferred socket for a
// short response lifetime. Immediate lookup returns that connection, and
// lookup after the wait reports no live affinity for the same response ID.
func TestOpenAIWSStateStore_ResponseConnTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	store.BindResponseConn("resp_conn", "conn_1", 30*time.Millisecond)

	connID, ok := store.GetResponseConn("resp_conn")
	require.True(t, ok)
	require.Equal(t, "conn_1", connID)

	// Wait twice the response-affinity TTL so the next lookup observes expiration
	// without relying on the minute-based physical cleanup election to remove its
	// entry.
	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetResponseConn("resp_conn")
	require.False(t, ok)
}

// TestOpenAIWSStateStore_SessionTurnStateTTL binds opaque state for one group
// and session. It reads back immediately, misses under another group, and
// becomes unavailable under the original group after its short lifetime has
// passed.
func TestOpenAIWSStateStore_SessionTurnStateTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	store.BindSessionTurnState(9, "session_hash_1", "turn_state_1", 30*time.Millisecond)

	state, ok := store.GetSessionTurnState(9, "session_hash_1")
	require.True(t, ok)
	require.Equal(t, "turn_state_1", state)

	// Session bindings are isolated by group.
	_, ok = store.GetSessionTurnState(10, "session_hash_1")
	require.False(t, ok)

	// Wait past the short opaque-state lifetime so read validity rejects this
	// group/session value even though periodic physical cleanup has not been
	// elected.
	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetSessionTurnState(9, "session_hash_1")
	require.False(t, ok)
}

// TestOpenAIWSStateStore_SessionConnTTL binds a preferred socket under one
// session and group. Another group cannot use it, and the original session's
// lookup becomes unavailable after the short lifetime passes.
func TestOpenAIWSStateStore_SessionConnTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	store.BindSessionConn(9, "session_hash_conn_1", "conn_1", 30*time.Millisecond)

	connID, ok := store.GetSessionConn(9, "session_hash_conn_1")
	require.True(t, ok)
	require.Equal(t, "conn_1", connID)

	// Session bindings are isolated by group.
	_, ok = store.GetSessionConn(10, "session_hash_conn_1")
	require.False(t, ok)

	// Wait twice the session-socket TTL so that identity loses its preferred
	// connection through read-time expiration before the periodic cleanup interval
	// is reached.
	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetSessionConn(9, "session_hash_conn_1")
	require.False(t, ok)
}

// TestOpenAIWSStateStore_GetResponseProvider_NoStaleAfterCacheMiss reads a
// provider from shared fixture storage without a local bind. Removing that
// shared value makes the next lookup return zero, checking that shared reads
// do not warm local state.
func TestOpenAIWSStateStore_GetResponseProvider_NoStaleAfterCacheMiss(t *testing.T) {
	cache := &stubGatewayCache{sessionBindings: map[string]int64{}}
	store := NewOpenAIWSStateStore(cache)
	ctx := context.Background()
	groupID := int64(17)
	responseID := "resp_cache_stale"
	cacheKey := openAIWSResponseProviderCacheKey(responseID)

	cache.sessionBindings[cacheKey] = 501
	providerID, err := store.GetResponseProvider(ctx, groupID, responseID)
	require.NoError(t, err)
	require.Equal(t, int64(501), providerID)

	delete(cache.sessionBindings, cacheKey)
	providerID, err = store.GetResponseProvider(ctx, groupID, responseID)
	require.NoError(t, err)
	require.Zero(t, providerID, "上游缓存失效后不应继续命中本地陈旧映射")
}

// TestCleanupExpiredBindings_AllKinds supplies each binding map with live,
// elapsed, and exact-deadline entries. Cleanup respects the scan budget and
// keeps retained values unchanged for every binding representation.
func TestCleanupExpiredBindings_AllKinds(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		testCleanupExpiredBindings(t, func(expiresAt time.Time) openAIWSProviderBinding {
			return openAIWSProviderBinding{providerID: 7, expiresAt: expiresAt}
		}, func(binding openAIWSProviderBinding) time.Time {
			return binding.expiresAt
		})
	})
	t.Run("http_owner", func(t *testing.T) {
		testCleanupExpiredBindings(t, func(expiresAt time.Time) openAIHTTPResponseOwnerBinding {
			return openAIHTTPResponseOwnerBinding{userID: 11, apiKeyID: 13, expiresAt: expiresAt}
		}, func(binding openAIHTTPResponseOwnerBinding) time.Time {
			return binding.expiresAt
		})
	})
	t.Run("response_connection", func(t *testing.T) {
		testCleanupExpiredBindings(t, func(expiresAt time.Time) openAIWSConnBinding {
			return openAIWSConnBinding{connID: "response-connection", expiresAt: expiresAt}
		}, func(binding openAIWSConnBinding) time.Time {
			return binding.expiresAt
		})
	})
	t.Run("turn_state", func(t *testing.T) {
		testCleanupExpiredBindings(t, func(expiresAt time.Time) openAIWSTurnStateBinding {
			return openAIWSTurnStateBinding{turnState: "opaque-state", expiresAt: expiresAt}
		}, func(binding openAIWSTurnStateBinding) time.Time {
			return binding.expiresAt
		})
	})
	t.Run("session_connection", func(t *testing.T) {
		testCleanupExpiredBindings(t, func(expiresAt time.Time) openAIWSSessionConnBinding {
			return openAIWSSessionConnBinding{connID: "session-connection", expiresAt: expiresAt}
		}, func(binding openAIWSSessionConnBinding) time.Time {
			return binding.expiresAt
		})
	})
	t.Run("invalid_encrypted", func(t *testing.T) {
		testCleanupExpiredBindings(t, func(expiresAt time.Time) openAIWSInvalidEncryptedBinding {
			return openAIWSInvalidEncryptedBinding{digests: map[string]struct{}{"digest": {}}, expiresAt: expiresAt}
		}, func(binding openAIWSInvalidEncryptedBinding) time.Time {
			return binding.expiresAt
		})
	})
}

// testCleanupExpiredBindings checks expiry and budget cases for one binding
// representation. Each scenario uses a separate map owned by this serial
// fixture, so cleanup needs no external map lock while reading supplied values.
func testCleanupExpiredBindings[T any](t *testing.T, newBinding func(time.Time) T, expiresAt func(T) time.Time) {
	t.Helper()
	now := time.Date(2026, time.October, 23, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string          // expiry and budget scenario
		deadlines     []time.Duration // deadline offsets from now
		maxScan       int             // inspected entry count limit
		wantRemaining int             // expected retained entry count
	}{
		{name: "all_live", deadlines: []time.Duration{time.Second, time.Second, time.Second}, maxScan: 2, wantRemaining: 3},
		{name: "all_expired", deadlines: []time.Duration{-time.Second, -time.Second, -time.Second}, maxScan: 3},
		{name: "bounded_expired", deadlines: []time.Duration{-time.Second, -time.Second, -time.Second}, maxScan: 2, wantRemaining: 1},
		{name: "equal_deadline", deadlines: []time.Duration{0, 0, 0}, maxScan: 3, wantRemaining: 3},
		{name: "mixed_deadlines", deadlines: []time.Duration{-time.Second, 0, time.Second}, maxScan: 3, wantRemaining: 2},
		{name: "zero_budget", deadlines: []time.Duration{-time.Second, -time.Second, -time.Second}, wantRemaining: 3},
		{name: "negative_budget", deadlines: []time.Duration{-time.Second, -time.Second, -time.Second}, maxScan: -1, wantRemaining: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bindings := make(map[string]T, len(test.deadlines))
			original := make(map[string]T, len(test.deadlines))
			for i, deadline := range test.deadlines {
				key := fmt.Sprintf("binding-%d", i)
				bindings[key] = newBinding(now.Add(deadline))
				original[key] = bindings[key]
			}

			cleanupExpiredBindings(bindings, now, test.maxScan, expiresAt)

			require.Len(t, bindings, test.wantRemaining)
			for key, binding := range bindings {
				require.Equal(t, original[key], binding)
			}
			if test.maxScan >= len(test.deadlines) {
				for i, deadline := range test.deadlines {
					_, retained := bindings[fmt.Sprintf("binding-%d", i)]
					require.Equal(t, deadline >= 0, retained)
				}
			}
		})
	}

	t.Run("nil_map", func(t *testing.T) {
		var bindings map[string]T
		cleanupExpiredBindings(bindings, now, 2, expiresAt)
		require.Nil(t, bindings)
	})
	t.Run("empty_map", func(t *testing.T) {
		bindings := make(map[string]T)
		cleanupExpiredBindings(bindings, now, 2, expiresAt)
		require.Empty(t, bindings)
		require.NotNil(t, bindings)
	})
}

// TestCleanupExpiredBindings_InspectedEntryBudget counts expiry reads while
// every binding remains live. Scanning stops at the visited-entry budget, and
// disabled budgets or absent bindings never invoke the expiry reader.
func TestCleanupExpiredBindings_InspectedEntryBudget(t *testing.T) {
	now := time.Date(2026, time.October, 23, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string // map and budget scenario
		entries   int    // initial live binding count
		maxScan   int    // inspected entry count limit
		wantReads int    // expected expiry reader calls
	}{
		{name: "bounded_live", entries: 5, maxScan: 2, wantReads: 2},
		{name: "larger_budget", entries: 2, maxScan: 5, wantReads: 2},
		{name: "zero_budget", entries: 5},
		{name: "negative_budget", entries: 5, maxScan: -1},
		{name: "empty_map", maxScan: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bindings := make(map[string]time.Time, test.entries)
			for i := range test.entries {
				bindings[fmt.Sprintf("binding-%d", i)] = now.Add(time.Hour)
			}
			reads := 0
			cleanupExpiredBindings(bindings, now, test.maxScan, func(deadline time.Time) time.Time {
				reads++
				return deadline
			})
			require.Equal(t, test.wantReads, reads)
			require.Len(t, bindings, test.entries)
		})
	}
}

// TestOpenAIWSStateStore_MaybeCleanupRemovesExpiredIncrementally preloads all
// six maps with elapsed bindings. Each elected round removes the per-map
// budget until every map is empty, with election times advanced between calls.
func TestOpenAIWSStateStore_MaybeCleanupRemovesExpiredIncrementally(t *testing.T) {
	// Leave a partial third round to check both the scan limit and exhausting
	// a smaller remainder, independently of unordered iteration within each map.
	total := 2*openAIWSStateStoreCleanupMaxPerMap + 17
	store := newExpiredOpenAIWSStateStore(t, total)
	remaining := total
	for range 3 {
		// Advance the election timestamp so each call scans immediately while the
		// binding deadlines remain elapsed, avoiding a real cleanup-interval wait.
		store.lastCleanupUnixNano.Store(time.Now().Add(-2 * openAIWSStateStoreCleanupInterval).UnixNano())
		store.maybeCleanup()
		remaining = max(0, remaining-openAIWSStateStoreCleanupMaxPerMap)
		requireOpenAIWSStateStoreBindingCount(t, store, remaining)
	}
}

// TestOpenAIWSStateStore_MaybeCleanupElectsOneCaller releases simultaneous
// cleanup callers against all six expired maps. One interval consumes exactly
// one scan budget per map, and a subsequent call in that interval is skipped.
func TestOpenAIWSStateStore_MaybeCleanupElectsOneCaller(t *testing.T) {
	total := 2 * openAIWSStateStoreCleanupMaxPerMap
	store := newExpiredOpenAIWSStateStore(t, total)
	store.lastCleanupUnixNano.Store(time.Now().Add(-2 * openAIWSStateStoreCleanupInterval).UnixNano())

	// Release the callers together so the atomic election competes before any
	// final size assertions, then wait for every caller to finish its map access.
	start := make(chan struct{})
	var callers sync.WaitGroup
	for range 16 {
		callers.Go(func() {
			<-start
			store.maybeCleanup()
		})
	}
	close(start)
	callers.Wait()
	requireOpenAIWSStateStoreBindingCount(t, store, openAIWSStateStoreCleanupMaxPerMap)
	store.maybeCleanup()
	requireOpenAIWSStateStoreBindingCount(t, store, openAIWSStateStoreCleanupMaxPerMap)
}

// newExpiredOpenAIWSStateStore fills all six local maps with elapsed bindings.
// The returned store belongs to the calling test, which starts cleanup after
// this initialization and controls any concurrent access afterward.
func newExpiredOpenAIWSStateStore(t *testing.T, total int) *defaultOpenAIWSStateStore {
	t.Helper()
	store, ok := NewOpenAIWSStateStore(nil).(*defaultOpenAIWSStateStore)
	require.True(t, ok)
	expiresAt := time.Now().Add(-time.Minute)
	for i := range total {
		key := fmt.Sprintf("expired-%d", i)
		store.responseToProvider[key] = openAIWSProviderBinding{expiresAt: expiresAt}
		store.responseOwners[key] = openAIHTTPResponseOwnerBinding{expiresAt: expiresAt}
		store.responseToConn[key] = openAIWSConnBinding{expiresAt: expiresAt}
		store.sessionToTurnState[key] = openAIWSTurnStateBinding{expiresAt: expiresAt}
		store.sessionToConn[key] = openAIWSSessionConnBinding{expiresAt: expiresAt}
		store.sessionInvalidEncrypted[key] = openAIWSInvalidEncryptedBinding{expiresAt: expiresAt}
	}
	return store
}

// requireOpenAIWSStateStoreBindingCount compares every local map with one
// expected count. Callers finish all concurrent store operations before this
// assertion because the fixture reads the maps directly without taking locks.
func requireOpenAIWSStateStoreBindingCount(t *testing.T, store *defaultOpenAIWSStateStore, want int) {
	t.Helper()
	require.Len(t, store.responseToProvider, want, "provider bindings")
	require.Len(t, store.responseOwners, want, "HTTP owner bindings")
	require.Len(t, store.responseToConn, want, "response connection bindings")
	require.Len(t, store.sessionToTurnState, want, "turn state bindings")
	require.Len(t, store.sessionToConn, want, "session connection bindings")
	require.Len(t, store.sessionInvalidEncrypted, want, "invalid encrypted content bindings")
}

// TestEnsureBindingCapacity_EvictsOneWhenMapIsFull admits a third key into a
// two-entry map. After insertion the map remains at capacity and contains the
// new value, while the evicted existing key is deliberately unspecified.
func TestEnsureBindingCapacity_EvictsOneWhenMapIsFull(t *testing.T) {
	bindings := map[string]int{
		"a": 1,
		"b": 2,
	}

	ensureBindingCapacity(bindings, "c", 2)
	bindings["c"] = 3

	require.Len(t, bindings, 2)
	require.Equal(t, 3, bindings["c"])
}

// TestEnsureBindingCapacity_DoesNotEvictWhenUpdatingExistingKey replaces an
// existing value in a full two-entry map. Its size remains two and the updated
// key holds the replacement, covering admission without removing another key.
func TestEnsureBindingCapacity_DoesNotEvictWhenUpdatingExistingKey(t *testing.T) {
	bindings := map[string]int{
		"a": 1,
		"b": 2,
	}

	ensureBindingCapacity(bindings, "a", 2)
	bindings["a"] = 9

	require.Len(t, bindings, 2)
	require.Equal(t, 9, bindings["a"])
}

// TestOpenAIWSStateStore_RedisOpsUseShortTimeout probes shared write, delete,
// and remote-only read contexts. A failed shared write leaves its provider
// locally readable without a shared Get; the observed shared deadlines fall
// within the asserted two-to-three-second remaining-duration range.
func TestOpenAIWSStateStore_RedisOpsUseShortTimeout(t *testing.T) {
	probe := &openAIWSStateStoreTimeoutProbeCache{}
	store := NewOpenAIWSStateStore(probe)
	ctx := context.Background()
	groupID := int64(5)

	err := store.BindResponseProvider(ctx, groupID, "resp_timeout_probe", 11, time.Minute)
	require.Error(t, err)

	providerID, getErr := store.GetResponseProvider(ctx, groupID, "resp_timeout_probe")
	require.NoError(t, getErr)
	require.Equal(t, int64(11), providerID, "本地缓存命中应优先返回已绑定提供商")

	require.NoError(t, store.DeleteResponseProvider(ctx, groupID, "resp_timeout_probe"))

	require.True(t, probe.setHasDeadline, "SetSessionProviderID 应携带独立超时上下文")
	require.True(t, probe.deleteHasDeadline, "DeleteSessionProviderID 应携带独立超时上下文")
	require.False(t, probe.getHasDeadline, "GetSessionProviderID 本用例应由本地缓存命中，不触发 Redis 读取")
	// Permit one second of fixture overhead while retaining the three-second
	// upper deadline, so these assertions inspect attached durations rather than elapsed storage work.
	require.Greater(t, probe.setDeadlineDelta, 2*time.Second)
	require.LessOrEqual(t, probe.setDeadlineDelta, 3*time.Second)
	require.Greater(t, probe.delDeadlineDelta, 2*time.Second)
	require.LessOrEqual(t, probe.delDeadlineDelta, 3*time.Second)

	probe2 := &openAIWSStateStoreTimeoutProbeCache{}
	store2 := NewOpenAIWSStateStore(probe2)
	providerID2, err2 := store2.GetResponseProvider(ctx, groupID, "resp_cache_only")
	require.NoError(t, err2)
	require.Equal(t, int64(123), providerID2)
	require.True(t, probe2.getHasDeadline, "GetSessionProviderID 在缓存未命中时应携带独立超时上下文")
	require.Greater(t, probe2.getDeadlineDelta, 2*time.Second)
	require.LessOrEqual(t, probe2.getDeadlineDelta, 3*time.Second)
}

// TestWithOpenAIWSStateStoreRedisTimeout_WithParentContext derives a storage
// context from context.Background. The returned context is nonnil and has a
// deadline; this fixture does not exercise cancellation or an earlier parent
// deadline.
func TestWithOpenAIWSStateStoreRedisTimeout_WithParentContext(t *testing.T) {
	ctx, cancel := withOpenAIWSStateStoreRedisTimeout(context.Background())
	defer cancel()
	require.NotNil(t, ctx)
	_, ok := ctx.Deadline()
	require.True(t, ok, "应附加短超时")
}

// GetSessionProviderID records any supplied deadline and returns provider 123.
// The serial fixture captures remaining duration at invocation, allowing the
// test to observe the attached context deadline while the probe returns immediately.
func (c *openAIWSStateStoreTimeoutProbeCache) GetSessionProviderID(ctx context.Context, _ int64, _ string) (int64, error) {
	if deadline, ok := ctx.Deadline(); ok {
		c.getHasDeadline = true
		c.getDeadlineDelta = time.Until(deadline)
	}
	return 123, nil
}

// SetSessionProviderID records the caller's deadline and returns its
// configured write-failure error. This serial probe leaves persistence
// unsuccessful so tests can separately inspect the state store's previously
// published local binding.
func (c *openAIWSStateStoreTimeoutProbeCache) SetSessionProviderID(ctx context.Context, _ int64, _ string, _ int64, _ time.Duration) error {
	if deadline, ok := ctx.Deadline(); ok {
		c.setHasDeadline = true
		c.setDeadlineDelta = time.Until(deadline)
	}
	return errors.New("set failed")
}

// RefreshSessionTTL accepts renewal requests without changing the serial
// deadline probe.
func (c *openAIWSStateStoreTimeoutProbeCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

// DeleteSessionProviderID records a supplied deadline and reports successful
// removal. It mutates the serial probe's presence and duration fields alone,
// allowing the caller to assert its derived shared-storage context deadline.
func (c *openAIWSStateStoreTimeoutProbeCache) DeleteSessionProviderID(ctx context.Context, _ int64, _ string) error {
	if deadline, ok := ctx.Deadline(); ok {
		c.deleteHasDeadline = true
		c.delDeadlineDelta = time.Until(deadline)
	}
	return nil
}

// SetSessionOwnerGroupID reports successful insertion for every supplied
// identity. The deadline probe stores no first-owner state, so this capability
// simply lets it satisfy the GatewayCache interface used by state-store
// fixtures.
func (c *openAIWSStateStoreTimeoutProbeCache) SetSessionOwnerGroupID(context.Context, int64, string, string, int64, time.Duration) (bool, error) {
	return true, nil
}

// GetSessionOwnerGroupID returns a not-found error for every ownership
// identity. The probe records deadline behavior for provider-shaped calls
// alone, leaving first-owner persistence unmodeled by this serial test
// adapter.
func (c *openAIWSStateStoreTimeoutProbeCache) GetSessionOwnerGroupID(context.Context, int64, string, string) (int64, error) {
	return 0, errors.New("not found")
}

// RefreshSessionOwnerTTL accepts owner renewals without modifying the serial
// deadline probe.
func (c *openAIWSStateStoreTimeoutProbeCache) RefreshSessionOwnerTTL(context.Context, int64, string, string, time.Duration) error {
	return nil
}

// GetSessionProviderID returns the identifier stored under the supplied hash.
// An absent hash returns a not-found error; groupID and cancellation are
// ignored, so tests model shared identifier values through serial map access.
func (c *stubGatewayCache) GetSessionProviderID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	if id, ok := c.sessionBindings[sessionHash]; ok {
		return id, nil
	}
	return 0, errors.New("not found")
}

// SetSessionProviderID assigns an identifier under the supplied hash,
// initializing the serial fixture map when needed. It ignores groupID, TTL
// expiration, and context cancellation, accepting both provider and HTTP-owner
// component identifiers.
func (c *stubGatewayCache) SetSessionProviderID(ctx context.Context, groupID int64, sessionHash string, providerID int64, ttl time.Duration) error {
	if c.sessionBindings == nil {
		c.sessionBindings = make(map[string]int64)
	}
	c.sessionBindings[sessionHash] = providerID
	return nil
}

// RefreshSessionTTL accepts lifetime requests without changing serial
// shared-fixture state.
func (c *stubGatewayCache) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	return nil
}

// DeleteSessionProviderID removes a supplied hash and counts the request when
// the binding map exists. Missing keys still count; a nil binding map skips
// both removal and counting in this serial fixture.
func (c *stubGatewayCache) DeleteSessionProviderID(ctx context.Context, groupID int64, sessionHash string) error {
	if c.sessionBindings == nil {
		return nil
	}
	if c.deletedSessions == nil {
		c.deletedSessions = make(map[string]int)
	}
	c.deletedSessions[sessionHash]++
	delete(c.sessionBindings, sessionHash)
	return nil
}

// SetSessionOwnerGroupID reports true for every first-owner insertion request.
// It stores no ownership record, so fixtures requiring first-group competition
// use another cache implementation with actual owner state.
func (c *stubGatewayCache) SetSessionOwnerGroupID(ctx context.Context, userID int64, source, sessionHash string, groupID int64, ttl time.Duration) (bool, error) {
	return true, nil
}

// GetSessionOwnerGroupID reports a not-found error for every first-owner
// identity. This fixture stores hash-to-identifier values for state-store
// tests alone; successful owner insertion results cannot be recovered through
// this separate method.
func (c *stubGatewayCache) GetSessionOwnerGroupID(ctx context.Context, userID int64, source, sessionHash string) (int64, error) {
	return 0, errors.New("not found")
}

// RefreshSessionOwnerTTL accepts owner lifetime requests without changing
// shared-fixture state.
func (c *stubGatewayCache) RefreshSessionOwnerTTL(ctx context.Context, userID int64, source, sessionHash string, ttl time.Duration) error {
	return nil
}
