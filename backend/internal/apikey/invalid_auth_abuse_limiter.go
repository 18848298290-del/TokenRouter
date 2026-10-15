package apikey

import (
	"sync"
	"sync/atomic"
	"time"
)

// invalid_auth_abuse_limiter.go tracks invalid credential failures and
// temporarily blocks clients within a fixed tracking capacity. Existing
// clients retain their own temporary blocks, while failures from new clients
// at capacity share an overflow bucket. That choice bounds retained client
// entries during random-credential scans while allowing one busy overflow
// population to affect other untracked clients.
//
// Each shard mutex protects its entries and failure windows; an atomic
// reservation count keeps concurrent insertions within total capacity. At
// capacity, a timed compare-and-swap permits one rotating shard cleanup scan,
// reclaiming expired entries without scanning every shard per request.
// Overflow counters use their own mutex. Health counters are sampled
// independently, and the bound clock callback is configured before concurrent
// checks or failure recording.

// KeyInvalidAuthAbuseShardCount spreads client identities across lock-protected
// maps while total capacity and cleanup scheduling remain shared.
const KeyInvalidAuthAbuseShardCount = 16

// KeyInvalidAuthAbuseEntry records one tracked client's failure window and
// block expiry.
type KeyInvalidAuthAbuseEntry struct {
	// failures counts events from windowStart until the threshold starts a block
	// ending at blockedUntil. Blocking resets failures and moves windowStart to
	// blockedUntil, retaining the entry through the following failure window.
	failures     int       // current window failure count
	windowStart  time.Time // current failure window start
	blockedUntil time.Time // temporary block end timestamp
}

// KeyInvalidAuthAbuseShard protects a subset of client entries and their
// mutations.
type KeyInvalidAuthAbuseShard struct {
	// mu protects entries and each entry's mutable failure times and counts.
	// Callers hold this shard lock through insertion, expiration and tracked recording.
	mu      sync.Mutex                           // shared mutation serialization lock
	entries map[string]*KeyInvalidAuthAbuseEntry // client identity tracking records
}

// KeyInvalidAuthOverflow protects the shared failure window for clients beyond
// tracking capacity.
type KeyInvalidAuthOverflow struct {
	// mu protects failures, windowStart and blockedUntil for all untracked
	// clients. Overflow recording and admission checks read or mutate them under
	// this same lock after the selected shard has been released.
	mu           sync.Mutex // shared mutation serialization lock
	failures     int        // current window failure count
	windowStart  time.Time  // current failure window start
	blockedUntil time.Time  // temporary block end timestamp
}

// KeyInvalidAuthAbuseLimiter bounds client entries and applies tracked or
// overflow failure blocks.
type KeyInvalidAuthAbuseLimiter struct {
	// threshold, window, block, capacity and now are captured configuration.
	// Configure the clock before concurrent use; the limiter never synchronizes
	// replacement of that callback with ongoing checks or failure recording.
	threshold int                                                     // failures before temporary blocking
	window    time.Duration                                           // tracked failure window duration
	block     time.Duration                                           // temporary admission block duration
	capacity  int64                                                   // maximum individually tracked clients
	shards    [KeyInvalidAuthAbuseShardCount]KeyInvalidAuthAbuseShard // lock-separated tracked client maps
	overflow  KeyInvalidAuthOverflow                                  // shared untracked-client failure bucket
	// now is captured when constructing the limiter. Callers configure a
	// concurrency-safe callback before other goroutines begin recording or checking clients.
	now func() time.Time

	// tracked reserves total entry capacity across shards, while cleanupNext
	// and cleanupCursor coordinate which shard can reclaim ended windows. The
	// remaining atomics count events independently for health observation.
	tracked       atomic.Int64  // currently reserved tracking entries
	recorded      atomic.Uint64 // total nonempty failures recorded
	blocked       atomic.Uint64 // total temporary blocks started
	rejected      atomic.Uint64 // total admission checks rejected
	expired       atomic.Uint64 // total tracked entries reclaimed
	overflowed    atomic.Uint64 // total failures beyond capacity
	globalBlocked atomic.Uint64 // total overflow check rejections
	cleanupNext   atomic.Int64  // next cleanup UTC nanoseconds
	cleanupCursor atomic.Uint32 // rotating shard cleanup sequence
}

