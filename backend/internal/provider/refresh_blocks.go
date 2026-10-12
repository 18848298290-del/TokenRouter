package provider

import (
	"sync"
	"time"
)

// RefreshFailureBlocks stores temporary blocks by credential identity; RuntimeBlockState owns provider quota
// and capacity blocks.
// Each in-flight credential version has an independent expiry time.
type RefreshFailureBlocks struct {
	mu      sync.Mutex
	entries map[int64]map[string]time.Time
}

func (b *RefreshFailureBlocks) Block(id int64, identity string, until, now time.Time) {
	if b == nil || id <= 0 || identity == "" || !until.After(now) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entries == nil {
		b.entries = make(map[int64]map[string]time.Time)
	}
	scopes := b.entries[id]
	if scopes == nil {
		scopes = make(map[string]time.Time)
		b.entries[id] = scopes
	}
	for key, deadline := range scopes {
		if !deadline.After(now) {
			delete(scopes, key)
		}
	}
	if until.After(scopes[identity]) {
		scopes[identity] = until
	}
}

// Blocked evaluates identity only when live entries exist; the callback must hash the current credential data.
func (b *RefreshFailureBlocks) Blocked(id int64, now time.Time, identity func() string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	scopes := b.entries[id]
	for key, deadline := range scopes {
		if !deadline.After(now) {
			delete(scopes, key)
		}
	}
	if len(scopes) == 0 {
		delete(b.entries, id)
		b.mu.Unlock()
		return false
	}
	b.mu.Unlock()
	// Compute the identity outside the shared lock so other providers can be checked concurrently.
	key := identity()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.entries[id][key].After(now)
}

func (b *RefreshFailureBlocks) Clear(id int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	delete(b.entries, id)
	b.mu.Unlock()
}
