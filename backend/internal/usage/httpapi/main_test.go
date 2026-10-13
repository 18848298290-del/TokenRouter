package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// Gin handlers under test use the framework's test mode for route execution.
// Package setup selects that mode before running the registered tests, so
// individual cases can create routers and request contexts using the same
// configuration.

// TestMain selects Gin test mode before the package's tests execute. It
// invokes the testing runner once, leaving each HTTP case to configure its
// own routes and dependencies.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