// InvalidAuthAbuseHealth reports independently sampled tracking, block and
// cleanup counters.
type InvalidAuthAbuseHealth struct {
	Enabled       bool   `json:"enabled"`        // limiter configuration enablement observation
	Tracked       int64  `json:"tracked"`        // observed retained client count
	Capacity      int64  `json:"capacity"`       // configured retained client maximum
	Recorded      uint64 `json:"recorded"`       // observed nonempty failure count
	Blocks        uint64 `json:"blocks"`         // observed temporary block count
	Rejected      uint64 `json:"rejected"`       // observed rejected check count
	Expired       uint64 `json:"expired"`        // observed reclaimed entry count
	Overflowed    uint64 `json:"overflowed"`     // observed overflow failure count
	GlobalBlocked uint64 `json:"global_blocked"` // observed overflow rejection count
}

// KeyNewInvalidAuthAbuseLimiter captures enabled positive abuse settings and
// initializes its client shards. Nil Options, disabled tracking or any
// nonpositive setting returns nil; an absent Now callback selects time.Now for
// later checks.
func KeyNewInvalidAuthAbuseLimiter(cfg *Options) *KeyInvalidAuthAbuseLimiter {
	if cfg == nil || !cfg.APIKeyAuth.InvalidAbuse.Enabled {
		return nil
	}
	c := cfg.APIKeyAuth.InvalidAbuse
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	if c.Threshold <= 0 || c.WindowSeconds <= 0 || c.BlockSeconds <= 0 || c.Capacity <= 0 {
		return nil
	}
	l := &KeyInvalidAuthAbuseLimiter{
		threshold: c.Threshold,
		// Capture configured seconds as durations for later block and window checks.
		window:   time.Duration(c.WindowSeconds) * time.Second,
		block:    time.Duration(c.BlockSeconds) * time.Second,
		capacity: int64(c.Capacity),
		now:      now,
	}
	for i := range l.shards {
		l.shards[i].entries = make(map[string]*KeyInvalidAuthAbuseEntry)
	}
	return l
}

// CheckInvalidAuthAbuse reports remaining block duration and whether clientKey
// is blocked. Nil services or disabled limiters return no block; the limiter
// may reclaim expired tracked entries while performing the check.
func (s *APIKeyService) CheckInvalidAuthAbuse(clientKey string) (time.Duration, bool) {
	if s == nil || s.invalidAuthAbuse == nil {
		return 0, false
	}
	return s.invalidAuthAbuse.KeyCheck(clientKey)
}

// RecordInvalidAuthFailure records one failure for clientKey through the
// configured bounded limiter. Nil services and disabled limiters do nothing;
// callers choose a stable client identity independently of the rejected
// credential.
func (s *APIKeyService) RecordInvalidAuthFailure(clientKey string) {
	if s == nil || s.invalidAuthAbuse == nil {
		return
	}
	s.invalidAuthAbuse.KeyRecord(clientKey)
}

// InvalidAuthAbuseHealth returns the configured limiter's independently sampled
// observation counters. Nil services and disabled limiters return zero health,
// including Enabled false, so callers can distinguish configuration from a
// running tracker.
func (s *APIKeyService) InvalidAuthAbuseHealth() InvalidAuthAbuseHealth {
	if s == nil || s.invalidAuthAbuse == nil {
		return InvalidAuthAbuseHealth{}
	}
	return s.invalidAuthAbuse.KeyHealth()
}

// KeyCheck reports remaining retry duration for a current client or overflow
// block. Nil limiters and empty client identities return no block.
//
// Expired tracked entries are removed under their shard lock before checking
// the client's block time. A tracked client that is not blocked is spared
// overflow rejection; an untracked client consults the shared bucket when
// sampled capacity is full. These checks update rejection and reclamation
// counters, while concurrent capacity changes can alter which bucket a later
// call observes.
func (l *KeyInvalidAuthAbuseLimiter) KeyCheck(clientKey string) (time.Duration, bool) {
	if l == nil || clientKey == "" {
		return 0, false
	}
	now := l.now()
	l.KeyMaybeCleanupAtCapacity(now)
	KeyShard := l.KeyShard(clientKey)
	KeyShard.mu.Lock()
	entry := KeyShard.entries[clientKey]
	if entry != nil && l.KeyEntryExpired(entry, now) {
		// Both the block and its following failure window have ended. Remove
		// the client entry and return its shared capacity before another insert.
		delete(KeyShard.entries, clientKey)
		l.tracked.Add(-1)
		l.expired.Add(1)
		entry = nil
	}
	if entry != nil && entry.blockedUntil.After(now) {
		retry := entry.blockedUntil.Sub(now)
		KeyShard.mu.Unlock()
		l.rejected.Add(1)
		return retry, true
	}
	KeyShard.mu.Unlock()

	if entry == nil && l.tracked.Load() >= l.capacity {
		if retry, blocked := l.KeyCheckOverflow(now); blocked {
			l.rejected.Add(1)
			l.globalBlocked.Add(1)
			return retry, true
		}
	}
	return 0, false
}

