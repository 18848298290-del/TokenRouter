package ops

import (
	"fmt"
	"time"
)

const (
	opsCleanupCronStopTimeout  = 3 * time.Second
	opsCleanupRunTimeout       = 30 * time.Minute
	opsCleanupHeartbeatTimeout = 2 * time.Second
)

type opsCleanupTarget struct {
	retentionDays int
	table         string
	timeCol       string
	castDate      bool
	counter       *int64
}
type opsCleanupDeletedCounts struct {
	errorLogs      int64
	ingressRejects int64
	alertEvents    int64
	systemLogs     int64
	logAudits      int64
	systemMetrics  int64
	hourlyPreagg   int64
	dailyPreagg    int64
	batches        int64
	throttled      time.Duration
}

func (c opsCleanupDeletedCounts) String() string {
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

// opsCleanupPlan converts retention days into a cleanup action.
// Negative days skip cleanup for compatibility with legacy values.
// Zero selects TRUNCATE; positive days select batched deletion before now minus that many days.
func opsCleanupPlan(now time.Time, days int) (cutoff time.Time, truncate, ok bool) {
	if days < 0 {
		return time.Time{}, false, false
	}
	if days == 0 {
		return time.Time{}, true, true
	}
	return now.AddDate(0, 0, -days), false, true
}
