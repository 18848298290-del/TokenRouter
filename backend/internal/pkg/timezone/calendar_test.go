package timezone

import (
	"reflect"
	"testing"
	"time"
)

// TestCalendarKeepsExplicitLocation constructs New York and UTC calendars
// around a daylight-saving transition. Local day bounds must span twenty-three
// hours, invalid user zones must fall back, and the week must begin Monday.
func TestCalendarKeepsExplicitLocation(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	calendar := NewCalendar(loc)
	day := time.Date(2026, 3, 8, 12, 0, 0, 0, loc)
	start := calendar.StartOfDay(day)
	end := calendar.EndOfDay(day)
	if end.Add(time.Nanosecond).Sub(start) != 23*time.Hour {
		t.Fatalf("DST day bounds: %s %s", start, end)
	}
	if NewCalendar(time.UTC).StartOfDay(day).Equal(start) {
		t.Fatal("independent calendars must keep different day boundaries")
	}
	parsed, err := calendar.ParseInUserLocation("2006-01-02", "2026-03-08", "invalid/timezone")
	if err != nil || !parsed.Equal(start) {
		t.Fatalf("invalid user timezone fallback: %s %v", parsed, err)
	}
	if calendar.StartOfWeek(day).Weekday() != time.Monday {
		t.Fatal("week must start on Monday")
	}
}

// TestUninitializedClockKeepsMonotonicReading reads an unset-location calendar
// through current-time and invalid-user-zone paths. Each returned system time
// must retain monotonic data detectable by comparison with Round(0).
func TestUninitializedClockKeepsMonotonicReading(t *testing.T) {
	calendar := NewCalendar(nil)
	for name, value := range map[string]time.Time{
		"empty_user":   calendar.NowInUserLocation(""),
		"invalid_user": calendar.NowInUserLocation("invalid/timezone"),
		"calendar":     calendar.Now(),
	} {
		// Round(0) strips the monotonic reading while retaining the wall-clock instant.
		// Comparing full values detects whether the no-location path retained that reading.
		if reflect.DeepEqual(value, value.Round(0)) {
			t.Errorf("%s: initialization fallback must preserve monotonic time", name)
		}
	}
}

// TestCalendarInjectedClock supplies a fixed UTC instant close to midnight and
// a UTC+8 calendar. Local day selection, user-zone conversion, unchanged
// unset-location time, and nil-clock system-time selection must follow their
// expected rules.
func TestCalendarInjectedClock(t *testing.T) {
	fixed := time.Date(2026, time.October, 5, 23, 30, 0, 0, time.UTC)
	// Express the eight-hour offset in seconds for FixedZone.
	loc := time.FixedZone("UTC+8", 8*60*60)
	calendar := NewCalendarWithClock(loc, func() time.Time { return fixed })
	if got := calendar.Today(); got.Day() != 6 || got.Hour() != 0 {
		t.Fatalf("日界线计算错误: %v", got)
	}
	if got := calendar.NowInUserLocation("UTC"); !got.Equal(fixed) || got.Location() != time.UTC {
		t.Fatalf("用户时区未使用固定时钟: %v", got)
	}
	if got := NewCalendarWithClock(nil, func() time.Time { return fixed }).Now(); got != fixed {
		t.Fatalf("未指定时区时应保留时钟返回值: %v", got)
	}
	if got := NewCalendarWithClock(nil, nil).Now(); time.Since(got) > time.Second {
		t.Fatalf("nil 时钟应使用系统时间: %v", got)
	}
}

// testCalendar constructs a calendar using the requested named test zone. It
// marks itself as a helper and fails the calling test when that zone cannot be
// loaded.
func testCalendar(t *testing.T, name string) Calendar {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return NewCalendar(location)
}

// TestToday reads today and the current time from a Shanghai calendar. The
// result must be midnight and carry the same local year, month, and day as the
// observed clock value.
func TestToday(t *testing.T) {
	calendar := testCalendar(t, "Asia/Shanghai")
	today := calendar.Today()
	now := calendar.Now()
	if today.Hour() != 0 || today.Minute() != 0 || today.Second() != 0 {
		t.Errorf("Today() not at start of day: %v", today)
	}
	if today.Year() != now.Year() || today.Month() != now.Month() || today.Day() != now.Day() {
		t.Errorf("Today() date mismatch: today=%v, now=%v", today, now)
	}
}

// TestStartOfDay supplies an afternoon instant with subsecond data in a
// Shanghai calendar. The returned instant must equal midnight on that input's
// local date.
func TestStartOfDay(t *testing.T) {
	calendar := testCalendar(t, "Asia/Shanghai")
	input := time.Date(2024, 6, 15, 15, 30, 45, 123456789, calendar.Location())
	start := calendar.StartOfDay(input)
	want := time.Date(2024, 6, 15, 0, 0, 0, 0, calendar.Location())
	if !start.Equal(want) {
		t.Errorf("StartOfDay: got %v, want %v", start, want)
	}
}

// TestTruncateVsStartOfDay logs fixed-duration truncation beside a Shanghai
// calendar day start. Its observable assertion requires the calendar result to
// have hour zero, while the logged truncation value provides comparison data.
func TestTruncateVsStartOfDay(t *testing.T) {
	calendar := testCalendar(t, "Asia/Shanghai")
	now := calendar.Now()
	// Compare fixed absolute-duration truncation with the calendar's local midnight.
	truncated := now.Truncate(24 * time.Hour)
	start := calendar.StartOfDay(now)
	t.Logf("Now: %v, Truncate(24h): %v, StartOfDay: %v", now, truncated, start)
	if start.Hour() != 0 {
		t.Errorf("StartOfDay should be at hour 0, got %d", start.Hour())
	}
}

// TestDSTAwareness constructs a New York calendar when its zone data is
// available. The current-time and day helpers are exercised for completion;
// unavailable zone data skips this smoke test.
func TestDSTAwareness(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		// Missing host zone data prevents the New York smoke case from being constructed.
		// Report a skip so this fixture does not mistake environment setup for calendar behavior.
		t.Skipf("America/New_York timezone not available: %v", err)
	}
	calendar := NewCalendar(location)
	_ = calendar.Today()
	_ = calendar.Now()
	_ = calendar.StartOfDay(calendar.Now())
}

// TestStartOfWeek_Boundaries supplies Friday, Sunday, and Monday instants from
// the same Shanghai week. Every result must equal that week's Monday midnight,
// including Sunday's day-seven mapping.
func TestStartOfWeek_Boundaries(t *testing.T) {
	calendar := testCalendar(t, "Asia/Shanghai")
	location := calendar.Location()
	want := time.Date(2026, 5, 18, 0, 0, 0, 0, location)
	cases := []struct {
		name string    // subtest display name
		in   time.Time // input local-week instant
	}{
		{"friday", time.Date(2026, 5, 22, 14, 30, 0, 0, location)},
		{"sunday", time.Date(2026, 5, 24, 10, 0, 0, 0, location)},
		{"monday-self", time.Date(2026, 5, 18, 9, 15, 30, 0, location)},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := calendar.StartOfWeek(item.in); !got.Equal(want) {
				t.Errorf("StartOfWeek(%v) = %v, want %v", item.in, got, want)
			}
		})
	}
}
