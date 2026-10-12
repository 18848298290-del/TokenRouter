package timezone

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Daily statistics and date inputs need a calendar location independent of the
// machine's displayed clock. Calendar converts instants to its location before
// constructing midnight, month start, and Monday week start, letting time.Date
// account for date and offset changes. User-specific helpers select a valid
// named user zone and otherwise use the calendar location.
//
// A nil stored location has a separate clock rule: Now returns the configured
// clock's exact time value, retaining its location and monotonic reading, while
// date calculations consult time.Local. Zoned datetime input is parsed before
// local layouts so its supplied offset remains authoritative. Unzoned dates and
// datetimes use the selected location, and date-only parsing reports that
// distinction to range consumers.

// Calendar holds the location and clock used for local date and time
// calculations.
type Calendar struct {
	// location selects date calculation rules. Nil makes Location consult time.Local
	// dynamically, while Now returns its clock value directly to retain monotonic data.
	location *time.Location
	// now supplies instants for Today and current-time helpers. Nil uses time.Now;
	// callers make injected clocks safe for their own concurrent calendar users.
	now func() time.Time
}

// NewCalendar constructs a calendar that reads system time with loc for date
// calculations. Nil loc makes date helpers use time.Local, while Now retains
// the exact system clock value it reads.
func NewCalendar(loc *time.Location) Calendar {
	return Calendar{location: loc}
}

// NewCalendarWithClock constructs a calendar using loc and the supplied clock.
// Nil now selects time.Now; nil loc retains each clock value in Now while date
// calculations obtain their location from time.Local.
func NewCalendarWithClock(loc *time.Location, now func() time.Time) Calendar {
	return Calendar{location: loc, now: now}
}

// Location returns the calendar's configured location, or the current
// time.Local when absent. Zero-valued calendars therefore use process-local
// date calculations, and the returned location pointer is shared with its
// owner.
func (c Calendar) Location() *time.Location {
	if c.location == nil {
		return time.Local
	}
	return c.location
}

// Now reads the configured clock, using time.Now when no clock was supplied.
// With a configured location it converts the instant to that zone; otherwise it
// returns the clock value unchanged, including monotonic data.
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

