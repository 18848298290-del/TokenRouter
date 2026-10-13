//go:build !integration

package payment_test

import (
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Payment unit cases run HTTP and SQLite scenarios with Gin configured for
// tests and UTC as their process-local timezone. This nonintegration runner
// performs that setup before the Go test runner, keeping date expectations
// consistent across the unit fixtures.

// TestMain selects Gin test mode and UTC for nonintegration payment cases
// before running the package. Their HTTP recorder and timestamp assertions then
// share the configured process environment.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	// UTC makes time fixtures independent of the host time zone.
	time.Local = time.UTC
	m.Run()
}
