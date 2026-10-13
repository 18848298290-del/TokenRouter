package team_test

import (
	"testing"
	"time"
)

// Team storage cases use calendar windows whose computation depends on the
// process timezone. TestMain selects UTC before the package runner starts, so
// billing calendars and timestamp expectations share a fixed local timezone
// across the integration fixtures.

// TestMain sets the process-local timezone to UTC before running team package
// cases. Their calendar-based billing and storage assertions therefore begin
// with the same timezone setting.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	m.Run()
}