// UTCOffset formats the calendar zone offset at t as hour and minute text. It
// derives the sign from the whole-hour component, so negative offsets shorter
// than one hour currently display a plus sign.
func (c Calendar) UTCOffset(t time.Time) string {
	_, offset := t.In(c.Location()).Zone()
	// Zone returns seconds; format whole hours and the remaining absolute minutes.
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

// StartOfDay returns midnight for t's date after converting it to the calendar
// location. time.Date resolves that local midnight under the location's rules,
// including days whose elapsed length differs from twenty-four hours.
func (c Calendar) StartOfDay(t time.Time) time.Time {
	loc := c.Location()
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// Today returns local midnight for the date observed by the calendar's clock.
func (c Calendar) Today() time.Time {
	return c.StartOfDay(c.Now())
}

// EndOfDay constructs the final nanosecond of t's local calendar date. It uses
// the location's date rules at 23:59:59.999999999 rather than adding a fixed
// duration to StartOfDay.
func (c Calendar) EndOfDay(t time.Time) time.Time {
	loc := c.Location()
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 999999999, loc)
}

// StartOfWeek returns Monday midnight for the local week containing t. Sunday
// is treated as day seven, and time.Date resolves the selected date even when
// subtraction crosses a month or year.
func (c Calendar) StartOfWeek(t time.Time) time.Time {
	loc := c.Location()
	t = t.In(loc)
	weekday := int(t.Weekday())
	if weekday == 0 {
		// Monday-based subtraction places Sunday at this week's end.
		weekday = 7
	}
	return time.Date(t.Year(), t.Month(), t.Day()-weekday+1, 0, 0, 0, 0, loc)
}

// StartOfMonth returns midnight on the first day of t's local month.
func (c Calendar) StartOfMonth(t time.Time) time.Time {
	loc := c.Location()
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
}

// ParseInUserLocation parses layout and value using a valid userTZ, otherwise
// the calendar location. Empty or invalid user zones retain that location,
// while time.ParseInLocation reports any remaining layout or value error.
func (c Calendar) ParseInUserLocation(layout, value, userTZ string) (time.Time, error) {
	loc := c.Location()
	// Unloadable user zones retain the calendar location so valid date text can
	// still be interpreted using the server's established local-date rules.
	if userTZ != "" {
		if userLoc, err := time.LoadLocation(userTZ); err == nil {
			loc = userLoc
		}
	}
	return time.ParseInLocation(layout, value, loc)
}

// ParseDateTimeInUserLocation parses trimmed timestamp text and identifies
// date-only input. RFC3339Nano is tried first, so a supplied offset determines
// the instant independently of userTZ and the calendar location.
//
// Other accepted forms are a date or an unzoned datetime with a T or space
// separator and minute or second precision. They use a valid userTZ, or the
// calendar location when the user zone is empty or cannot be loaded. Date-only
// forms return true; empty or unsupported text returns an error so range
// callers can reject it.
func (c Calendar) ParseDateTimeInUserLocation(value, userTZ string) (parsed time.Time, dateOnly bool, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false, fmt.Errorf("empty datetime")
	}

	if t, parseErr := time.Parse(time.RFC3339Nano, value); parseErr == nil {
		return t, false, nil
	}

	loc := c.Location()
	// Offset-bearing input already returned above. For unzoned forms, retain the
	// calendar location if the requested user zone cannot be loaded.
	if userTZ != "" {
		if userLoc, loadErr := time.LoadLocation(userTZ); loadErr == nil {
			loc = userLoc
		}
	}

	layouts := []struct {
		layout   string // accepted unzoned timestamp layout
		dateOnly bool   // date-only range input flag
	}{
		{layout: "2006-01-02", dateOnly: true},
		{layout: "2006-01-02T15:04:05", dateOnly: false},
		{layout: "2006-01-02T15:04", dateOnly: false},
		{layout: "2006-01-02 15:04:05", dateOnly: false},
		{layout: "2006-01-02 15:04", dateOnly: false},
	}
	// Try accepted unzoned forms in order and retain the matched date-only flag
	// so range consumers can distinguish whole-day input from a particular instant.
	for _, candidate := range layouts {
		if t, parseErr := time.ParseInLocation(candidate.layout, value, loc); parseErr == nil {
			return t, candidate.dateOnly, nil
		}
	}

	return time.Time{}, false, fmt.Errorf("invalid datetime %q", value)
}

// NowInUserLocation returns the current calendar instant converted to a valid
// userTZ. Empty or invalid zone names return Now as supplied, including the
// clock value's monotonic reading when the calendar location is unset.
func (c Calendar) NowInUserLocation(userTZ string) time.Time {
	if userTZ == "" {
		return c.Now()
	}
	// Failed user-zone lookup leaves Now's result available, including its stored
	// location and any monotonic clock data retained by an uninitialized calendar.
	if userLoc, err := time.LoadLocation(userTZ); err == nil {
		return c.Now().In(userLoc)
	}
	return c.Now()
}

// StartOfDayInUserLocation returns midnight for t in a valid userTZ. Empty or
// invalid user zone names use the calendar location, and time.Date supplies the
// date's local midnight under that zone.
func (c Calendar) StartOfDayInUserLocation(t time.Time, userTZ string) time.Time {
	loc := c.Location()
	// An unavailable user zone leaves the calendar location selected. Convert t
	// there before extracting its date so midnight follows that location's day rules.
	if userTZ != "" {
		if userLoc, err := time.LoadLocation(userTZ); err == nil {
			loc = userLoc
		}
	}
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// ParseFlexibleTimestamp tries the supported RFC3339 timestamp layouts in
// order, including fractional seconds. It does not trim raw, and input accepted
// by no layout returns strconv.ErrSyntax rather than the final parser-specific
// error.
func ParseFlexibleTimestamp(raw string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000Z",
	}
	// Consumers receive one syntax sentinel when all supported layouts fail,
	// allowing consistent rejection without depending on whichever parser ran last.
	for _, format := range formats {
		if ts, err := time.Parse(format, raw); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, strconv.ErrSyntax
}
