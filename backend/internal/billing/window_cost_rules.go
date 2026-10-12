package billing

import "time"

// Provider cost windows distinguish requests seeking a new provider from
// sessions already bound to it. Spending below the regular ceiling permits
// both, while the extra reserve keeps bound sessions eligible after that
// ceiling is reached. At the combined ceiling, every scheduling attempt
// is blocked; a nonpositive regular limit disables this cost check.
//
// Window queries also need a usable start for their standard-cost
// aggregation. A present start is retained while its recorded end is
// future, even when that start itself is unusual. Missing bounds or an
// elapsed end select the current local hour so the guard can query a
// current period. These helpers return decisions and timestamps for the
// guard to apply; they perform no cache or persistence updates.

// Cost-window classifications tell WindowCostGuard whether all requests,
// existing sticky sessions, or no requests remain eligible at the
// observed standard-cost total.
const (
	WindowCostSchedulable    WindowCostSchedulability = iota // regular admission is allowed
	WindowCostStickyOnly                                     // existing sticky sessions allowed
	WindowCostNotSchedulable                                 // cost-based admission is blocked
)

// WindowCostSchedulability identifies unrestricted, sticky-only, or blocked cost-window admission.
type WindowCostSchedulability int

// CheckWindowCost classifies the observed provider cost against a regular
// limit and additional reserve. A nonpositive limit permits unrestricted
// admission. Otherwise costs below limit are unrestricted, costs from
// limit through strictly below limit plus reserve are sticky-only, and
// costs at or above that combined ceiling are blocked.
//
// The caller applies the sticky-only result to its session binding.
// For limit ten and reserve two, cost ten permits a bound session and
// cost twelve blocks it. A nonpositive reserve leaves no sticky-only
// interval; this helper uses supplied values without normalizing them.
func CheckWindowCost(current, limit, reserve float64) WindowCostSchedulability {
	if limit <= 0 || current < limit {
		return WindowCostSchedulable
	}
	if current < limit+reserve {
		return WindowCostStickyOnly
	}
	return WindowCostNotSchedulable
}

// CurrentCostWindowStart returns the supplied start when both bounds
// exist and end is future relative to now. Otherwise it returns the
// current hour in now's location; it does not validate that a retained
// start precedes now or end.
func CurrentCostWindowStart(start, end *time.Time, now time.Time) time.Time {
	if start != nil && end != nil && now.Before(*end) {
		return *start
	}
	// Align the replacement query start to the local hour used by provider
	// standard-cost reads when no active pair of window bounds is available.
	return time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
}
