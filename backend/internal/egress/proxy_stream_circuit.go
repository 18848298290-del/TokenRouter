package egress

import (
	"sync"
	"time"
)

// proxy_stream_circuit.go counts proxy stream failures and manages timed
// quarantine for scheduling decisions.
// OpenAI stream failures can quarantine a proxy after enough independent
// observations arrive within a window. A shared multiplexed connection may
// report a burst of failures, so a configured collapse interval counts the
// burst from the last retained failure as one observation.
//
// The circuit serializes observation, success reset, and block checks under one
// mutex. Quarantine uses an exclusive deadline and counts active entries for
// the scheduler's fail-open decision. Capacity cleanup first removes stale
// observations and expired quarantines, then evicts the least recently touched
// remaining proxy if needed. This bound can remove an active quarantine, and
// the circuit records in-memory state for this instance only.

// Stream circuit defaults govern observation thresholds, quarantine duration,
// burst collapse, and the bounded set of retained proxy identities.
const (
	defaultOpenAIProxyStreamFailureThreshold  = 2
	defaultOpenAIProxyStreamFailureWindow     = time.Minute
	defaultOpenAIProxyStreamQuarantineTTL     = 10 * time.Minute
	defaultOpenAIProxyStreamCircuitMaxEntries = 4096
	// A failed proxy connection can interrupt multiplexed streams together.
	// The production collapse duration counts that nearby burst as one retained observation.
	defaultOpenAIProxyStreamFailureCollapse = 3 * time.Second
)

// ProxyStreamCircuitSettings configures per-proxy failure windows, quarantine, and capacity.
//
// FailureThreshold, FailureWindow, QuarantineTTL, and MaxEntries select
// defaults when nonpositive. CollapseInterval is normalized only when
// negative; a zero value keeps collapse disabled, while
// DefaultProxyStreamCircuitSettings supplies the production duration.
type ProxyStreamCircuitSettings struct {
	Disabled         bool          // failure recording bypass flag
	FailureThreshold int           // counted failure activation threshold
	FailureWindow    time.Duration // failure accumulation window duration
	QuarantineTTL    time.Duration // activated proxy block duration
	CollapseInterval time.Duration // nearby failure collapse duration
	MaxEntries       int           // retained proxy identity limit
}

// openAIProxyStreamCircuitEntry retains a proxy's counted failures, quarantine
// deadline, and capacity-recency observations.
type openAIProxyStreamCircuitEntry struct {
	// windowStart and failureCount track observations, while lastFailureAt
	// controls burst collapse. blockedUntil controls quarantine; lastTouched
	// records failure access for capacity eviction, and read checks do not advance
	// it.
	failureCount  int       // retained observation window count
	windowStart   time.Time // active observation window start
	lastFailureAt time.Time // latest counted failure time
	blockedUntil  time.Time // exclusive proxy block deadline
	lastTouched   time.Time // capacity eviction recency timestamp
}

// ProxyStreamCircuit stores bounded proxy failure observations and quarantine
// deadlines for scheduler selection.
type ProxyStreamCircuit struct {
	// mu protects entries during failure recording, reset, block lookup, and
	// capacity eviction. settings is normalized once during construction, then
	// read by each operation without later mutation.
	mu       sync.Mutex                              // observation and admission lock
	settings ProxyStreamCircuitSettings              // normalized immutable circuit options
	entries  map[int64]openAIProxyStreamCircuitEntry // proxy-ID observation state map
}

// NewProxyStreamCircuit constructs empty circuit state using normalized
// threshold, window, quarantine, and capacity defaults. Negative
// CollapseInterval becomes zero, but zero itself disables burst collapse; use
// DefaultProxyStreamCircuitSettings for the production collapse duration.
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

// RecordFailure records a proxy observation and returns whether this call
// activates quarantine plus the retained block deadline. Nil or disabled
// circuits and nonpositive IDs return false with zero time. An already blocked
// proxy retains its deadline and updates eviction recency.
//
// The lock covers window resets, burst collapse, threshold counting, and
// capacity eviction. Clock regression restarts the observation window.
// Collapsed failures update recency but leave the last counted failure
// timestamp unchanged; later independent observations can therefore activate
// quarantine. A new ID can evict the oldest retained entry when stale cleanup
// does not create capacity.
func (c *ProxyStreamCircuit) RecordFailure(proxyID int64, now time.Time) (bool, time.Time) {
	if c == nil || c.settings.Disabled || proxyID <= 0 {
		return false, time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.entries[proxyID]
	// Failures during quarantine retain its deadline but refresh eviction recency,
	// so capacity cleanup measures the latest failure access to this proxy.
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
	// Count burst timing from the last retained failure, not each collapsed
	// arrival. This permits a later independent failure to cross the threshold
	// after many multiplexed streams fail near the same connection event.
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

// RecordSuccess removes all retained failure and quarantine state for a
// positive proxy ID. It reports whether an entry existed, including when
// recording is disabled; nil circuits and nonpositive IDs return false.
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

// IsBlocked reports whether a proxy has a quarantine deadline later than now.
// Reached deadlines delete their entries under the circuit lock; nil or
// disabled circuits and invalid IDs return false without lookup.
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
	// Remove reached quarantines here so a later failure starts from an empty
	// entry rather than retaining the count that activated the expired block.
	if !now.Before(entry.blockedUntil) {
		delete(c.entries, proxyID)
		return false
	}
	return true
}

// ActiveBlockCount counts future quarantine deadlines for the scheduler's
// fail-open decision. It returns zero for nil or disabled circuits and leaves
// expired entries available for later lookup or capacity cleanup to remove.
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

// ensureCapacityLocked makes room for one new proxy entry when the retained map
// reaches MaxEntries. The caller holds the circuit mutex before invoking it.
// Stale unblocked observations and reached quarantine deadlines are deleted
// first.
//
// If the map remains full, the least recently touched proxy is evicted,
// including an active quarantine. Recency is updated by failure observations
// rather than block reads. The caller inserts its new entry before releasing
// the same lock, so concurrent new IDs cannot independently consume the space
// made here.
func (c *ProxyStreamCircuit) ensureCapacityLocked(now time.Time) {
	if len(c.entries) < c.settings.MaxEntries {
		return
	}
	// Remove observations whose last failure access has aged beyond the window,
	// plus reached quarantine deadlines, before selecting a still-live eviction target.
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
	// A full map still needs one slot for the incoming proxy. Evict by last
	// failure access even when that oldest entry retains an active quarantine.
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
