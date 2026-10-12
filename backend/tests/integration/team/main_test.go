package team_test

import (
	"testing"
	"time"
)

// TestMain sets the process timezone to UTC so PostgreSQL receives a fixed timezone name.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	m.Run()
}
