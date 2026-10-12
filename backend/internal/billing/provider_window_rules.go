package billing

import (
	"time"
)

const (
	ProviderDailyWindow ProviderWindowPeriod = iota
	ProviderWeeklyWindow
)

// ProviderWindowPeriod selects a provider's calendar-day or calendar-week window.
type ProviderWindowPeriod uint8

type FixedProviderWindow struct {
	Period    ProviderWindowPeriod
	Mode      string
	Limit     float64
	Hour, Day int
}

// NextFixedDailyReset returns the first daily reset strictly after after.
func NextFixedDailyReset(hour int, tz *time.Location, after time.Time) time.Time {
	t := after.In(tz)
	today := time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, tz)
	if !after.Before(today) {
		return today.AddDate(0, 0, 1)
	}
	return today
}

// LastFixedDailyReset returns the latest daily reset at or before now.
func LastFixedDailyReset(hour int, tz *time.Location, now time.Time) time.Time {
	t := now.In(tz)
	today := time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, tz)
	if now.Before(today) {
		return today.AddDate(0, 0, -1)
	}
	return today
}

// NextFixedWeeklyReset returns the first weekly reset strictly after after.
// day ranges from 0 (Sunday) through 6 (Saturday).
func NextFixedWeeklyReset(day, hour int, tz *time.Location, after time.Time) time.Time {
	t := after.In(tz)
	todayReset := time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, tz)
	currentDay := int(todayReset.Weekday())

	daysForward := (day - currentDay + 7) % 7
	if daysForward == 0 && !after.Before(todayReset) {
		daysForward = 7
	}
	return todayReset.AddDate(0, 0, daysForward)
}

// LastFixedWeeklyReset returns the latest weekly reset at or before now.
func LastFixedWeeklyReset(day, hour int, tz *time.Location, now time.Time) time.Time {
	t := now.In(tz)
	todayReset := time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, tz)
	currentDay := int(todayReset.Weekday())

	daysBack := (currentDay - day + 7) % 7
	if daysBack == 0 && now.Before(todayReset) {
		daysBack = 7
	}
	return todayReset.AddDate(0, 0, -daysBack)
}

func (w FixedProviderWindow) normalized() FixedProviderWindow {
	if w.Hour < 0 || w.Hour > 23 {
		w.Hour = 0
	}
	if w.Day < 0 || w.Day > 6 {
		w.Day = 1
	}
	return w
}

// NextProviderFixedReset returns the next fixed reset, or a zero time and false for other modes.
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

// ReconcileProviderFixedWindow returns the current fixed window's start and whether the caller should persist a reset.
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
