package httpapi

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// Gin test mode is installed before the handler package tests execute. The
// package entry point waits for the test runner, so the request-parser and
// scope fixtures share that configured mode throughout their test execution.

// TestMain sets Gin global test mode before running this package tests. It
// waits for m.Run to finish, allowing the handler fixtures to use that
// configured mode.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}
