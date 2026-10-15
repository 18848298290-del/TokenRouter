package provider

import (
	"sync"
	"time"
)

// write_throttle.go limits provider snapshot write attempts through
// synchronized per-provider admission timestamps.
// Provider snapshot writers can attempt several observations for one provider
// inside a short interval. Admission reserves the interval before persistence,
// so a failed write still counts as an attempt and a burst cannot immediately
// repeat that provider's write.
//
// A mutex serializes admission and history updates per throttle instance.
// Provider IDs have independent timestamps, and occasional pruning removes old
// entries once the retained table grows; this history is local to the process
// and the caller supplies each observation time. A new instance starts with
// empty history.

// WriteThrottle limits snapshot write attempts per provider using admitted timestamps.
type WriteThrottle struct {
	minInterval time.Duration // minimum time between admitted attempts
	// mu protects each timestamp comparison, reservation, and pruning pass.
	// Separate instances do not coordinate their history for the same provider.
	mu sync.Mutex
	// lastByID records admitted attempts, including later failed writes.
	// Allow occasionally removes old entries when the retained history grows.
	lastByID map[int64]time.Time
}

// NewWriteThrottle constructs a process-local attempt limiter with the supplied
// minimum interval. A nonpositive interval disables reservations, and callers
// can also use a nil throttle for unconditional admission.
func NewWriteThrottle(minInterval time.Duration) *WriteThrottle {
	return &WriteThrottle{
		minInterval: minInterval,
		lastByID:    make(map[int64]time.Time),
	}
}

// Allow reports whether this provider can reserve a write attempt at now. Nil
// throttles, nonpositive IDs, and nonpositive intervals admit calls
// immediately.
//
// For positive inputs the mutex checks and updates the provider's last admitted
// timestamp together, so concurrent calls at the same time reserve one
// interval. A successful reservation remains even if persistence fails. A
// supplied time earlier than the previous reservation is also inside the
// blocked interval, so callers provide a consistent clock for useful timing.
func (t *WriteThrottle) Allow(id int64, now time.Time) bool {
	if t == nil || id <= 0 || t.minInterval <= 0 {
		return true
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if last, ok := t.lastByID[id]; ok && now.Sub(last) < t.minInterval {
		return false
	}
	t.lastByID[id] = now

	// Prune only after admission and only once history grows beyond this bound.
	// Four intervals retain recent reservations while removing inactive provider
	// IDs that no longer need to block another attempt.
	if len(t.lastByID) > 4096 {
		cutoff := now.Add(-4 * t.minInterval)
		for providerID, writtenAt := range t.lastByID {
			if writtenAt.Before(cutoff) {
				delete(t.lastByID, providerID)
			}
		}
	}

	return true
}
