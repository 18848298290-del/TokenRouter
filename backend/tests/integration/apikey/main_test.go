package apikey_test

import (
	"testing"
	"time"
)

// Storage timestamps are interpreted with a UTC process timezone in this test
// package. TestMain selects UTC before the runner starts so persisted times and
// window expectations use one timezone across SQLite and integration cases.

// TestMain selects UTC before executing the package test runner. Storage cases
// therefore share one process-local timezone when checking timestamps and quota
// window starts.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	m.Run()
}
