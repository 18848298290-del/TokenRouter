package billing

import (
	"time"
)

// provider_window_rules.go computes fixed provider reset times and reconciles
// stored quota windows.
// Fixed provider quotas reset according to a local clock hour and,
// for weekly quotas, a weekday, or for monthly quotas, a day of month.
// Publishing the next reset timestamp
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

// Provider-period labels select local daily, weekly, or monthly date calculations
// when fixed quota configuration is interpreted by reset publication
// and reconciliation.
const (
	ProviderDailyWindow   ProviderWindowPeriod = iota // daily reset date
	ProviderWeeklyWindow                              // weekly reset date
	ProviderMonthlyWindow                             // monthly reset date
)

// ProviderWindowPeriod identifies daily, weekly, or monthly reset-date
// calculation for
// provider quotas.
type ProviderWindowPeriod uint8

// FixedProviderWindow carries the configured quota period, mode, limit, and reset clock.
type FixedProviderWindow struct {
	// Period selects weekly or monthly calendar calculations for their named
	// values. Other values select the daily calculation in the wrapper helpers.
	Period ProviderWindowPeriod // configured quota period
	// Mode activates these helpers when it is exactly fixed. Other
	// strings produce a zero timestamp and false result, leaving
	// nonfixed quota-window handling to the caller's other rules.
	Mode string
	// Limit is consulted by reconciliation and must satisfy greater
	// than zero there. Nonpositive and NaN values skip reconciliation;
	// next-reset publication does not consult this amount.
	Limit float64
	// Hour and Day select the local reset hour and weekday or day of month.
	// Wrappers replace invalid hours with midnight and invalid days with one;
	// monthly dates beyond the month length select its final day.
	Hour int // local reset hour
	Day  int // weekday or month date
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
// replaced by midnight and day one. It retains period, mode, and
// limit as supplied; wrappers use this value before constructing
// local reset dates.
func (w FixedProviderWindow) normalized() FixedProviderWindow {
	if w.Hour < 0 || w.Hour > 23 {
		w.Hour = 0
	}
	if (w.Period == ProviderMonthlyWindow && (w.Day < 1 || w.Day > 31)) ||
		(w.Period != ProviderMonthlyWindow && (w.Day < 0 || w.Day > 6)) {
		w.Day = 1
	}
	return w
}

// NextProviderFixedReset returns a computed reset timestamp with
// true for exact fixed mode, or zero and false for other modes.
// It normalizes hour/day and selects monthly or weekly calculation for
// their named periods; other periods select daily calculation.
// A usable timezone is required on the active calculation path.
func NextProviderFixedReset(window FixedProviderWindow, tz *time.Location, now time.Time) (time.Time, bool) {
	if window.Mode != "fixed" {
		return time.Time{}, false
	}
	window = window.normalized()
	switch window.Period {
	case ProviderMonthlyWindow:
		return NextFixedMonthlyReset(window.Day, window.Hour, tz, now), true
	case ProviderWeeklyWindow:
		return NextFixedWeeklyReset(window.Day, window.Hour, tz, now), true
	default:
		return NextFixedDailyReset(window.Hour, tz, now), true
	}
}

// ReconcileProviderFixedWindow returns the computed current fixed
// boundary and whether stored usage needs a reset. Nonfixed mode
// and limits that do not compare greater than zero return zero
// and false before timezone calculations; callers provide a nonnil
// timezone when reconciliation is active.
//
// Hour and day are normalized before choosing monthly, weekly, or daily
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
	switch window.Period {
	case ProviderMonthlyWindow:
		last = LastFixedMonthlyReset(window.Day, window.Hour, tz, now)
	case ProviderWeeklyWindow:
		last = LastFixedWeeklyReset(window.Day, window.Hour, tz, now)
	default:
		last = LastFixedDailyReset(window.Hour, tz, now)
	}
	return last, start.IsZero() || start.Before(last)
}

// monthlyResetDate constructs a local monthly reset with its day clamped to
// the month's final date. Supply a nonnil location, a day from one through
// thirty-one, and a valid clock hour; time.Date defines clock-transition behavior.
func monthlyResetDate(year int, month time.Month, day, hour int, tz *time.Location) time.Time {
	// Clamping before time.Date prevents a short month from spilling its quota
	// reset into the following month and skipping that month's allowance entirely.
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, tz).Day()
	return time.Date(year, month, min(day, lastDay), hour, 0, 0, 0, tz)
}

// NextFixedMonthlyReset returns the next monthly reset strictly after after.
// Callers supply a nonnil location, day one through thirty-one, and hour zero
// through twenty-three; short months reset on their final date.
//
// The current month is tested before advancing, and equality selects the next
// month because the observed reset has already taken effect. Each month uses
// the configured day again, so a February month-end clamp does not shift the
// following March reset to February's shorter day number.
func NextFixedMonthlyReset(day, hour int, tz *time.Location, after time.Time) time.Time {
	t := after.In(tz)
	reset := monthlyResetDate(t.Year(), t.Month(), day, hour, tz)
	if !after.Before(reset) {
		return monthlyResetDate(t.Year(), t.Month()+1, day, hour, tz)
	}
	return reset
}

// LastFixedMonthlyReset returns the current monthly reset at or before now.
// Callers supply a nonnil location, day one through thirty-one, and a valid
// clock hour; the configured day is reapplied separately to each month.
//
// The current month supplies the boundary when its reset has taken effect,
// including equality. Before that local reset instant, the preceding month
// supplies the active window. Reapplying the requested date there prevents a
// short current month from moving the preceding month's reset away from its
// configured calendar date.
func LastFixedMonthlyReset(day, hour int, tz *time.Location, now time.Time) time.Time {
	t := now.In(tz)
	reset := monthlyResetDate(t.Year(), t.Month(), day, hour, tz)
	if now.Before(reset) {
		return monthlyResetDate(t.Year(), t.Month()-1, day, hour, tz)
	}
	return reset
}
