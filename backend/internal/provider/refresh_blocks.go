package provider

import (
	"sync"
	"time"
)

// Refresh failures apply to the credentials that were attempted, so several
// versions of one provider can carry different deadlines. Extending one version
// leaves other identities intact, and a shorter later notice cannot reduce that
// version's existing deadline. Cleanup removes deadlines that are no longer
// after the supplied observation time.
//
// The mutex protects deadline mutation and reads, while credential hashing runs
// outside that mutex. Blocked first removes expired entries and skips hashing
// when none remain, then rechecks the current map after the callback. A
// concurrent Clear therefore prevents the earlier map snapshot from restoring a
// cleared block. RuntimeBlockState separately owns provider-wide quota and
// capacity pauses.

// RefreshFailureBlocks tracks independent deadlines for credential versions of
// each provider.
type RefreshFailureBlocks struct {
	// mu protects entries and every provider's nested identity map. The zero
	// value allocates storage on its first usable block, and readers remove
	// expired entries before deciding whether identity evaluation is needed.
	mu      sync.Mutex                     // deadline map synchronization
	entries map[int64]map[string]time.Time // provider and identity deadlines
}

// Block extends the selected provider and credential identity to until when that
// deadline is after now. Nil state, nonpositive IDs, empty identities and
// already expired deadlines are ignored.
//
// Under the mutex, expired identities for this provider are removed before the
// selected deadline is compared. A shorter notice leaves its existing deadline
// intact, while another identity keeps its own expiry. Callers supply a
// consistent observation time and captured authentication identity so unrelated
// credential versions are not associated.
func (b *RefreshFailureBlocks) Block(id int64, identity string, until, now time.Time) {
	if b == nil || id <= 0 || identity == "" || !until.After(now) {
		return
	}
	// Expire old versions and extend the selected deadline under one lock so
	// overlapping notices cannot shorten a live block for the same credential.
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

// Blocked reports whether the current credential identity has a deadline after
// now. It removes expired entries and avoids calling identity when this provider
// has no live entries.
//
// When reached, identity must be callable and return the currently observed
// authentication digest. The callback runs outside the shared mutex so hashing
// one provider does not hold up deadline access for all providers. Blocked
// reacquires the mutex and reads the current map afterward, allowing a
// concurrent Clear or Block to determine its result.
func (b *RefreshFailureBlocks) Blocked(id int64, now time.Time, identity func() string) bool {
	if b == nil {
		return false
	}
	// Remove expired deadlines before invoking the credential callback so empty
	// providers need no hash and stale versions cannot affect the final lookup.
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
	// Hash outside the shared mutex, then reread the current map under the lock.
	// Clear may remove these entries while hashing is in progress.
	key := identity()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.entries[id][key].After(now)
}

// Clear removes every credential-version deadline for the selected provider
// under the mutex. A nil receiver is accepted; an in-progress Blocked callback
// rechecks this cleared map before reporting a result.
func (b *RefreshFailureBlocks) Clear(id int64) {
	if b == nil {
		return
	}
	// Recovery clears every attempted version together; a hashing reader will
	// consult this current map after its callback instead of an earlier snapshot.
	b.mu.Lock()
	delete(b.entries, id)
	b.mu.Unlock()
}
