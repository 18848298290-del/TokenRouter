package provider

import (
	"sync"
	"time"
)

// WriteThrottle limits snapshot write attempts per provider.
type WriteThrottle struct {
	minInterval time.Duration
	mu          sync.Mutex
	lastByID    map[int64]time.Time
}

func NewWriteThrottle(minInterval time.Duration) *WriteThrottle {
	return &WriteThrottle{
		minInterval: minInterval,
		lastByID:    make(map[int64]time.Time),
	}
}

// Allow reserves the current interval when it returns true, even if the caller
// later fails to write. A nil throttle or nonpositive ID or interval allows every call.
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
