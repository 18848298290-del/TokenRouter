package session

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestAnthropicPromptCacheScopePrefixAndExpiry checks isolation, longest-prefix matching, replacement, and expiry.
func TestAnthropicPromptCacheScopePrefixAndExpiry(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	store := NewAnthropicPromptCache(func() time.Time { return now })
	store.Bind(1, 2, "s:a-u:b", " key ", "", time.Hour)
	key, chain := store.Find(1, 2, "s:a-u:b-a:c-u:d")
	require.Equal(t, "key", key)
	require.Equal(t, "s:a-u:b", chain)
	key, _ = store.Find(2, 2, "s:a-u:b")
	require.Empty(t, key)
	key, _ = store.Find(1, 3, "s:a-u:b")
	require.Empty(t, key)
	store.Bind(1, 2, "s:a-u:b-a:c", "new", "s:a-u:b", time.Hour)
	key, _ = store.Find(1, 2, "s:a-u:b")
	require.Empty(t, key)
	key, chain = store.Find(1, 2, "s:a-u:b-a:c-u:d")
	require.Equal(t, "new", key)
	require.Equal(t, "s:a-u:b-a:c", chain)
	now = now.Add(time.Hour)
	key, chain = store.Find(1, 2, "s:a-u:b-a:c-u:d")
	require.Empty(t, key)
	require.Empty(t, chain)
}

// TestAnthropicPromptCacheConcurrentBindings checks provider isolation for concurrent writes of the same digest.
func TestAnthropicPromptCacheConcurrentBindings(t *testing.T) {
	store := NewAnthropicPromptCache(time.Now)
	var pending sync.WaitGroup
	for id := int64(1); id <= 20; id++ {
		pending.Go(func() {
			store.Bind(id, 5, "s:a-u:b", fmt.Sprint(id), "", time.Hour)
		})
	}
	pending.Wait()
	for id := int64(1); id <= 20; id++ {
		key, chain := store.Find(id, 5, "s:a-u:b-a:c")
		require.Equal(t, fmt.Sprint(id), key)
		require.Equal(t, "s:a-u:b", chain)
	}
}
