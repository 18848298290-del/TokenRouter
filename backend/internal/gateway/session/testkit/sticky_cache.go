package testkit

import (
	"context"
	"errors"
	"time"
)

// Gateway tests can record provider bindings by session hash and count removals.
// The fixture ignores group IDs, cancellation, and TTL expiration, so tests using
// it establish their own isolation and timing behavior separately. First-owner
// methods accept every insertion and retain no ownership state.

// StickyCache records serial test bindings and removal counts by session hash.
// SessionBindings and DeletedSessions are supplied mutable maps, accessed directly
// without synchronization or expiration; callers coordinate all reads and writes.
// Group IDs do not partition this fixture's bindings.
type StickyCache struct {
	SessionBindings map[string]int64 // session hash to fixture provider
	DeletedSessions map[string]int   // session hash to removal count
}

// GetSessionProviderID returns the fixture provider stored under sessionHash.
// An absent hash returns a not-found error; groupID and ctx are ignored, so this
// lookup models the test binding map alone.
func (c *StickyCache) GetSessionProviderID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	if id, ok := c.SessionBindings[sessionHash]; ok {
		return id, nil
	}
	return 0, errors.New("not found")
}

// SetSessionProviderID records providerID under sessionHash, initializing its map
// when needed. The fixture ignores groupID, context cancellation, and ttl; callers
// provide serial access to the mutable binding map.
func (c *StickyCache) SetSessionProviderID(ctx context.Context, groupID int64, sessionHash string, providerID int64, ttl time.Duration) error {
	if c.SessionBindings == nil {
		c.SessionBindings = make(map[string]int64)
	}
	c.SessionBindings[sessionHash] = providerID
	return nil
}

// RefreshSessionTTL accepts the request without changing the serial fixture state.
func (c *StickyCache) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	return nil
}

// DeleteSessionProviderID removes sessionHash and counts the deletion request when
// the binding map exists. Missing hashes still increment the count; a nil binding
// map skips both counting and removal.
func (c *StickyCache) DeleteSessionProviderID(ctx context.Context, groupID int64, sessionHash string) error {
	if c.SessionBindings == nil {
		return nil
	}
	if c.DeletedSessions == nil {
		c.DeletedSessions = make(map[string]int)
	}
	c.DeletedSessions[sessionHash]++
	delete(c.SessionBindings, sessionHash)
	return nil
}

// SetSessionOwnerGroupID reports successful insertion for every supplied identity.
// It stores no owner and ignores TTL, so tests requiring competing first-owner
// behavior use a fixture that implements that state separately.
func (c *StickyCache) SetSessionOwnerGroupID(ctx context.Context, userID int64, source, sessionHash string, groupID int64, ttl time.Duration) (bool, error) {
	return true, nil
}

// GetSessionOwnerGroupID reports a not-found error for every owner identity.
// The fixture retains provider bindings alone, so its successful owner insertions
// cannot be recovered through this lookup method.
func (c *StickyCache) GetSessionOwnerGroupID(ctx context.Context, userID int64, source, sessionHash string) (int64, error) {
	return 0, errors.New("not found")
}

// RefreshSessionOwnerTTL accepts the request without changing serial fixture state.
func (c *StickyCache) RefreshSessionOwnerTTL(ctx context.Context, userID int64, source, sessionHash string, ttl time.Duration) error {
	return nil
}
