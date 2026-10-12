package billing

import "time"

const (
	WindowCostSchedulable WindowCostSchedulability = iota
	WindowCostStickyOnly
	WindowCostNotSchedulable
)

// WindowCostSchedulability distinguishes unrestricted, sticky-only, and blocked scheduling.
type WindowCostSchedulability int

func CheckWindowCost(current, limit, reserve float64) WindowCostSchedulability {
	if limit <= 0 || current < limit {
		return WindowCostSchedulable
	}
	if current < limit+reserve {
		return WindowCostStickyOnly
	}
	return WindowCostNotSchedulable
}

// CurrentCostWindowStart returns the active start, or the current local hour when the window is expired or incomplete.
func CurrentCostWindowStart(start, end *time.Time, now time.Time) time.Time {
	if start != nil && end != nil && now.Before(*end) {
		return *start
	}
	return time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
}
