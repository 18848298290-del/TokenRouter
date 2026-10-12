package timezone

import (
	"reflect"
	"testing"
	"time"
)

// TestCalendarKeepsExplicitLocation checks independent locations and DST day bounds.
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

// TestUninitializedClockKeepsMonotonicReading checks that default clock values retain monotonic data.
func TestUninitializedClockKeepsMonotonicReading(t *testing.T) {
	calendar := NewCalendar(nil)
	for name, value := range map[string]time.Time{
		"empty_user":   calendar.NowInUserLocation(""),
		"invalid_user": calendar.NowInUserLocation("invalid/timezone"),
		"calendar":     calendar.Now(),
	} {
		// Compare full Time values because Round(0) removes monotonic data.
		if reflect.DeepEqual(value, value.Round(0)) {
			t.Errorf("%s: initialization fallback must preserve monotonic time", name)
		}
	}
}

// TestCalendarInjectedClock checks date boundaries and user-zone conversion with a fixed clock.
func TestCalendarInjectedClock(t *testing.T) {
	fixed := time.Date(2026, time.October, 5, 23, 30, 0, 0, time.UTC)
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

// testCalendar creates a calendar using the named test location.
func testCalendar(t *testing.T, name string) Calendar {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return NewCalendar(location)
}

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

func TestStartOfDay(t *testing.T) {
	calendar := testCalendar(t, "Asia/Shanghai")
	input := time.Date(2024, 6, 15, 15, 30, 45, 123456789, calendar.Location())
	start := calendar.StartOfDay(input)
	want := time.Date(2024, 6, 15, 0, 0, 0, 0, calendar.Location())
	if !start.Equal(want) {
		t.Errorf("StartOfDay: got %v, want %v", start, want)
	}
}

func TestTruncateVsStartOfDay(t *testing.T) {
	calendar := testCalendar(t, "Asia/Shanghai")
	now := calendar.Now()
	truncated := now.Truncate(24 * time.Hour)
	start := calendar.StartOfDay(now)
	t.Logf("Now: %v, Truncate(24h): %v, StartOfDay: %v", now, truncated, start)
	if start.Hour() != 0 {
		t.Errorf("StartOfDay should be at hour 0, got %d", start.Hour())
	}
}

func TestDSTAwareness(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("America/New_York timezone not available: %v", err)
	}
	calendar := NewCalendar(location)
	_ = calendar.Today()
	_ = calendar.Now()
	_ = calendar.StartOfDay(calendar.Now())
}

func TestStartOfWeek_Boundaries(t *testing.T) {
	calendar := testCalendar(t, "Asia/Shanghai")
	location := calendar.Location()
	want := time.Date(2026, 5, 18, 0, 0, 0, 0, location)
	cases := []struct {
		name string
		in   time.Time
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