// KeyRecord records one nonempty client failure and may start its temporary
// block. Nil limiters and empty identities do nothing, and already blocked
// clients retain their existing block deadline.
//
// After removing an expired entry under its shard lock, a new tracked client
// reserves global capacity atomically. Failure to reserve directs this failure
// to the overflow bucket after releasing the shard lock. A new or elapsed
// or future-started window resets its count; reaching threshold moves windowStart to blockedUntil
// so the next failure window follows the completed block.
func (l *KeyInvalidAuthAbuseLimiter) KeyRecord(clientKey string) {
	if l == nil || clientKey == "" {
		return
	}
	l.recorded.Add(1)
	now := l.now()
	l.KeyMaybeCleanupAtCapacity(now)
	KeyShard := l.KeyShard(clientKey)
	KeyShard.mu.Lock()
	entry := KeyShard.entries[clientKey]
	if entry != nil && l.KeyEntryExpired(entry, now) {
		// Both the block and its following failure window have ended. Remove
		// the client entry and return its shared capacity before another insert.
		delete(KeyShard.entries, clientKey)
		l.tracked.Add(-1)
		l.expired.Add(1)
		entry = nil
	}
	if entry == nil {
		if !l.KeyReserveEntry() {
			// Overflow recording takes its own mutex. Release the shard lock
			// first so full-capacity clients do not hold unrelated tracked clients waiting.
			KeyShard.mu.Unlock()
			l.KeyRecordOverflow(now)
			return
		}
		// Global capacity was reserved before publishing this entry in its
		// shard, so concurrent clients cannot exceed the retained-entry limit across maps.
		entry = &KeyInvalidAuthAbuseEntry{windowStart: now}
		KeyShard.entries[clientKey] = entry
	}
	if entry.blockedUntil.After(now) {
		KeyShard.mu.Unlock()
		return
	}
	if entry.windowStart.After(now) || !now.Before(entry.windowStart.Add(l.window)) {
		entry.windowStart = now
		entry.failures = 0
	}
	entry.failures++
	if entry.failures >= l.threshold {
		entry.failures = 0
		entry.blockedUntil = now.Add(l.block)
		// Start the next failure window at block completion, preventing failures
		// after unblock from being counted against the window that caused this block.
		entry.windowStart = entry.blockedUntil
		l.blocked.Add(1)
	}
	KeyShard.mu.Unlock()
}

