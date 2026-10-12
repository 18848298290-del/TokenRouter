package notification

import (
	"context"
	"sync"
)

// Equal delivery keys share one admission token while distinct keys can proceed
// independently. A waiter registers its reference before waiting for that token,
// so releasing a current holder cannot remove the entry and let a later waiter
// acquire a different token for the same active key.
//
// Cancellation removes a waiter's reference. If cancellation is observed after
// token acquisition, the helper returns the token before reporting the error.
// A release function performs its return once and removes the key entry after
// the last holder or waiter releases its reference. The coordination applies
// within the owning NotificationEmailService instance.

// keyCoordinator serializes keyed notification work and retains entries for waiting callers.
type keyCoordinator struct {
	// mu protects entries and each keyEntry.refs count; token ownership is
	// transferred through the entry channel after its map reference is registered.
	mu      sync.Mutex           // entry bookkeeping lock
	entries map[string]*keyEntry // active keyed entries
}

// keyEntry tracks one shared admission token and all its holders or waiters.
type keyEntry struct {
	// token and refs serve different lifetimes: the token admits one holder,
	// while refs retains the entry for that holder and every registered waiter
	// until their cancellation or release drops the last reference.
	token chan struct{} // available admission token
	refs  int           // registered caller references
}

// acquire returns an idempotent release function after obtaining the key's shared token.
// Cancellation observed during acquisition returns its error and no release function. The
// caller releases after the entire coordinated operation, including its
// persistence or delivery phases, finishes.
//
// Registration increments refs before waiting, preventing map cleanup from
// splitting active callers across separate entries. Cancellation before token
// acquisition drops that reference; cancellation observed after acquisition
// returns the unused token and then drops it. Repeated release calls use one
// sync.Once, so bookkeeping and token capacity are changed once for that holder.
func (c *keyCoordinator) acquire(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Register the caller under the map lock before it waits for admission;
	// the retained reference prevents another caller from creating a replacement entry for this key.
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*keyEntry)
	}
	entry := c.entries[key]
	if entry == nil {
		entry = &keyEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		c.entries[key] = entry
	}
	entry.refs++
	c.mu.Unlock()
	drop := func() {
		// Last-reference removal happens under the same lock as registration.
		// Waiters retain the entry until they either cancel or later release their acquired token.
		c.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(c.entries, key)
		}
		c.mu.Unlock()
	}
	// A canceled waiter leaves the entry available to its current holder;
	// receiving the token instead transfers admission while retaining this caller's registered reference.
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-entry.token:
	}
	var once sync.Once
	// Return admission before dropping the reference so another registered waiter
	// can obtain the same entry's token even as the previous holder finishes bookkeeping.
	release := func() { once.Do(func() { entry.token <- struct{}{}; drop() }) }
	if err := ctx.Err(); err != nil {
		// Cancellation may have raced with admission. Release the unused token
		// through the same once guard before exposing cancellation to the waiting caller.
		release()
		return nil, err
	}
	return release, nil
}
