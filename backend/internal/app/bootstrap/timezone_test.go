package bootstrap

import (
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// Process timezone changes affect time.Now and calendars built afterward.
// Each test restores time.Local during cleanup so valid, invalid, and default
// bootstrap settings can be checked without carrying a previous test's location
// into later cases.

// preserveTimezone captures time.Local and restores it through test cleanup.
// Call it before changing the global timezone; tests using this helper run
// serially because the saved value is process-wide.
func preserveTimezone(t *testing.T) {
	t.Helper()
	previous := time.Local
	// Restore the shared timezone after assertions so subsequent bootstrap tests
	// observe the location that existed before this test changed process state.
	t.Cleanup(func() { time.Local = previous })
}

// TestInit selects Asia/Shanghai through bootstrap. The process timezone and
// a calendar created afterward must both expose that location to their
// respective time consumers.
func TestInit(t *testing.T) {
	preserveTimezone(t)
	if err := InitTimezone("Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	if time.Local.String() != "Asia/Shanghai" {
		t.Errorf("time.Local not set correctly: %s", time.Local)
	}
	calendar := timezone.NewCalendar(time.Local)
	if calendar.Location().String() != "Asia/Shanghai" {
		t.Errorf("calendar did not capture bootstrap location: %s", calendar.Location())
	}
}

// TestInitInvalidTimezone supplies a location that cannot be loaded. Bootstrap
// must return an error and retain the previous process timezone for later
// calendar construction.
func TestInitInvalidTimezone(t *testing.T) {
	preserveTimezone(t)
	previous := time.Local
	if err := InitTimezone("Invalid/Timezone"); err == nil {
		t.Fatal("invalid timezone must fail")
	}
	if time.Local != previous {
		t.Fatal("failed initialization changed timezone")
	}
}

// TestInitDefaultTimezone leaves the deployment timezone empty. Bootstrap must
// select Asia/Shanghai as the process location so application calendars receive
// the documented default timezone.
func TestInitDefaultTimezone(t *testing.T) {
	preserveTimezone(t)
	if err := InitTimezone(""); err != nil {
		t.Fatal(err)
	}
	if time.Local.String() != "Asia/Shanghai" {
		t.Fatalf("default timezone changed: %s", time.Local)
	}
}

// TestTimeNowAffected captures current time under UTC and Asia/Shanghai.
// Their zone offsets must differ by eight hours, showing that successful
// bootstrap changes affect subsequent time.Now values.
func TestTimeNowAffected(t *testing.T) {
	preserveTimezone(t)
	if err := InitTimezone("UTC"); err != nil {
		t.Fatal(err)
	}
	utcNow := time.Now()
	if err := InitTimezone("Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	shanghaiNow := time.Now()
	_, utcOffset := utcNow.Zone()
	_, shanghaiOffset := shanghaiNow.Zone()
	// Eight hours are compared in the seconds returned by Time.Zone.
	if difference := shanghaiOffset - utcOffset; difference != 8*3600 {
		t.Errorf("timezone offset difference: got %d, want %d", difference, 8*3600)
	}
}
