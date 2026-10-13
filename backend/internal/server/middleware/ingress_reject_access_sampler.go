package middleware

import (
	"sync"
	"time"
)

// An admission-rejection storm can create more access events than the logger
// should emit. One process-wide sampler spends a fixed budget across all
// requests, regardless of client address or API key, while the aggregate
// rejection recorder still sees each marked request before sampling.
//
// The sampler keeps its window counters and dropped-summary state under one
// mutex. A new, elapsed, or backward-moving window resets the emission count;
// skipped events accumulate across windows until a rejected call can publish
// their summary. Summary timing is independent of the emission window, and
// accepted events do not trigger a summary. This state stays constant in size
// as request identities vary.

const (
	// These values bound process-wide rejection access events and set a separate
	// reporting interval for the number of events skipped by sampling.
	ingressRejectAccessLogLimit       = 20
	ingressRejectAccessLogWindow      = time.Second
	ingressRejectDroppedSummaryPeriod = 30 * time.Second
)

// globalIngressRejectAccessSampler shares one rejection access-log budget and
// dropped-event summary schedule across every Logger middleware instance.
var globalIngressRejectAccessSampler = newIngressRejectAccessSampler(
	ingressRejectAccessLogLimit,
	ingressRejectAccessLogWindow,
	ingressRejectDroppedSummaryPeriod,
)

// ingressRejectAccessSampler coordinates rejection access admission and
// occasional dropped-event summaries under one mutex.
type ingressRejectAccessSampler struct {
	// mu protects windowStart, emitted, dropped, and lastSummary as one decision
	// state. allow reads and updates them under the lock so concurrent requests
	// share the same budget and summary counts.
	mu sync.Mutex // sampler decision mutex
	// limit caps emitted access events within window. A nonpositive value disables
	// admission and dropped-summary updates for this sampler.
	limit int
	// window sets the access budget's duration. A nonpositive duration disables
	// sampling decisions, and a backward clock starts a fresh window.
	window time.Duration
	// summaryPeriod spaces dropped-count reports independently of window.
	// Nonpositive values accumulate skipped events while suppressing summary
	// publication.
	summaryPeriod time.Duration
	windowStart   time.Time // emission window start
	emitted       int       // current window emissions
	dropped       uint64    // skips since last summary
	lastSummary   time.Time // last summary publication
}

// newIngressRejectAccessSampler constructs an unused sampler with the supplied
// emission and summary settings. Nonpositive limits or windows make allow
// reject silently; a nonpositive summary period suppresses summaries while
// skipped events accumulate.
func newIngressRejectAccessSampler(limit int, window, summaryPeriod time.Duration) *ingressRejectAccessSampler {
	return &ingressRejectAccessSampler{limit: limit, window: window, summaryPeriod: summaryPeriod}
}

// allow spends one access-event slot or returns the number of dropped events
// due for a summary. Nil samplers and nonpositive limits or windows return
// false and zero. Concurrent calls use the same locked decision state.
//
// A missing, elapsed, or backward-moving emission window resets its spent
// slots. Calls beyond the budget increment dropped; only such calls can publish
// a summary when its separate interval is due, then clear the published count.
// Accepted calls return zero summary, and nonpositive summary periods leave
// skipped counts accumulated.
func (s *ingressRejectAccessSampler) allow(now time.Time) (allowed bool, droppedSummary uint64) {
	if s == nil || s.limit <= 0 || s.window <= 0 {
		return false, 0
	}
	// Admission and dropped reporting consume the same state. Hold the mutex
	// across both decisions so competing requests cannot spend a slot or publish a
	// count twice.
	s.mu.Lock()
	defer s.mu.Unlock()
	// Restart the budget when elapsed time crosses the window or moves backward.
	// Reset emission state alone so skipped-event summaries retain their
	// independent schedule.
	if s.windowStart.IsZero() || now.Sub(s.windowStart) >= s.window || now.Before(s.windowStart) {
		s.windowStart = now
		s.emitted = 0
	}
	if s.emitted < s.limit {
		s.emitted++
		return true, 0
	}
	s.dropped++
	// A rejected call publishes accumulated skips when the summary interval is
	// due. Clear that count under the same lock before later calls can report it.
	if s.summaryPeriod > 0 && (s.lastSummary.IsZero() || now.Sub(s.lastSummary) >= s.summaryPeriod) {
		droppedSummary = s.dropped
		s.dropped = 0
		s.lastSummary = now
	}
	return false, droppedSummary
}
