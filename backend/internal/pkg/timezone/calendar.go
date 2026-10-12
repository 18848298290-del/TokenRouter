package timezone

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Calendar holds the location and clock used for date calculations.
type Calendar struct {
	location *time.Location
	now      func() time.Time
}

// NewCalendar uses loc for calendar calculations; nil selects time.Local.
func NewCalendar(loc *time.Location) Calendar {
	return Calendar{location: loc}
}

// NewCalendarWithClock uses a supplied clock; nil reads system time.
func NewCalendarWithClock(loc *time.Location, now func() time.Time) Calendar {
	return Calendar{location: loc, now: now}
}

// Location returns the calendar location, or time.Local for the zero value.
func (c Calendar) Location() *time.Location {
	if c.location == nil {
		return time.Local
	}
	return c.location
}

// Now reads the configured clock and converts to the calendar location when set.
// With no location, it returns the clock value unchanged, including its monotonic reading.
func (c Calendar) Now() time.Time {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	value := now()
	if c.location == nil {
		return value
	}
	return value.In(c.Location())
}

// UTCOffset formats the location's UTC offset at t as hours and minutes.
func (c Calendar) UTCOffset(t time.Time) string {
	_, offset := t.In(c.Location()).Zone()
	hours := offset / 3600
	minutes := (offset % 3600) / 60
	if minutes < 0 {
		minutes = -minutes
	}
	sign := "+"
	if hours < 0 {
		sign = "-"
		hours = -hours
	}
	return fmt.Sprintf("%s%02d:%02d", sign, hours, minutes)
}

// StartOfDay returns midnight on t's date in the calendar location.
func (c Calendar) StartOfDay(t time.Time) time.Time {
	loc := c.Location()
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// Today returns midnight on the current date in the calendar location.
func (c Calendar) Today() time.Time {
	return c.StartOfDay(c.Now())
}

// EndOfDay returns the last nanosecond of t's date in the calendar location.
func (c Calendar) EndOfDay(t time.Time) time.Time {
	loc := c.Location()
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 999999999, loc)
}

// StartOfWeek returns Monday at midnight in the calendar location.
func (c Calendar) StartOfWeek(t time.Time) time.Time {
	loc := c.Location()
	t = t.In(loc)
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7 // Treat Sunday as the seventh day of the week.
	}
	return time.Date(t.Year(), t.Month(), t.Day()-weekday+1, 0, 0, 0, 0, loc)
}

// StartOfMonth returns midnight on the first day of t's month in the calendar location.
func (c Calendar) StartOfMonth(t time.Time) time.Time {
	loc := c.Location()
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
}

// ParseInUserLocation parses in userTZ, using the calendar location if it is invalid.
func (c Calendar) ParseInUserLocation(layout, value, userTZ string) (time.Time, error) {
	loc := c.Location()
	if userTZ != "" {
		if userLoc, err := time.LoadLocation(userTZ); err == nil {
			loc = userLoc
		}
	}
	return time.ParseInLocation(layout, value, loc)
}

// ParseDateTimeInUserLocation parses a date or datetime and reports date-only input.
// RFC3339 input retains its offset; unzoned input uses userTZ or the calendar location.
func (c Calendar) ParseDateTimeInUserLocation(value, userTZ string) (parsed time.Time, dateOnly bool, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false, fmt.Errorf("empty datetime")
	}

	if t, parseErr := time.Parse(time.RFC3339Nano, value); parseErr == nil {
		return t, false, nil
	}

	loc := c.Location()
	if userTZ != "" {
		if userLoc, loadErr := time.LoadLocation(userTZ); loadErr == nil {
			loc = userLoc
		}
	}

	layouts := []struct {
		layout   string
		dateOnly bool
	}{
		{layout: "2006-01-02", dateOnly: true},
		{layout: "2006-01-02T15:04:05", dateOnly: false},
		{layout: "2006-01-02T15:04", dateOnly: false},
		{layout: "2006-01-02 15:04:05", dateOnly: false},
		{layout: "2006-01-02 15:04", dateOnly: false},
	}
	for _, candidate := range layouts {
		if t, parseErr := time.ParseInLocation(candidate.layout, value, loc); parseErr == nil {
			return t, candidate.dateOnly, nil
		}
	}

	return time.Time{}, false, fmt.Errorf("invalid datetime %q", value)
}

// NowInUserLocation converts the current clock value to a valid userTZ; otherwise it returns Now.
func (c Calendar) NowInUserLocation(userTZ string) time.Time {
	if userTZ == "" {
		return c.Now()
	}
	if userLoc, err := time.LoadLocation(userTZ); err == nil {
		return c.Now().In(userLoc)
	}
	return c.Now()
}

// StartOfDayInUserLocation returns midnight in userTZ, using the calendar location if invalid.
func (c Calendar) StartOfDayInUserLocation(t time.Time, userTZ string) time.Time {
	loc := c.Location()
	if userTZ != "" {
		if userLoc, err := time.LoadLocation(userTZ); err == nil {
			loc = userLoc
		}
	}
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// ParseFlexibleTimestamp parses RFC3339 timestamps, including fractional seconds.
func ParseFlexibleTimestamp(raw string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000Z",
	}
	for _, format := range formats {
		if ts, err := time.Parse(format, raw); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, strconv.ErrSyntax
}
