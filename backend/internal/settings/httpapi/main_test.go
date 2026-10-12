package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain sets Gin test mode before invoking the HTTP package's tests.
// This shared setup keeps request fixtures in the framework's test mode while
// the runner determines each test's result.
func TestMain(m *testing.M) {
	// Select the global Gin mode before the runner starts request fixtures so
	// their router construction observes the same test configuration throughout this package run.
	gin.SetMode(gin.TestMode)
	m.Run()
}
