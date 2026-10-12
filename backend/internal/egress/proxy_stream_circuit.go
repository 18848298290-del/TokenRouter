package egress

import (
	"sync"
	"time"
)

const (
	defaultOpenAIProxyStreamFailureThreshold  = 2
	defaultOpenAIProxyStreamFailureWindow     = time.Minute
	defaultOpenAIProxyStreamQuarantineTTL     = 10 * time.Minute
	defaultOpenAIProxyStreamCircuitMaxEntries = 4096
	// A failed proxy connection can interrupt many multiplexed streams; collapse bursts into one observation.
	defaultOpenAIProxyStreamFailureCollapse = 3 * time.Second
)

// ProxyStreamCircuitSettings configures per-proxy failure windows, quarantine, and capacity.
type ProxyStreamCircuitSettings struct {
	Disabled         bool
	FailureThreshold int
	FailureWindow    time.Duration
	QuarantineTTL    time.Duration
	CollapseInterval time.Duration
	MaxEntries       int
}

type openAIProxyStreamCircuitEntry struct {
	failureCount  int
	windowStart   time.Time
	lastFailureAt time.Time
	blockedUntil  time.Time
	lastTouched   time.Time
}

// ProxyStreamCircuit tracks a bounded set of quarantined proxy IDs in memory.
// Restarting clears observations, and quarantine expires at its TTL.
type ProxyStreamCircuit struct {
	mu       sync.Mutex
	settings ProxyStreamCircuitSettings
	entries  map[int64]openAIProxyStreamCircuitEntry
}

// NewProxyStreamCircuit allocates bounded circuit state without starting background work.
func NewProxyStreamCircuit(settings ProxyStreamCircuitSettings) *ProxyStreamCircuit {
	if settings.FailureThreshold <= 0 {
		settings.FailureThreshold = defaultOpenAIProxyStreamFailureThreshold
	}
	if settings.FailureWindow <= 0 {
		settings.FailureWindow = defaultOpenAIProxyStreamFailureWindow
	}
	if settings.QuarantineTTL <= 0 {
		settings.QuarantineTTL = defaultOpenAIProxyStreamQuarantineTTL
	}
	if settings.MaxEntries <= 0 {
		settings.MaxEntries = defaultOpenAIProxyStreamCircuitMaxEntries
	}
	if settings.CollapseInterval < 0 {
		settings.CollapseInterval = 0
	}
	return &ProxyStreamCircuit{
		settings: settings,
		entries:  make(map[int64]openAIProxyStreamCircuitEntry),
	}
}

func (c *ProxyStreamCircuit) RecordFailure(proxyID int64, now time.Time) (bool, time.Time) {
	if c == nil || c.settings.Disabled || proxyID <= 0 {
		return false, time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.entries[proxyID]
	if exists && now.Before(entry.blockedUntil) {
		entry.lastTouched = now
		c.entries[proxyID] = entry
		return false, entry.blockedUntil
	}
	if !exists {
		c.ensureCapacityLocked(now)
	}
	if entry.windowStart.IsZero() || now.Before(entry.windowStart) || now.Sub(entry.windowStart) > c.settings.FailureWindow {
		entry.failureCount = 0
		entry.windowStart = now
		entry.blockedUntil = time.Time{}
	}
	// Collapse concurrent failures from a shared connection into one proxy failure event.
	if c.settings.CollapseInterval > 0 && !entry.lastFailureAt.IsZero() &&
		now.Sub(entry.lastFailureAt) >= 0 && now.Sub(entry.lastFailureAt) < c.settings.CollapseInterval {
		entry.lastTouched = now
		c.entries[proxyID] = entry
		return false, time.Time{}
	}
	entry.failureCount++
	entry.lastFailureAt = now
	entry.lastTouched = now
	tripped := entry.failureCount >= c.settings.FailureThreshold
	if tripped {
		entry.blockedUntil = now.Add(c.settings.QuarantineTTL)
	}
	c.entries[proxyID] = entry
	return tripped, entry.blockedUntil
}

func (c *ProxyStreamCircuit) RecordSuccess(proxyID int64) bool {
	if c == nil || proxyID <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[proxyID]; !ok {
		return false
	}
	delete(c.entries, proxyID)
	return true
}

func (c *ProxyStreamCircuit) IsBlocked(proxyID int64, now time.Time) bool {
	if c == nil || c.settings.Disabled || proxyID <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[proxyID]
	if !ok || entry.blockedUntil.IsZero() {
		return false
	}
	if !now.Before(entry.blockedUntil) {
		delete(c.entries, proxyID)
		return false
	}
	return true
}

// ActiveBlockCount counts quarantined proxies for the scheduler's fail-open decision.
func (c *ProxyStreamCircuit) ActiveBlockCount(now time.Time) int {
	if c == nil || c.settings.Disabled {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, entry := range c.entries {
		if !entry.blockedUntil.IsZero() && now.Before(entry.blockedUntil) {
			count++
		}
	}
	return count
}

func (c *ProxyStreamCircuit) ensureCapacityLocked(now time.Time) {
	if len(c.entries) < c.settings.MaxEntries {
		return
	}
	for proxyID, entry := range c.entries {
		staleObservation := entry.blockedUntil.IsZero() && now.Sub(entry.lastTouched) > c.settings.FailureWindow
		expiredQuarantine := !entry.blockedUntil.IsZero() && !now.Before(entry.blockedUntil)
		if staleObservation || expiredQuarantine {
			delete(c.entries, proxyID)
		}
	}
	if len(c.entries) < c.settings.MaxEntries {
		return
	}
	var oldestProxyID int64
	var oldest time.Time
	for proxyID, entry := range c.entries {
		if oldestProxyID == 0 || entry.lastTouched.Before(oldest) {
			oldestProxyID = proxyID
			oldest = entry.lastTouched
		}
	}
	if oldestProxyID > 0 {
		delete(c.entries, oldestProxyID)
	}
}

// DefaultProxyStreamCircuitSettings returns production defaults, including a three-second collapse interval.
func DefaultProxyStreamCircuitSettings() ProxyStreamCircuitSettings {
	return ProxyStreamCircuitSettings{FailureThreshold: defaultOpenAIProxyStreamFailureThreshold, FailureWindow: defaultOpenAIProxyStreamFailureWindow, QuarantineTTL: defaultOpenAIProxyStreamQuarantineTTL, CollapseInterval: defaultOpenAIProxyStreamFailureCollapse, MaxEntries: defaultOpenAIProxyStreamCircuitMaxEntries}
}
