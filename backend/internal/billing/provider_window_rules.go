package billing

import (
	"time"
)

// provider_window_rules.go computes fixed provider reset times and reconciles
// stored quota windows. Daily, weekly, and monthly schedules retain their
// configured local hour when moving to another calendar date. Publication needs
// fixed mode, while reconciliation also requires a positive limit and a missing
// or older stored start. Callers persist usage resets when reconciliation returns
// true and publish the next timestamp used by transactional accounting.
//
// Calendar dates are selected before resolving their timezone offset. The
// shared resolver follows PostgreSQL: a skipped clock time uses the offset
// before the forward transition, and a repeated time uses the offset after the
// backward transition. Admission, configuration, and SQL accounting therefore
// select the same reset instant, including non-hour clock changes. Each later
// date starts from the configured hour, so normalization at one transition
// cannot shift subsequent resets.

// Provider-period labels select local daily, weekly, or monthly date calculations
// when fixed quota configuration is interpreted by reset publication
// and reconciliation.
const (
	ProviderDailyWindow   ProviderWindowPeriod = iota // daily reset date
	ProviderWeeklyWindow                              // weekly reset date
	ProviderMonthlyWindow                             // monthly reset date
)

// ProviderWindowPeriod identifies daily, weekly, or monthly reset-date
// calculation for provider quotas.
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

// NextFixedDailyReset returns the first daily reset strictly after after.
// Supply a nonnil location and an hour from zero through twenty-three. Every
// date resolves that configured hour using PostgreSQL's clock-transition rules;
// equality advances to the following eligible calendar date.
func NextFixedDailyReset(hour int, tz *time.Location, after time.Time) time.Time {
	t := after.In(tz)
	for day := t.Day(); ; day++ {
		reset := providerResetTime(t.Year(), t.Month(), day, hour, tz)
		if after.Before(reset) {
			return reset
		}
	}
}

// LastFixedDailyReset returns the latest daily reset at or before now.
// Supply a nonnil location and an hour from zero through twenty-three. Calendar
// dates are retried when a skipped local date resolves into the future, while
// each candidate retains the configured hour and PostgreSQL's offset choice.
func LastFixedDailyReset(hour int, tz *time.Location, now time.Time) time.Time {
	t := now.In(tz)
	for day := t.Day(); ; day-- {
		reset := providerResetTime(t.Year(), t.Month(), day, hour, tz)
		if !now.Before(reset) {
			return reset
		}
	}
}

// NextFixedWeeklyReset returns the first scheduled weekly reset after after.
// Supply a nonnil location, a Sunday-zero weekday, and an hour from zero through
// twenty-three. The weekday selects a calendar date before its clock is
// resolved, so a skipped hour on today's date cannot change a later reset's
// configured hour. Equality advances one week, and each target date uses
// PostgreSQL's interpretation of repeated or skipped local times.
func NextFixedWeeklyReset(day, hour int, tz *time.Location, after time.Time) time.Time {
	t := after.In(tz)
	// Modulo seven maps the requested weekday into the forward calendar cycle.
	daysForward := (day - int(t.Weekday()) + 7) % 7
	for ; ; daysForward += 7 {
		reset := providerResetTime(t.Year(), t.Month(), t.Day()+daysForward, hour, tz)
		if after.Before(reset) {
			return reset
		}
	}
}

// LastFixedWeeklyReset returns the latest weekly reset at or before now.
// Supply a nonnil location, a Sunday-zero weekday, and an hour from zero through
// twenty-three. Candidate dates retain that hour even when a clock transition
// normalizes another date to a different time. A target that resolves after now
// moves back one calendar week; equality belongs to the current window, using
// the same timezone offset rules as transactional accounting.
func LastFixedWeeklyReset(day, hour int, tz *time.Location, now time.Time) time.Time {
	t := now.In(tz)
	// Modulo seven maps the requested weekday into the preceding calendar cycle.
	daysBack := (int(t.Weekday()) - day + 7) % 7
	for ; ; daysBack += 7 {
		reset := providerResetTime(t.Year(), t.Month(), t.Day()-daysBack, hour, tz)
		if !now.Before(reset) {
			return reset
		}
	}
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
// thirty-one, and a valid clock hour. The selected date uses PostgreSQL's
// interpretation of repeated and skipped local times.
func monthlyResetDate(year int, month time.Month, day, hour int, tz *time.Location) time.Time {
	// Clamping before time.Date prevents a short month from spilling its quota
	// reset into the following month and skipping that month's allowance entirely.
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	return providerResetTime(year, month, min(day, lastDay), hour, tz)
}

// providerResetTime resolves a calendar clock using PostgreSQL's offset rules.
// Supply a nonnil location and a valid hour; overflowing dates normalize before
// timezone lookup. Repeated clocks use the post-transition offset, while skipped
// clocks use the pre-transition offset and consequently move forward by the gap.
//
// Go can choose either side of a transition, so the initial candidate supplies
// its adjacent zone boundaries rather than the final ambiguity policy. Comparing
// the requested wall clock with each transition's local interval identifies
// gaps and overlaps independently of their duration or daylight-saving labels.
// Both cases use the smaller offset, matching SQL accounting's reset timestamp.
//
// @project-doc docs/domains/routing_and_billing.md#provider_spending_limits
func providerResetTime(year int, month time.Month, day, hour int, tz *time.Location) time.Time {
	wall := time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
	date := time.Date(wall.Year(), wall.Month(), wall.Day(), hour, 0, 0, 0, tz)
	start, end := date.ZoneBounds()
	for _, transition := range []time.Time{start, end} {
		if transition.IsZero() {
			continue
		}
		_, beforeOffset := transition.Add(-time.Nanosecond).Zone()
		_, afterOffset := transition.Zone()
		// Interpret both transition endpoints as wall clocks, allowing the same
		// comparison to cover forward gaps and backward overlaps of any duration.
		lower := transition.UTC().Add(time.Duration(min(beforeOffset, afterOffset)) * time.Second)
		upper := transition.UTC().Add(time.Duration(max(beforeOffset, afterOffset)) * time.Second)
		if !wall.Before(lower) && wall.Before(upper) {
			return wall.Add(-time.Duration(min(beforeOffset, afterOffset)) * time.Second).In(tz)
		}
	}
	return date
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
