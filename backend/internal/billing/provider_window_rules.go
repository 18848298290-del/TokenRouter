package billing

import (
	"time"
)

// Fixed provider quotas reset according to a local clock hour and,
// for weekly quotas, a weekday. Publishing the next reset timestamp
// and reconciling stored usage have different eligibility rules:
// publication needs fixed mode, while reconciliation also requires
// a limit greater than zero and a missing or older stored start.
//
// The boundary helpers construct a reset date in the supplied
// location and move by calendar days using AddDate. They inherit
// time.Date's interpretation of skipped or repeated local times,
// including any normalized clock value used as the base of a later
// calendar shift. Reconciliation returns the computed boundary and
// a reset decision for its caller; the caller clears usage and
// persists the start when that decision is true.

// Provider-period labels select local daily or weekly date calculations
// when fixed quota configuration is interpreted by reset publication
// and reconciliation.
const (
	ProviderDailyWindow  ProviderWindowPeriod = iota // daily reset date
	ProviderWeeklyWindow                             // weekly reset date
)

// ProviderWindowPeriod identifies daily or weekly reset-date calculation for
// provider quotas.
type ProviderWindowPeriod uint8

// FixedProviderWindow carries the configured quota period, mode, limit, and reset clock.
type FixedProviderWindow struct {
	// Period selects weekly calculation only for ProviderWeeklyWindow.
	// Wrapper date calculations use the daily branch for every other
	// period value, including the ordinary daily label.
	Period ProviderWindowPeriod // configured quota period
	// Mode activates these helpers when it is exactly fixed. Other
	// strings produce a zero timestamp and false result, leaving
	// nonfixed quota-window handling to the caller's other rules.
	Mode string
	// Limit is consulted by reconciliation and must satisfy greater
	// than zero there. Nonpositive and NaN values skip reconciliation;
	// next-reset publication does not consult this amount.
	Limit float64
	// Hour and Day select local reset time and weekly weekday. The
	// wrapper helpers normalize invalid hours to midnight and invalid
	// weekdays to Monday before calling the date calculations; direct
	// boundary helpers receive supplied clock values.
	Hour, Day int // Hour: local reset hour; Day: local reset weekday
}

// NextFixedDailyReset constructs today's local reset and advances
// one calendar day when after is at or past it. Supply a nonnil
// location and the intended clock hour; time.Date and AddDate
// determine normalization at skipped or repeated local times.
func NextFixedDailyReset(hour int, tz *time.Location, after time.Time) time.Time {
	t := after.In(tz)
	today := time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, tz)
	if !after.Before(today) {
		return today.AddDate(0, 0, 1)
	}
	return today
}

// LastFixedDailyReset constructs today's local reset and moves
// one calendar day backward when now is earlier. Supply a nonnil
// location and the intended clock hour; the returned boundary
// retains Go date normalization for clock transitions.
func LastFixedDailyReset(hour int, tz *time.Location, now time.Time) time.Time {
	t := now.In(tz)
	today := time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, tz)
	if now.Before(today) {
		return today.AddDate(0, 0, -1)
	}
	return today
}

// NextFixedWeeklyReset computes a weekly boundary from the local
// reset date and weekday offset, advancing a whole week when that
// weekday's constructed boundary has passed. Supply a nonnil timezone,
// day zero through six for Sunday through Saturday, and the
// intended clock hour.
func NextFixedWeeklyReset(day, hour int, tz *time.Location, after time.Time) time.Time {
	t := after.In(tz)
	todayReset := time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, tz)
	currentDay := int(todayReset.Weekday())

	// Modulo seven maps the requested weekday into the forward calendar cycle.
	daysForward := (day - currentDay + 7) % 7
	// The same weekday has no remaining reset when its constructed
	// clock boundary is at or before after. Advance seven calendar
	// days so publication targets the following weekly occurrence.
	if daysForward == 0 && !after.Before(todayReset) {
		daysForward = 7
	}
	return todayReset.AddDate(0, 0, daysForward)
}

// LastFixedWeeklyReset computes the preceding weekly boundary
// from the local reset date and weekday offset. When now is earlier
// than the constructed same-weekday reset, it moves back a whole
// week. Supply a nonnil timezone and day zero through six.
func LastFixedWeeklyReset(day, hour int, tz *time.Location, now time.Time) time.Time {
	t := now.In(tz)
	todayReset := time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, tz)
	currentDay := int(todayReset.Weekday())

	// Modulo seven maps the requested weekday into the preceding calendar cycle.
	daysBack := (currentDay - day + 7) % 7
	// Before the constructed reset on the matching weekday, the
	// current weekly quota still belongs to the previous week.
	// Move the selected boundary back seven calendar days.
	if daysBack == 0 && now.Before(todayReset) {
		daysBack = 7
	}
	return todayReset.AddDate(0, 0, -daysBack)
}

// normalized returns a window value with unusable clock fields
// replaced by midnight and Monday. It retains period, mode, and
// limit as supplied; wrappers use this value before constructing
// local reset dates.
func (w FixedProviderWindow) normalized() FixedProviderWindow {
	if w.Hour < 0 || w.Hour > 23 {
		w.Hour = 0
	}
	if w.Day < 0 || w.Day > 6 {
		w.Day = 1
	}
	return w
}

// NextProviderFixedReset returns a computed reset timestamp with
// true for exact fixed mode, or zero and false for other modes.
// It normalizes hour/day and selects weekly calculation only for
// ProviderWeeklyWindow; other periods select daily calculation.
// A usable timezone is required on the active calculation path.
func NextProviderFixedReset(window FixedProviderWindow, tz *time.Location, now time.Time) (time.Time, bool) {
	if window.Mode != "fixed" {
		return time.Time{}, false
	}
	window = window.normalized()
	if window.Period == ProviderWeeklyWindow {
		return NextFixedWeeklyReset(window.Day, window.Hour, tz, now), true
	}
	return NextFixedDailyReset(window.Hour, tz, now), true
}

// ReconcileProviderFixedWindow returns the computed current fixed
// boundary and whether stored usage needs a reset. Nonfixed mode
// and limits that do not compare greater than zero return zero
// and false before timezone calculations; callers provide a nonnil
// timezone when reconciliation is active.
//
// Hour and day are normalized before choosing weekly or daily
// calculation. A zero or earlier stored start requests a reset,
// while an equal or later start retains existing usage. The boundary
// is returned even when no reset is needed, and the caller owns
// usage mutation and persistence.
func ReconcileProviderFixedWindow(window FixedProviderWindow, start time.Time, tz *time.Location, now time.Time) (time.Time, bool) {
	if window.Mode != "fixed" || !(window.Limit > 0) {
		return time.Time{}, false
	}
	window = window.normalized()
	var last time.Time
	if window.Period == ProviderWeeklyWindow {
		last = LastFixedWeeklyReset(window.Day, window.Hour, tz, now)
	} else {
		last = LastFixedDailyReset(window.Hour, tz, now)
	}
	return last, start.IsZero() || start.Before(last)
}
