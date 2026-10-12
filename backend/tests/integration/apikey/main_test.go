package apikey_test

import (
	"testing"
	"time"
)

// TestMain sets the process timezone to UTC before running storage tests.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	m.Run()
}