// KeyReserveEntry atomically reserves one tracked-client position when sampled
// capacity is below its limit. Competing inserts retry the compare-and-swap; a
// full sample returns false, and successful callers later release the count
// when removing an entry.
//
// Reservation precedes publication in the owning shard. The count therefore
// includes capacity claimed by other shards before their entry becomes visible,
// preventing simultaneous inserts from each consuming the same remaining slot.
func (l *KeyInvalidAuthAbuseLimiter) KeyReserveEntry() bool {
	for {
		current := l.tracked.Load()
		if current >= l.capacity {
			return false
		}
		// Another shard can reserve capacity after this sample. Retry a lost CAS
		// against the new count before deciding whether this client uses overflow.
		if l.tracked.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

// KeyMaybeCleanupAtCapacity scans one rotating shard when tracking has reached
// its total limit. Calls below capacity do nothing.
//
// A shared nanosecond deadline and compare-and-swap grant at most one cleanup
// opportunity per 100 milliseconds of supplied time. The winning call selects
// the next shard and removes entries whose block and failure window have both
// ended under its lock. Reclaimed counts become available to later inserts,
// while unscanned shards retain their entries until their own cleanup or direct
// client access.
func (l *KeyInvalidAuthAbuseLimiter) KeyMaybeCleanupAtCapacity(now time.Time) {
	if l.tracked.Load() < l.capacity {
		return
	}
	nowUnixNano := now.UnixNano()
	for {
		next := l.cleanupNext.Load()
		if nowUnixNano < next {
			return
		}
		// Claim this cleanup opportunity before choosing the rotating shard
		// so concurrent capacity checks cannot each scan a shard in the same interval.
		if l.cleanupNext.CompareAndSwap(next, now.Add(100*time.Millisecond).UnixNano()) {
			break
		}
	}
	index := l.cleanupCursor.Add(1) - 1
	KeyShard := &l.shards[index%KeyInvalidAuthAbuseShardCount]
	KeyShard.mu.Lock()
	// Remove entries under their owning shard lock before returning reserved
	// capacity, keeping direct client access from observing state already reclaimed here.
	for key, entry := range KeyShard.entries {
		if l.KeyEntryExpired(entry, now) {
			delete(KeyShard.entries, key)
			l.tracked.Add(-1)
			l.expired.Add(1)
		}
	}
	KeyShard.mu.Unlock()
}

// KeyEntryExpired reports whether both an entry's block and failure window have
// ended. Nil entries return false, future window starts stay retained, and
// equality at the elapsed window is eligible for removal.
//
// Blocking moves windowStart to the block end. Expiring the block alone therefore
// permits admission while retaining the tracked entry until its following
// failure window also ends, so cleanup and direct access use the same retention
// decision.
func (l *KeyInvalidAuthAbuseLimiter) KeyEntryExpired(entry *KeyInvalidAuthAbuseEntry, now time.Time) bool {
	return entry != nil && !entry.blockedUntil.After(now) && !entry.windowStart.After(now) && !now.Before(entry.windowStart.Add(l.window))
}

// KeyShard selects the client's shard using a byte-based FNV-1a hash.
func (l *KeyInvalidAuthAbuseLimiter) KeyShard(clientKey string) *KeyInvalidAuthAbuseShard {
	// FNV-1a seed and prime distribute the supplied client bytes deterministically.
	const fnvOffset32 = uint32(2166136261)
	const fnvPrime32 = uint32(16777619)
	hash := fnvOffset32
	for i := range len(clientKey) {
		hash ^= uint32(clientKey[i])
		hash *= fnvPrime32
	}
	return &l.shards[hash%KeyInvalidAuthAbuseShardCount]
}

// KeyRecordOverflow counts one failure redirected from a full tracker and
// updates the shared block window. Existing blocks retain their deadline;
// elapsed windows reset, and reaching threshold moves the window start to the
// block end.
//
// All untracked clients contribute to these same counters, so one client's
// failure can start a block observed by another. Every call increments the
// overflow health observation, including calls during a block that leave its
// failure count and deadline unchanged.
func (l *KeyInvalidAuthAbuseLimiter) KeyRecordOverflow(now time.Time) {
	l.overflowed.Add(1)
	l.overflow.mu.Lock()
	defer l.overflow.mu.Unlock()
	if l.overflow.blockedUntil.After(now) {
		return
	}
	if l.overflow.windowStart.IsZero() || !now.Before(l.overflow.windowStart.Add(l.window)) {
		l.overflow.windowStart = now
		l.overflow.failures = 0
	}
	l.overflow.failures++
	if l.overflow.failures >= l.threshold {
		l.overflow.failures = 0
		l.overflow.blockedUntil = now.Add(l.block)
		// The shared failure window follows its completed block, giving newly
		// observed overflow clients a fresh threshold after that block has ended.
		l.overflow.windowStart = l.overflow.blockedUntil
		l.blocked.Add(1)
	}
}

// KeyCheckOverflow reports the remaining shared block duration under its mutex.
// An absent or elapsed block returns false with zero duration; the check
// retains overflow counters and timestamps for subsequent recording.
func (l *KeyInvalidAuthAbuseLimiter) KeyCheckOverflow(now time.Time) (time.Duration, bool) {
	l.overflow.mu.Lock()
	defer l.overflow.mu.Unlock()
	if l.overflow.blockedUntil.After(now) {
		return l.overflow.blockedUntil.Sub(now), true
	}
	return 0, false
}

// KeyHealth returns Enabled true with separately loaded tracking and event
// counters. Concurrent callers can change values between loads, so the result
// is an observation rather than one transactional snapshot of the tracker.
func (l *KeyInvalidAuthAbuseLimiter) KeyHealth() InvalidAuthAbuseHealth {
	return InvalidAuthAbuseHealth{
		Enabled:       true,
		Tracked:       l.tracked.Load(),
		Capacity:      l.capacity,
		Recorded:      l.recorded.Load(),
		Blocks:        l.blocked.Load(),
		Rejected:      l.rejected.Load(),
		Expired:       l.expired.Load(),
		Overflowed:    l.overflowed.Load(),
		GlobalBlocked: l.globalBlocked.Load(),
	}
}
