package ops

import (
	"fmt"
	"time"
)

// cleanup_policy.go translates retention days into cleanup actions and formats
// completed deletion counts.
// Retention values distinguish skipping a target, truncating its whole table,
// and deleting observations before a calendar-day cutoff. Cleanup summaries
// retain only successfully returned target counts, which can be estimates for
// truncation, and format throttling separately from those row counts.

// Cleanup time budgets limit a schedule-rebuild drain wait and the contexts for
// a scheduled run and detached heartbeat write. Stop still joins retired cron
// jobs after the short local drain wait expires.
const (
	opsCleanupCronStopTimeout  = 3 * time.Second  // local cron retirement wait
	opsCleanupRunTimeout       = 30 * time.Minute // scheduled cleanup context budget
	opsCleanupHeartbeatTimeout = 2 * time.Second  // detached heartbeat context budget
)

// opsCleanupTarget binds one retention decision to a trusted table and its
// result counter.
type opsCleanupTarget struct {
	// retentionDays selects opsCleanupPlan behavior: negative values skip the
	// target, zero truncates it, and positive values delete rows before a
	// calendar-day cutoff.
	retentionDays int // configured calendar retention days
	// table and timeCol are interpolated as SQL identifiers by the PostgreSQL
	// cleanup backend. runCleanupOnce supplies fixed internal names, so callers
	// constructing targets need trusted identifiers rather than request text.
	table   string // trusted cleanup table identifier
	timeCol string // trusted retention column identifier
	// castDate asks the PostgreSQL cleanup query to cast its cutoff parameter to
	// date. The daily-preaggregation target uses it for bucket_date comparisons.
	castDate bool // date-column cutoff cast flag
	// counter points to a nonnil per-run accumulator owned by runCleanupOnce. The
	// caller copies the backend Deleted value into it only after the target
	// returns without error.
	counter *int64 // completed target result accumulator
}

// opsCleanupDeletedCounts summarizes completed target results and throttling
// for one cleanup run.
type opsCleanupDeletedCounts struct {
	// errorLogs, ingressRejects, alertEvents, systemLogs, logAudits,
	// systemMetrics, hourlyPreagg, and dailyPreagg retain backend counts for
	// targets that returned successfully. Deletes use affected rows; truncations
	// can use estimates, and an errored target result is not copied into these
	// totals.
	errorLogs      int64 // returned error-log deletion count
	ingressRejects int64 // returned ingress aggregate deletion count
	alertEvents    int64 // returned alert-event deletion count
	systemLogs     int64 // returned system-log deletion count
	logAudits      int64 // returned cleanup-audit deletion count
	systemMetrics  int64 // returned system-metric deletion count
	hourlyPreagg   int64 // returned hourly-summary deletion count
	dailyPreagg    int64 // returned daily-summary deletion count
	// batches and throttled sum the backend batch counts and completed throttle
	// waits for successful targets. A nonempty estimated truncation counts as one
	// batch; failed targets can have already persisted work outside these totals.
	batches   int64         // completed nonempty target batches
	throttled time.Duration // completed target throttle duration
}

// String formats completed cleanup counts and throttle duration for job
// diagnostics.
func (c opsCleanupDeletedCounts) String() string {
	// Heartbeat and log output express the accumulated throttle duration in
	// integer milliseconds.
	return fmt.Sprintf(
		"error_logs=%d ingress_rejects=%d alert_events=%d system_logs=%d log_audits=%d system_metrics=%d hourly_preagg=%d daily_preagg=%d batches=%d throttled_ms=%d",
		c.errorLogs,
		c.ingressRejects,
		c.alertEvents,
		c.systemLogs,
		c.logAudits,
		c.systemMetrics,
		c.hourlyPreagg,
		c.dailyPreagg,
		c.batches,
		c.throttled.Milliseconds(),
	)
}

// opsCleanupPlan returns the action and optional cutoff for retention days.
// Negative days return ok false to skip a target, zero selects truncation with
// an unused cutoff, and positive days subtract calendar days from now,
// retaining its time and location.
func opsCleanupPlan(now time.Time, days int) (cutoff time.Time, truncate, ok bool) {
	// Skipped retention targets bypass RunTarget and leave their per-run counters
	// unchanged.
	if days < 0 {
		return time.Time{}, false, false
	}
	// Zero retention selects a whole-table operation, whose backend ignores the
	// cutoff time.
	if days == 0 {
		return time.Time{}, true, true
	}
	return now.AddDate(0, 0, -days), false, true
}
